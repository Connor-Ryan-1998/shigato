package property

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/core"
)

// InsurancePolicy is one insurance policy on a property. A property keeps
// its policy history; the one with the latest renewal date is current.
type InsurancePolicy struct {
	ID           int64
	PropertyID   int64
	Insurer      string
	Type         string // building, landlord, strata_contents or other
	PolicyNumber string
	StartDate    time.Time
	RenewalDate  time.Time
	SumInsured   *core.Cents
	Premium      *core.Cents
	Excess       *core.Cents
	FileID       *int64
	// FileName is the attached document's original name ("" when none).
	FileName string
}

var insuranceTypes = map[string]bool{"building": true, "landlord": true, "strata_contents": true, "other": true}

const insuranceColumns = `ip.id, ip.property_id, ip.insurer, ip.type, ip.policy_number, ip.start_date, ip.renewal_date,
	ip.sum_insured, ip.premium, ip.excess, ip.file_id, coalesce(f.original_name, '')`

const insuranceFrom = `insurance_policies ip LEFT JOIN files f ON f.id = ip.file_id`

func scanInsurance(row pgx.Row) (InsurancePolicy, error) {
	var p InsurancePolicy
	var sum, premium, excess *int64
	err := row.Scan(&p.ID, &p.PropertyID, &p.Insurer, &p.Type, &p.PolicyNumber, &p.StartDate, &p.RenewalDate,
		&sum, &premium, &excess, &p.FileID, &p.FileName)
	p.SumInsured, p.Premium, p.Excess = centsPtr(sum), centsPtr(premium), centsPtr(excess)
	return p, err
}

// requireProperty returns core.ErrNotFound unless propertyID is orgID's.
func requireProperty(ctx context.Context, q core.Querier, orgID, propertyID int64) error {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM properties WHERE org_id = $1 AND id = $2)`, orgID, propertyID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return core.ErrNotFound
	}
	return nil
}

// requireOrgFile returns core.ErrInvalidInput unless fileID is orgID's.
func requireOrgFile(ctx context.Context, q core.Querier, orgID, fileID int64) error {
	if _, err := core.GetFile(ctx, q, orgID, fileID); err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("%w: file %d not found", core.ErrInvalidInput, fileID)
		}
		return err
	}
	return nil
}

func validateInsurance(ctx context.Context, q core.Querier, orgID int64, p InsurancePolicy) error {
	if p.Insurer == "" {
		return fmt.Errorf("%w: insurer is required", core.ErrInvalidInput)
	}
	if !insuranceTypes[p.Type] {
		return fmt.Errorf("%w: type must be building, landlord, strata_contents or other", core.ErrInvalidInput)
	}
	if p.StartDate.IsZero() || p.RenewalDate.IsZero() {
		return fmt.Errorf("%w: start_date and renewal_date are required", core.ErrInvalidInput)
	}
	if p.RenewalDate.Before(p.StartDate) {
		return fmt.Errorf("%w: renewal_date must not be before start_date", core.ErrInvalidInput)
	}
	for name, c := range map[string]*core.Cents{"sum_insured": p.SumInsured, "premium": p.Premium, "excess": p.Excess} {
		if c != nil && *c < 0 {
			return fmt.Errorf("%w: %s must not be negative", core.ErrInvalidInput, name)
		}
	}
	if p.FileID != nil {
		return requireOrgFile(ctx, q, orgID, *p.FileID)
	}
	return nil
}

func getInsurancePolicy(ctx context.Context, q core.Querier, orgID, propertyID, id int64) (InsurancePolicy, error) {
	row := q.QueryRow(ctx, `SELECT `+insuranceColumns+` FROM `+insuranceFrom+`
		WHERE ip.org_id = $1 AND ip.property_id = $2 AND ip.id = $3`, orgID, propertyID, id)
	p, err := scanInsurance(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return InsurancePolicy{}, core.ErrNotFound
	}
	return p, err
}

// CreateInsurancePolicy adds p to its property, scoped to orgID.
func CreateInsurancePolicy(ctx context.Context, q core.Querier, orgID int64, p InsurancePolicy) (InsurancePolicy, error) {
	if err := requireProperty(ctx, q, orgID, p.PropertyID); err != nil {
		return InsurancePolicy{}, err
	}
	if err := validateInsurance(ctx, q, orgID, p); err != nil {
		return InsurancePolicy{}, err
	}
	var id int64
	err := q.QueryRow(ctx, `
		INSERT INTO insurance_policies (org_id, property_id, insurer, type, policy_number, start_date, renewal_date,
			sum_insured, premium, excess, file_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
		orgID, p.PropertyID, p.Insurer, p.Type, p.PolicyNumber, p.StartDate, p.RenewalDate,
		int64Ptr(p.SumInsured), int64Ptr(p.Premium), int64Ptr(p.Excess), p.FileID,
	).Scan(&id)
	if err != nil {
		return InsurancePolicy{}, err
	}
	return getInsurancePolicy(ctx, q, orgID, p.PropertyID, id)
}

// UpdateInsurancePolicy replaces policy p.ID on p.PropertyID, scoped to
// orgID.
func UpdateInsurancePolicy(ctx context.Context, q core.Querier, orgID int64, p InsurancePolicy) (InsurancePolicy, error) {
	if _, err := getInsurancePolicy(ctx, q, orgID, p.PropertyID, p.ID); err != nil {
		return InsurancePolicy{}, err
	}
	if err := validateInsurance(ctx, q, orgID, p); err != nil {
		return InsurancePolicy{}, err
	}
	if _, err := q.Exec(ctx, `
		UPDATE insurance_policies SET insurer = $4, type = $5, policy_number = $6, start_date = $7, renewal_date = $8,
			sum_insured = $9, premium = $10, excess = $11, file_id = $12
		WHERE org_id = $1 AND property_id = $2 AND id = $3`,
		orgID, p.PropertyID, p.ID, p.Insurer, p.Type, p.PolicyNumber, p.StartDate, p.RenewalDate,
		int64Ptr(p.SumInsured), int64Ptr(p.Premium), int64Ptr(p.Excess), p.FileID,
	); err != nil {
		return InsurancePolicy{}, err
	}
	return getInsurancePolicy(ctx, q, orgID, p.PropertyID, p.ID)
}

// DeleteInsurancePolicy removes policy id from propertyID, scoped to orgID.
func DeleteInsurancePolicy(ctx context.Context, q core.Querier, orgID, propertyID, id int64) error {
	tag, err := q.Exec(ctx, `DELETE FROM insurance_policies WHERE org_id = $1 AND property_id = $2 AND id = $3`, orgID, propertyID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return core.ErrNotFound
	}
	return nil
}

// ListInsurancePolicies lists propertyID's policies, latest renewal first.
// It returns core.ErrNotFound if the property is not orgID's.
func ListInsurancePolicies(ctx context.Context, q core.Querier, orgID, propertyID int64) ([]InsurancePolicy, error) {
	if err := requireProperty(ctx, q, orgID, propertyID); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT `+insuranceColumns+` FROM `+insuranceFrom+`
		WHERE ip.org_id = $1 AND ip.property_id = $2 ORDER BY ip.renewal_date DESC, ip.id DESC`, orgID, propertyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InsurancePolicy{}
	for rows.Next() {
		p, err := scanInsurance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// renewalWindowDays is how far ahead a current policy's renewal counts as
// "renewing soon".
const renewalWindowDays = 30

// InsuranceRenewingSoon counts orgID's properties whose current policy
// (latest renewal date) renews within [today, today+30 days].
func InsuranceRenewingSoon(ctx context.Context, q core.Querier, orgID int64, today time.Time) (int, error) {
	var n int
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT DISTINCT ON (property_id) renewal_date
			FROM insurance_policies WHERE org_id = $1
			ORDER BY property_id, renewal_date DESC, id DESC
		) current
		WHERE renewal_date BETWEEN $2 AND $3`,
		orgID, today, today.AddDate(0, 0, renewalWindowDays),
	).Scan(&n)
	return n, err
}
