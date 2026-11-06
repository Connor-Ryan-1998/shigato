package property

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"shigoto/internal/core"
)

// isUniqueViolation reports whether err is a Postgres unique_violation
// (SQLSTATE 23505). Used as a defensive, concurrency-safe backstop behind a
// pre-insert check for the same condition (e.g. two concurrent requests
// racing to insert the same lease_rent_changes effective_date).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ParseFrequency validates s as a Frequency.
func ParseFrequency(s string) (Frequency, error) {
	switch Frequency(s) {
	case Weekly, Fortnightly, Monthly:
		return Frequency(s), nil
	default:
		return "", fmt.Errorf("%w: frequency must be weekly, fortnightly or monthly", core.ErrInvalidInput)
	}
}

// --- properties ---

const propertyColumns = `id, org_id, name, coalesce(address,''), coalesce(kind,''), residential`

func scanProperty(row pgx.Row) (Property, error) {
	var p Property
	err := row.Scan(&p.ID, &p.OrgID, &p.Name, &p.Address, &p.Kind, &p.Residential)
	return p, err
}

// CreateProperty inserts a new property for orgID and returns it.
func CreateProperty(ctx context.Context, q core.Querier, orgID int64, p Property) (Property, error) {
	if p.Name == "" {
		return Property{}, fmt.Errorf("%w: name is required", core.ErrInvalidInput)
	}
	row := q.QueryRow(ctx, `
		INSERT INTO properties (org_id, name, address, kind, residential)
		VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), $5)
		RETURNING `+propertyColumns,
		orgID, p.Name, p.Address, p.Kind, p.Residential,
	)
	return scanProperty(row)
}

// GetProperty looks up a property by id, scoped to orgID. It returns
// core.ErrNotFound if no such property exists in that org.
func GetProperty(ctx context.Context, q core.Querier, orgID, id int64) (Property, error) {
	row := q.QueryRow(ctx, `SELECT `+propertyColumns+` FROM properties WHERE org_id = $1 AND id = $2`, orgID, id)
	p, err := scanProperty(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Property{}, core.ErrNotFound
	}
	return p, err
}

// ListProperties lists an org's properties.
func ListProperties(ctx context.Context, q core.Querier, orgID int64) ([]Property, error) {
	rows, err := q.Query(ctx, `SELECT `+propertyColumns+` FROM properties WHERE org_id = $1 ORDER BY id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	properties := []Property{}
	for rows.Next() {
		p, err := scanProperty(rows)
		if err != nil {
			return nil, err
		}
		properties = append(properties, p)
	}
	return properties, rows.Err()
}

// --- leases ---

// NewLeaseTenant is one tenant to attach when creating a lease.
type NewLeaseTenant struct {
	ContactID int64
	Primary   bool
}

// NewLease describes a lease to create: the lease itself, its tenants and
// the initial rent amount (stored as the first lease_rent_changes row,
// dated StartDate).
type NewLease struct {
	PropertyID     int64
	Tenants        []NewLeaseTenant
	StartDate      time.Time
	EndDate        *time.Time
	Frequency      Frequency
	RentAmount     core.Cents
	PaidToDate     *time.Time
	OpeningArrears core.Cents
	BondAmount     core.Cents
}

// CreateLease validates in (exactly one primary tenant, every tenant a
// tenant-kind contact in orgID, a positive rent amount), then inserts the
// lease, its tenants and the initial rent change (dated StartDate). If
// OpeningArrears is positive it also creates a draft "Opening arrears" rent
// invoice to the primary tenant, due on StartDate, and links it as
// opening_arrears_invoice_id; it is issued like any other draft. It does
// not start its own transaction: pass a tx if the caller needs the inserts
// to roll back together.
func CreateLease(ctx context.Context, q core.Querier, orgID int64, in NewLease) (Lease, error) {
	if len(in.Tenants) == 0 {
		return Lease{}, fmt.Errorf("%w: a lease needs at least one tenant", core.ErrInvalidInput)
	}
	primaries := 0
	seenContacts := make(map[int64]bool, len(in.Tenants))
	for _, t := range in.Tenants {
		if t.Primary {
			primaries++
		}
		if seenContacts[t.ContactID] {
			return Lease{}, fmt.Errorf("%w: contact_id %d listed more than once in tenants", core.ErrInvalidInput, t.ContactID)
		}
		seenContacts[t.ContactID] = true
	}
	if primaries != 1 {
		return Lease{}, fmt.Errorf("%w: a lease needs exactly one primary tenant", core.ErrInvalidInput)
	}
	if in.RentAmount <= 0 {
		return Lease{}, fmt.Errorf("%w: rent_amount must be positive", core.ErrInvalidInput)
	}
	if in.EndDate != nil && in.EndDate.Before(in.StartDate) {
		return Lease{}, fmt.Errorf("%w: end_date must not be before start_date", core.ErrInvalidInput)
	}
	if in.OpeningArrears < 0 {
		return Lease{}, fmt.Errorf("%w: opening_arrears must not be negative", core.ErrInvalidInput)
	}
	if in.BondAmount < 0 {
		return Lease{}, fmt.Errorf("%w: bond_amount must not be negative", core.ErrInvalidInput)
	}
	if in.PaidToDate != nil && !validPeriodEnd(in.StartDate, in.Frequency, *in.PaidToDate) {
		return Lease{}, fmt.Errorf("%w: paid_to_date must be the last day of a rent period", core.ErrInvalidInput)
	}
	prop, err := GetProperty(ctx, q, orgID, in.PropertyID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return Lease{}, fmt.Errorf("%w: property_id not found", core.ErrInvalidInput)
		}
		return Lease{}, err
	}
	for _, t := range in.Tenants {
		contact, err := core.GetContact(ctx, q, orgID, t.ContactID)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				return Lease{}, fmt.Errorf("%w: contact_id %d not found", core.ErrInvalidInput, t.ContactID)
			}
			return Lease{}, err
		}
		if contact.Kind != "tenant" {
			return Lease{}, fmt.Errorf("%w: contact_id %d is not a tenant", core.ErrInvalidInput, t.ContactID)
		}
	}

	var leaseID int64
	err = q.QueryRow(ctx, `
		INSERT INTO leases (org_id, property_id, start_date, end_date, frequency, paid_to_date, opening_arrears, bond_amount)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		orgID, in.PropertyID, in.StartDate, in.EndDate, string(in.Frequency), in.PaidToDate,
		int64(in.OpeningArrears), int64(in.BondAmount),
	).Scan(&leaseID)
	if err != nil {
		return Lease{}, err
	}

	for _, t := range in.Tenants {
		if _, err := q.Exec(ctx,
			`INSERT INTO lease_tenants (lease_id, contact_id, is_primary) VALUES ($1, $2, $3)`,
			leaseID, t.ContactID, t.Primary,
		); err != nil {
			return Lease{}, err
		}
	}

	if _, err := q.Exec(ctx,
		`INSERT INTO lease_rent_changes (lease_id, effective_date, amount) VALUES ($1, $2, $3)`,
		leaseID, in.StartDate, int64(in.RentAmount),
	); err != nil {
		return Lease{}, err
	}

	if in.OpeningArrears > 0 {
		if err := createOpeningArrearsInvoice(ctx, q, orgID, leaseID, prop, in); err != nil {
			return Lease{}, err
		}
	}

	return GetLease(ctx, q, orgID, leaseID)
}

// createOpeningArrearsInvoice creates in's opening-arrears draft invoice
// for leaseID (GST code per the rent rule for prop) and stamps it on the
// lease.
func createOpeningArrearsInvoice(ctx context.Context, q core.Querier, orgID, leaseID int64, prop Property, in NewLease) error {
	settings, err := core.GetSettings(ctx, q, orgID)
	if err != nil {
		return err
	}
	var primaryContactID int64
	for _, t := range in.Tenants {
		if t.Primary {
			primaryContactID = t.ContactID
		}
	}
	dueOn := in.StartDate
	inv, err := core.CreateDraftInvoice(ctx, q, orgID, core.NewInvoice{
		ContactID: primaryContactID,
		Source:    "rent",
		DueOn:     &dueOn,
		Lines: []core.NewLine{{
			Description:   "Opening arrears",
			QuantityMilli: 1000,
			UnitAmount:    in.OpeningArrears,
			GSTCode:       string(gstCodeForRent(prop.Residential, settings.GSTRegistered)),
			PropertyID:    &prop.ID,
		}},
	})
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `UPDATE leases SET opening_arrears_invoice_id = $1 WHERE org_id = $2 AND id = $3`, inv.ID, orgID, leaseID)
	return err
}

// GetLease looks up a lease and its tenants, rent changes and charges by id,
// scoped to orgID. It returns core.ErrNotFound if no such lease exists in
// that org.
func GetLease(ctx context.Context, q core.Querier, orgID, id int64) (Lease, error) {
	var l Lease
	var frequency string
	err := q.QueryRow(ctx, `
		SELECT id, org_id, property_id, start_date, end_date, termination_date, frequency,
			paid_to_date, opening_arrears, opening_arrears_invoice_id, bond_amount, bond_invoice_id,
			coalesce(rta_lodgement_ref,''), bond_lodged_on
		FROM leases WHERE org_id = $1 AND id = $2`, orgID, id,
	).Scan(&l.ID, &l.OrgID, &l.PropertyID, &l.StartDate, &l.EndDate, &l.TerminationDate, &frequency,
		&l.PaidToDate, &l.OpeningArrears, &l.OpeningArrearsInvoiceID, &l.BondAmount, &l.BondInvoiceID,
		&l.RTALodgementRef, &l.BondLodgedOn)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, core.ErrNotFound
	}
	if err != nil {
		return Lease{}, err
	}
	l.Frequency = Frequency(frequency)

	tenantRows, err := q.Query(ctx, `SELECT contact_id, is_primary FROM lease_tenants WHERE lease_id = $1 ORDER BY contact_id`, id)
	if err != nil {
		return Lease{}, err
	}
	defer tenantRows.Close()
	l.Tenants = []LeaseTenant{}
	for tenantRows.Next() {
		var lt LeaseTenant
		if err := tenantRows.Scan(&lt.ContactID, &lt.IsPrimary); err != nil {
			return Lease{}, err
		}
		l.Tenants = append(l.Tenants, lt)
	}
	if err := tenantRows.Err(); err != nil {
		return Lease{}, err
	}

	changes, err := listRentChanges(ctx, q, id)
	if err != nil {
		return Lease{}, err
	}
	l.RentChanges = changes

	charges, err := listCharges(ctx, q, id)
	if err != nil {
		return Lease{}, err
	}
	l.Charges = charges

	return l, nil
}

// ListLeasesByProperty lists a property's leases, scoped to orgID.
func ListLeasesByProperty(ctx context.Context, q core.Querier, orgID, propertyID int64) ([]Lease, error) {
	rows, err := q.Query(ctx, `SELECT id FROM leases WHERE org_id = $1 AND property_id = $2 ORDER BY id`, orgID, propertyID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	leases := make([]Lease, 0, len(ids))
	for _, id := range ids {
		l, err := GetLease(ctx, q, orgID, id)
		if err != nil {
			return nil, err
		}
		leases = append(leases, l)
	}
	return leases, nil
}

// listLeaseIDs lists the ids of every lease in orgID, for the dashboard's
// across-every-lease totals (GetLedger and BondInfo take a lease id, not a
// property, so there's no single aggregate query for them - see
// dashboardSection's own note).
func listLeaseIDs(ctx context.Context, q core.Querier, orgID int64) ([]int64, error) {
	rows, err := q.Query(ctx, `SELECT id FROM leases WHERE org_id = $1 ORDER BY id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// --- rent changes ---

func listRentChanges(ctx context.Context, q core.Querier, leaseID int64) ([]LeaseRentChange, error) {
	rows, err := q.Query(ctx, `SELECT id, effective_date, amount FROM lease_rent_changes WHERE lease_id = $1 ORDER BY effective_date`, leaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	changes := []LeaseRentChange{}
	for rows.Next() {
		var c LeaseRentChange
		if err := rows.Scan(&c.ID, &c.EffectiveDate, &c.Amount); err != nil {
			return nil, err
		}
		changes = append(changes, c)
	}
	return changes, rows.Err()
}

// warnMoreThanOneIncreaseWithin12Months is the warning message returned by
// CreateRentChange when it applies.
const warnMoreThanOneIncreaseWithin12Months = "more than one rent increase within 12 months"

// rentChangeWarnings decides whether adding a change of newAmount effective
// newEff produces the 12-month warning: it fires when newAmount is an
// increase over the rate effective immediately before newEff, and the most
// recent prior increase (excluding the initial row dated startDate) falls
// within 365 days before newEff. existing must be sorted ascending by
// EffectiveDate.
func rentChangeWarnings(existing []LeaseRentChange, startDate, newEff time.Time, newAmount core.Cents) []string {
	var currentRate core.Cents
	var prevRate core.Cents
	var lastIncrease *time.Time
	first := true
	for _, c := range existing {
		if c.EffectiveDate.After(newEff) {
			continue
		}
		if !first && c.Amount > prevRate && !c.EffectiveDate.Equal(startDate) {
			t := c.EffectiveDate
			lastIncrease = &t
		}
		prevRate = c.Amount
		currentRate = c.Amount
		first = false
	}

	// newEff.Before(lastIncrease + 1 year) rather than a 365-day cutoff: an
	// exact one-year anniversary is not "within 12 months" (it's the
	// boundary itself), so it must not warn.
	if newAmount > currentRate && lastIncrease != nil && newEff.Before(lastIncrease.AddDate(1, 0, 0)) {
		return []string{warnMoreThanOneIncreaseWithin12Months}
	}
	return nil
}

// CreateRentChange validates effective/amount, inserts the change, and
// returns the inserted row plus any warnings (currently just the 12-month
// multiple-increase warning). It does not start its own transaction.
func CreateRentChange(ctx context.Context, q core.Querier, orgID, leaseID int64, effective time.Time, amount core.Cents) (LeaseRentChange, []string, error) {
	if amount <= 0 {
		return LeaseRentChange{}, nil, fmt.Errorf("%w: amount must be positive", core.ErrInvalidInput)
	}
	lease, err := GetLease(ctx, q, orgID, leaseID)
	if err != nil {
		return LeaseRentChange{}, nil, err
	}
	for _, c := range lease.RentChanges {
		if c.EffectiveDate.Equal(effective) {
			return LeaseRentChange{}, nil, fmt.Errorf("%w: a rent change already exists for effective_date %s", core.ErrInvalidInput, effective.Format(dateLayout))
		}
	}

	warnings := rentChangeWarnings(lease.RentChanges, lease.StartDate, effective, amount)

	var created LeaseRentChange
	err = q.QueryRow(ctx, `
		INSERT INTO lease_rent_changes (lease_id, effective_date, amount)
		VALUES ($1, $2, $3)
		RETURNING id, effective_date, amount`,
		leaseID, effective, int64(amount),
	).Scan(&created.ID, &created.EffectiveDate, &created.Amount)
	if err != nil {
		// A concurrent request may have inserted the same effective_date
		// between the check above and this insert; map that race the same
		// way as the pre-check, rather than surfacing a raw DB error.
		if isUniqueViolation(err) {
			return LeaseRentChange{}, nil, fmt.Errorf("%w: a rent change already exists for effective_date %s", core.ErrInvalidInput, effective.Format(dateLayout))
		}
		return LeaseRentChange{}, nil, err
	}
	return created, warnings, nil
}

// --- charges ---

func listCharges(ctx context.Context, q core.Querier, leaseID int64) ([]LeaseCharge, error) {
	rows, err := q.Query(ctx, `SELECT id, description, amount, start_date, end_date FROM lease_charges WHERE lease_id = $1 ORDER BY id`, leaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	charges := []LeaseCharge{}
	for rows.Next() {
		var c LeaseCharge
		if err := rows.Scan(&c.ID, &c.Description, &c.Amount, &c.StartDate, &c.EndDate); err != nil {
			return nil, err
		}
		charges = append(charges, c)
	}
	return charges, rows.Err()
}

// NewCharge describes a lease charge to create.
type NewCharge struct {
	Description string
	Amount      core.Cents
	StartDate   time.Time
	EndDate     *time.Time
}

// CreateCharge validates in (a lease exists in orgID, a non-empty
// description and a positive amount) and inserts the charge. It does not
// start its own transaction.
func CreateCharge(ctx context.Context, q core.Querier, orgID, leaseID int64, in NewCharge) (LeaseCharge, error) {
	if in.Description == "" {
		return LeaseCharge{}, fmt.Errorf("%w: description is required", core.ErrInvalidInput)
	}
	if in.Amount <= 0 {
		return LeaseCharge{}, fmt.Errorf("%w: amount must be positive", core.ErrInvalidInput)
	}
	if in.EndDate != nil && in.EndDate.Before(in.StartDate) {
		return LeaseCharge{}, fmt.Errorf("%w: end_date must not be before start_date", core.ErrInvalidInput)
	}
	if _, err := GetLease(ctx, q, orgID, leaseID); err != nil {
		return LeaseCharge{}, err
	}

	var created LeaseCharge
	err := q.QueryRow(ctx, `
		INSERT INTO lease_charges (lease_id, description, amount, start_date, end_date)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, description, amount, start_date, end_date`,
		leaseID, in.Description, int64(in.Amount), in.StartDate, in.EndDate,
	).Scan(&created.ID, &created.Description, &created.Amount, &created.StartDate, &created.EndDate)
	return created, err
}

// --- termination ---

// TerminateLease sets a lease's termination_date, scoped to orgID. It
// returns core.ErrNotFound if no such lease exists in that org.
func TerminateLease(ctx context.Context, q core.Querier, orgID, leaseID int64, terminationDate time.Time) (Lease, error) {
	lease, err := GetLease(ctx, q, orgID, leaseID)
	if err != nil {
		return Lease{}, err
	}
	if terminationDate.Before(lease.StartDate) {
		return Lease{}, fmt.Errorf("%w: termination_date must not be before start_date", core.ErrInvalidInput)
	}

	if _, err := q.Exec(ctx, `UPDATE leases SET termination_date = $1 WHERE org_id = $2 AND id = $3`, terminationDate, orgID, leaseID); err != nil {
		return Lease{}, err
	}
	return GetLease(ctx, q, orgID, leaseID)
}

// warnFixedTermEnded is the lease warning leaseWarnings returns once a
// lease's fixed term has ended without a termination.
const warnFixedTermEnded = "fixed term ended; tenancy continues periodic"

// leaseWarnings returns l's tenancy warnings as of today: currently just
// warnFixedTermEnded, once end_date has passed and no termination_date is
// recorded.
func leaseWarnings(l Lease, today time.Time) []string {
	if l.EndDate != nil && l.EndDate.Before(today) && l.TerminationDate == nil {
		return []string{warnFixedTermEnded}
	}
	return nil
}

// ToTerms builds the pure LeaseTerms periods.go computes against from a
// loaded Lease: End is TerminationDate only - EndDate ends the fixed term,
// but the tenancy continues periodic until terminated (see
// leaseWarnings). Changes and Charges are converted and Changes stay
// sorted ascending (GetLease already orders them by effective_date).
func (l Lease) ToTerms() LeaseTerms {

	changes := make([]RentChange, len(l.RentChanges))
	for i, c := range l.RentChanges {
		changes[i] = RentChange{Effective: c.EffectiveDate, Amount: c.Amount}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Effective.Before(changes[j].Effective) })

	charges := make([]Charge, len(l.Charges))
	for i, c := range l.Charges {
		charges[i] = Charge{Description: c.Description, Amount: c.Amount, Start: c.StartDate, End: c.EndDate}
	}

	return LeaseTerms{
		Start:     l.StartDate,
		End:       l.TerminationDate,
		Frequency: l.Frequency,
		PaidTo:    l.PaidToDate,
		Changes:   changes,
		Charges:   charges,
	}
}
