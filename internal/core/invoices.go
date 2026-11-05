package core

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrInvalidState is returned when a write is attempted against an invoice
// in a status that doesn't allow it (e.g. issuing a non-draft, or paying a
// draft or void invoice).
var ErrInvalidState = errors.New("invalid state")

// ErrInvalidInput is returned when caller-supplied data fails validation
// (e.g. a bad GST code, a non-positive quantity, or an invoice with no
// lines).
var ErrInvalidInput = errors.New("invalid input")

// invoiceHasPaymentsError is VoidInvoice's error for an invoice that has at
// least one recorded payment. It unwraps to ErrInvalidState (so
// errors.Is(err, ErrInvalidState) and the 409 mapping both still work) but
// carries its own exact message rather than ErrInvalidState's generic one,
// since credit notes and refunds are out of scope for this plan: voiding a
// paid invoice outright would make the money disappear from the books.
type invoiceHasPaymentsError struct{}

func (invoiceHasPaymentsError) Error() string {
	return "invoice has payments; record a credit note instead"
}

func (invoiceHasPaymentsError) Unwrap() error { return ErrInvalidState }

// ErrInvoiceHasPayments is returned by VoidInvoice when the invoice being
// voided has at least one recorded payment.
var ErrInvoiceHasPayments error = invoiceHasPaymentsError{}

// NewLine is one line of a draft invoice being created.
type NewLine struct {
	Description   string
	QuantityMilli int64
	UnitAmount    Cents
	GSTCode       string
	// PropertyID attributes the line to a property (e.g. a rent or
	// lease-charge line). Nil for lines with no property (most manual
	// invoices).
	PropertyID *int64
	// ProjectID attributes the line to a timesheets project (a billing-run
	// line). Nil for lines with no project.
	ProjectID *int64
}

// NewInvoice describes a draft invoice to create.
type NewInvoice struct {
	ContactID int64
	Source    string
	DueInDays int
	// DueOn, if set, pins the invoice's due date regardless of when it's
	// issued (e.g. rent is due on the period's start date, not N days
	// after whenever the draft happens to be issued). IssueInvoice uses
	// max(issue_date, DueOn) so a late issue never backdates the due
	// date. DueInDays is still stored and still drives DueDate when DueOn
	// is nil, keeping the Stripe integration's existing term.
	DueOn *time.Time
	Lines []NewLine
}

// InvoiceLine is a persisted invoice line, with its computed amount and GST.
type InvoiceLine struct {
	ID            int64
	Description   string
	QuantityMilli int64
	UnitAmount    Cents
	GSTCode       GSTCode
	GSTAmount     Cents
	Amount        Cents
}

// Invoice is a persisted invoice and its lines.
type Invoice struct {
	ID               int64
	OrgID            int64
	ContactID        int64
	Number           *int
	Source           string
	Status           string
	IssueDate        *time.Time
	DueDate          *time.Time
	DueInDays        int
	DueOn            *time.Time
	Currency         string
	Subtotal         Cents
	GSTTotal         Cents
	Total            Cents
	PaymentReference string
	ProviderRef      string
	ProviderURL      string
	Lines            []InvoiceLine
}

const invoiceColumns = `id, org_id, contact_id, number, source, status, issue_date, due_date,
	due_in_days, due_on, currency, subtotal, gst_total, total, payment_reference,
	coalesce(provider_ref,''), coalesce(provider_url,'')`

func scanInvoice(row pgx.Row) (Invoice, error) {
	var inv Invoice
	var number *int32
	var subtotal, gstTotal, total int64
	err := row.Scan(&inv.ID, &inv.OrgID, &inv.ContactID, &number, &inv.Source, &inv.Status,
		&inv.IssueDate, &inv.DueDate, &inv.DueInDays, &inv.DueOn, &inv.Currency, &subtotal, &gstTotal, &total,
		&inv.PaymentReference, &inv.ProviderRef, &inv.ProviderURL)
	if err != nil {
		return Invoice{}, err
	}
	if number != nil {
		n := int(*number)
		inv.Number = &n
	}
	inv.Subtotal = Cents(subtotal)
	inv.GSTTotal = Cents(gstTotal)
	inv.Total = Cents(total)
	return inv, nil
}

// GetInvoice looks up an invoice and its lines by id, scoped to orgID. It
// returns ErrNotFound if no such invoice exists in that org.
func GetInvoice(ctx context.Context, q Querier, orgID, id int64) (Invoice, error) {
	row := q.QueryRow(ctx, `SELECT `+invoiceColumns+` FROM invoices WHERE org_id = $1 AND id = $2`, orgID, id)
	inv, err := scanInvoice(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invoice{}, ErrNotFound
	}
	if err != nil {
		return Invoice{}, err
	}

	lines, err := invoiceLines(ctx, q, orgID, id)
	if err != nil {
		return Invoice{}, err
	}
	inv.Lines = lines
	return inv, nil
}

// ListInvoices lists an org's invoices, optionally filtered by status. An
// empty status returns every invoice. Lines are not loaded (use GetInvoice
// for a single invoice's lines).
func ListInvoices(ctx context.Context, q Querier, orgID int64, status string) ([]Invoice, error) {
	var rows pgx.Rows
	var err error
	if status == "" {
		rows, err = q.Query(ctx, `SELECT `+invoiceColumns+` FROM invoices WHERE org_id = $1 ORDER BY id`, orgID)
	} else {
		rows, err = q.Query(ctx, `SELECT `+invoiceColumns+` FROM invoices WHERE org_id = $1 AND status = $2 ORDER BY id`, orgID, status)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	invoices := []Invoice{}
	for rows.Next() {
		inv, err := scanInvoice(rows)
		if err != nil {
			return nil, err
		}
		invoices = append(invoices, inv)
	}
	return invoices, rows.Err()
}

func invoiceLines(ctx context.Context, q Querier, orgID, invoiceID int64) ([]InvoiceLine, error) {
	rows, err := q.Query(ctx, `
		SELECT id, description, quantity_milli, unit_amount, gst_code, gst_amount, amount
		FROM invoice_lines WHERE org_id = $1 AND invoice_id = $2 ORDER BY id`,
		orgID, invoiceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	lines := []InvoiceLine{}
	for rows.Next() {
		var l InvoiceLine
		var unitAmount, gstAmount, amount int64
		var code string
		if err := rows.Scan(&l.ID, &l.Description, &l.QuantityMilli, &unitAmount, &code, &gstAmount, &amount); err != nil {
			return nil, err
		}
		l.UnitAmount = Cents(unitAmount)
		l.GSTCode = GSTCode(code)
		l.GSTAmount = Cents(gstAmount)
		l.Amount = Cents(amount)
		lines = append(lines, l)
	}
	return lines, rows.Err()
}

// CreateDraftInvoice validates in's lines, computes totals and a unique
// payment reference, and inserts a new draft invoice (status 'draft', no
// number). It does not start its own transaction: pass a tx if the caller
// needs the insert to roll back together with other work.
func CreateDraftInvoice(ctx context.Context, q Querier, orgID int64, in NewInvoice) (Invoice, error) {
	if len(in.Lines) == 0 {
		return Invoice{}, fmt.Errorf("%w: invoice must have at least one line", ErrInvalidInput)
	}
	if in.DueInDays < 0 {
		return Invoice{}, fmt.Errorf("%w: due_in_days must not be negative", ErrInvalidInput)
	}

	if _, err := GetContact(ctx, q, orgID, in.ContactID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Invoice{}, fmt.Errorf("%w: contact_id not found", ErrInvalidInput)
		}
		return Invoice{}, err
	}

	settings, err := GetSettings(ctx, q, orgID)
	if err != nil {
		return Invoice{}, err
	}

	codes := make([]GSTCode, len(in.Lines))
	lines := make([]Line, len(in.Lines))
	for i, l := range in.Lines {
		// Bounds keep LineAmount's quantityMilli*int64(unit) multiplication
		// (at most 10,000,000 * 100,000,000,000 = 1e18) within int64 range
		// (max ~9.2e18), so LineAmount can never overflow.
		if l.QuantityMilli < 1 || l.QuantityMilli > 10_000_000 {
			return Invoice{}, fmt.Errorf("%w: line %d: quantity_milli must be from 1 to 10,000,000", ErrInvalidInput, i)
		}
		if l.UnitAmount < 0 || l.UnitAmount > 100_000_000_000 {
			return Invoice{}, fmt.Errorf("%w: line %d: unit_amount must be from 0 to 100,000,000,000", ErrInvalidInput, i)
		}
		code, err := ParseGSTCode(l.GSTCode)
		if err != nil {
			return Invoice{}, fmt.Errorf("%w: line %d: %v", ErrInvalidInput, i, err)
		}
		if code == GST && !settings.GSTRegistered {
			return Invoice{}, fmt.Errorf("%w: line %d: org is not GST-registered", ErrInvalidInput, i)
		}
		codes[i] = code
		lines[i] = Line{QuantityMilli: l.QuantityMilli, Unit: l.UnitAmount, Code: code}
	}
	subtotal, gst, total := Totals(lines)

	// ponytail: a single attempt at a random 40-bit reference, not a
	// collision-retry loop. A collision inside the caller's transaction
	// would abort it (Postgres requires a savepoint to recover from a
	// failed statement), and Querier doesn't expose Begin for one. At
	// 1-in-2^40 odds this is the right tradeoff; add a savepoint-based
	// retry (needs a pgx.Tx, not just Querier) if collisions are ever
	// observed.
	ref, err := generatePaymentReference()
	if err != nil {
		return Invoice{}, err
	}
	var invoiceID int64
	err = q.QueryRow(ctx, `
		INSERT INTO invoices (org_id, contact_id, source, status, currency, subtotal, gst_total, total, payment_reference, due_in_days, due_on)
		VALUES ($1, $2, $3, 'draft', 'AUD', $4, $5, $6, $7, $8, $9)
		RETURNING id`,
		orgID, in.ContactID, in.Source, int64(subtotal), int64(gst), int64(total), ref, in.DueInDays, in.DueOn,
	).Scan(&invoiceID)
	if err != nil {
		return Invoice{}, err
	}

	for i, l := range in.Lines {
		amount := LineAmount(l.QuantityMilli, l.UnitAmount)
		gstAmount := GSTOnExclusive(amount, codes[i])
		_, err := q.Exec(ctx, `
			INSERT INTO invoice_lines (invoice_id, org_id, description, quantity_milli, unit_amount, gst_code, gst_amount, amount, property_id, project_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			invoiceID, orgID, l.Description, l.QuantityMilli, int64(l.UnitAmount), string(codes[i]), int64(gstAmount), int64(amount), l.PropertyID, l.ProjectID,
		)
		if err != nil {
			return Invoice{}, err
		}
	}

	return GetInvoice(ctx, q, orgID, invoiceID)
}

// IssueInvoice assigns the next gap-free invoice number for orgID (locking
// invoice_counters for the duration of the caller's transaction) and sets
// issue_date, due_date (today + the draft's due_in_days) and status 'open'.
// Only a 'draft' invoice can be issued; any other status returns
// ErrInvalidState, and a zero total returns ErrInvalidInput. Run this in the caller's transaction so a rollback also
// releases the number, keeping numbering gap-free.
func IssueInvoice(ctx context.Context, tx Querier, orgID, id int64, today time.Time) (Invoice, error) {
	var status string
	var dueInDays int
	var total int64
	var dueOn *time.Time
	err := tx.QueryRow(ctx,
		`SELECT status, due_in_days, due_on, total FROM invoices WHERE org_id = $1 AND id = $2 FOR UPDATE`,
		orgID, id,
	).Scan(&status, &dueInDays, &dueOn, &total)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invoice{}, ErrNotFound
	}
	if err != nil {
		return Invoice{}, err
	}
	if status != "draft" {
		return Invoice{}, ErrInvalidState
	}
	if total <= 0 {
		return Invoice{}, fmt.Errorf("%w: cannot issue an invoice with a zero total", ErrInvalidInput)
	}

	var number int
	err = tx.QueryRow(ctx, `
		INSERT INTO invoice_counters (org_id, last_number) VALUES ($1, 1)
		ON CONFLICT (org_id) DO UPDATE SET last_number = invoice_counters.last_number + 1
		RETURNING last_number`,
		orgID,
	).Scan(&number)
	if err != nil {
		return Invoice{}, err
	}

	// today is expected to already be in the operator's timezone (the
	// handler passes time.Now().In(cfg.Location)): take its calendar
	// date, not a UTC day truncation, which would shift the date for
	// callers east of UTC during their morning.
	y, m, d := today.Date()
	issueDate := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)

	// A pinned due_on (e.g. rent's period start) wins over the
	// days-after-issue term, but never predates the issue date itself -
	// an invoice issued after its "due on" date (a late manual retry of
	// a failed auto-issue) is due immediately, not in the past.
	dueDate := issueDate.AddDate(0, 0, dueInDays)
	if dueOn != nil {
		dueDate = *dueOn
		if issueDate.After(dueDate) {
			dueDate = issueDate
		}
	}
	_, err = tx.Exec(ctx,
		`UPDATE invoices SET number = $1, issue_date = $2, due_date = $3, status = 'open' WHERE org_id = $4 AND id = $5`,
		number, issueDate, dueDate, orgID, id,
	)
	if err != nil {
		return Invoice{}, err
	}

	return GetInvoice(ctx, tx, orgID, id)
}

// VoidInvoice voids an 'open' or 'draft' invoice, keeping its number (an
// 'open' invoice has one; a 'draft' never did). Any other status returns
// ErrInvalidState. Discarding a draft this way (rather than deleting it)
// keeps it visible for audit purposes and lets billing.Void's single code
// path unlock its time entries and skip the provider call (a draft never
// has a provider_ref) the same way it does for an open invoice.
//
// An invoice with any recorded payment is refused with ErrInvalidState
// ("invoice has payments; record a credit note instead"): voiding a paid
// (or partially paid) invoice would make it disappear from the books while
// the money stayed collected. The payment count is checked after the FOR
// UPDATE lock above, in the same transaction, so a payment RecordPayment
// commits concurrently (it takes the same row lock) can't be recorded
// after this check passes and before the void commits.
func VoidInvoice(ctx context.Context, tx Querier, orgID, id int64) (Invoice, error) {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM invoices WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invoice{}, ErrNotFound
	}
	if err != nil {
		return Invoice{}, err
	}
	if status != "open" && status != "draft" {
		return Invoice{}, ErrInvalidState
	}

	var paymentCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM payments WHERE org_id = $1 AND invoice_id = $2`, orgID, id).Scan(&paymentCount); err != nil {
		return Invoice{}, err
	}
	if paymentCount > 0 {
		return Invoice{}, ErrInvoiceHasPayments
	}

	if _, err := tx.Exec(ctx, `UPDATE invoices SET status = 'void' WHERE org_id = $1 AND id = $2`, orgID, id); err != nil {
		return Invoice{}, err
	}
	return GetInvoice(ctx, tx, orgID, id)
}

// generatePaymentReference returns "INV-" followed by the base32 (no
// padding) encoding of 5 random bytes.
func generatePaymentReference() (string, error) {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "INV-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}
