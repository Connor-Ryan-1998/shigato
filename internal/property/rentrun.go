package property

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/billing"
	"shigoto/internal/core"
)

// RunResult summarises one rent run: how many periods got a new draft
// invoice, how many of those were then auto-issued, and how many failed
// (an auto-issue failure, or a lease with no primary tenant).
type RunResult struct {
	Created int `json:"created"`
	Issued  int `json:"issued"`
	Failed  int `json:"failed"`
}

// gstCodeForRent returns the GST code a rent or charge line takes, per
// global-constraints.md: residential rent is always input-taxed;
// non-residential follows the org's GST registration.
func gstCodeForRent(residential, gstRegistered bool) core.GSTCode {
	switch {
	case residential:
		return core.INP
	case gstRegistered:
		return core.GST
	default:
		return core.FRE
	}
}

// RunRent generates rent-period invoices for every lease in orgID whose
// uninvoiced periods fall on or before asOf plus the org's invoice lead
// time. It is idempotent: a period already recorded in rent_periods
// (unique on lease_id, period_start) is skipped rather than re-invoiced, so
// running it twice - or concurrently - for the same day is safe. If the
// org has auto_send_rent on, each newly created invoice is issued
// immediately after its period's transaction commits; an issue failure
// (e.g. too few pay options) leaves the draft in place, counts as Failed,
// and is audited as rent_issue_failed.
func RunRent(ctx context.Context, b *billing.Service, orgID int64, orgSlug string, asOf time.Time) (RunResult, error) {
	settings, err := core.GetSettings(ctx, b.Pool, orgID)
	if err != nil {
		return RunResult{}, err
	}

	y, m, dd := asOf.Date()
	asOf = time.Date(y, m, dd, 0, 0, 0, 0, time.UTC)
	cutoff := asOf.AddDate(0, 0, settings.InvoiceLeadDays)

	leaseIDs, err := leasesStartingBy(ctx, b.Pool, orgID, cutoff)
	if err != nil {
		return RunResult{}, err
	}

	var result RunResult
	for _, leaseID := range leaseIDs {
		if err := runRentForLease(ctx, b, orgID, orgSlug, leaseID, asOf, settings, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

// leasesStartingBy returns, in order, the ids of orgID's leases whose
// start_date is on or before cutoff. A lease that has since ended or
// terminated is still included: PeriodsToInvoice naturally yields no
// periods for one with nothing left to invoice, and still yields any
// periods it hasn't caught up on yet for one that has.
func leasesStartingBy(ctx context.Context, q core.Querier, orgID int64, cutoff time.Time) ([]int64, error) {
	rows, err := q.Query(ctx, `SELECT id FROM leases WHERE org_id = $1 AND start_date <= $2 ORDER BY id`, orgID, cutoff)
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

// runRentForLease builds leaseID's LeaseTerms, generates its uninvoiced
// periods and creates (and, if configured, issues) an invoice for each.
func runRentForLease(ctx context.Context, b *billing.Service, orgID int64, orgSlug string, leaseID int64, asOf time.Time, settings core.OrgSettings, result *RunResult) error {
	lease, err := GetLease(ctx, b.Pool, orgID, leaseID)
	if err != nil {
		return err
	}

	var latestEnd *time.Time
	if err := b.Pool.QueryRow(ctx, `SELECT max(period_end) FROM rent_periods WHERE lease_id = $1`, leaseID).Scan(&latestEnd); err != nil {
		return err
	}
	paidTo := lease.PaidToDate
	if latestEnd != nil && (paidTo == nil || latestEnd.After(*paidTo)) {
		paidTo = latestEnd
	}

	terms := lease.ToTerms()
	terms.PaidTo = paidTo

	periods := PeriodsToInvoice(terms, asOf, settings.InvoiceLeadDays)
	if len(periods) == 0 {
		return nil
	}

	var primaryContactID int64
	hasPrimary := false
	for _, t := range lease.Tenants {
		if t.IsPrimary {
			primaryContactID = t.ContactID
			hasPrimary = true
			break
		}
	}
	// Task 4 enforces exactly one primary tenant at lease-creation time,
	// so this shouldn't happen; guard anyway rather than invoice a lease
	// with nowhere to send the bill.
	if !hasPrimary {
		result.Failed += len(periods)
		return nil
	}

	prop, err := GetProperty(ctx, b.Pool, orgID, lease.PropertyID)
	if err != nil {
		return err
	}
	gstCode := gstCodeForRent(prop.Residential, settings.GSTRegistered)

	for _, period := range periods {
		invoiceID, created, err := createPeriodInvoice(ctx, b.Pool, orgID, leaseID, primaryContactID, prop.ID, gstCode, asOf, period)
		if err != nil {
			return err
		}
		if !created {
			// Another run already claimed this period.
			continue
		}
		result.Created++

		if !settings.AutoSendRent {
			continue
		}
		if _, err := b.Issue(ctx, orgID, orgSlug, invoiceID); err != nil {
			result.Failed++
			auditRentIssueFailed(ctx, b.Pool, orgID, invoiceID, err)
			continue
		}
		result.Issued++
	}
	return nil
}

// periodTotal is the pre-GST subtotal of a period: its rent plus every
// charge. It's the rent_periods.amount_due placeholder stored before the
// invoice exists; createPeriodInvoice corrects it to the invoice's actual
// total (which may include GST) once that's known.
func periodTotal(p Period) core.Cents {
	total := p.Rent
	for _, c := range p.Charges {
		total += c.Amount
	}
	return total
}

// createPeriodInvoice runs one period's whole idempotent write - the
// rent_periods insert, the draft invoice and lines, and stamping
// rent_periods.invoice_id - in a single transaction. If the period is
// already recorded (ON CONFLICT DO NOTHING finds nothing to insert, e.g. a
// concurrent run claimed it first), it returns created=false and does
// nothing else.
func createPeriodInvoice(ctx context.Context, pool *pgxpool.Pool, orgID, leaseID, contactID, propertyID int64, gstCode core.GSTCode, asOf time.Time, period Period) (invoiceID int64, created bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var periodID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO rent_periods (org_id, lease_id, period_start, period_end, amount_due)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (lease_id, period_start) DO NOTHING
		RETURNING id`,
		orgID, leaseID, period.Start, period.End, int64(periodTotal(period)),
	).Scan(&periodID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}

	dueInDays := int(period.Start.Sub(asOf).Hours() / 24)
	if dueInDays < 0 {
		dueInDays = 0
	}

	lines := make([]core.NewLine, 0, 1+len(period.Charges))
	lines = append(lines, core.NewLine{
		Description:   fmt.Sprintf("Rent %s–%s", period.Start.Format(dateLayout), period.End.Format(dateLayout)),
		QuantityMilli: 1000,
		UnitAmount:    period.Rent,
		GSTCode:       string(gstCode),
		PropertyID:    &propertyID,
	})
	for _, c := range period.Charges {
		lines = append(lines, core.NewLine{
			Description:   c.Description,
			QuantityMilli: 1000,
			UnitAmount:    c.Amount,
			GSTCode:       string(gstCode),
			PropertyID:    &propertyID,
		})
	}

	inv, err := core.CreateDraftInvoice(ctx, tx, orgID, core.NewInvoice{
		ContactID: contactID,
		Source:    "rent",
		DueInDays: dueInDays,
		Lines:     lines,
	})
	if err != nil {
		return 0, false, err
	}

	if _, err := tx.Exec(ctx, `UPDATE rent_periods SET invoice_id = $1, amount_due = $2 WHERE id = $3`, inv.ID, int64(inv.Total), periodID); err != nil {
		return 0, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, false, err
	}
	return inv.ID, true, nil
}

// LedgerPeriod is one rent period on a lease's ledger.
type LedgerPeriod struct {
	Start, End    time.Time
	AmountDue     core.Cents
	InvoiceID     *int64
	InvoiceStatus string
	Paid          core.Cents
}

// Ledger is a lease's full rent ledger: every rent period recorded against
// it, plus the running arrears figure.
type Ledger struct {
	Periods        []LedgerPeriod
	OpeningArrears core.Cents
	Arrears        core.Cents
}

// GetLedger builds leaseID's ledger, scoped to orgID. Arrears is the
// lease's opening_arrears plus the outstanding balance of every period
// whose invoice is open or paid (void and draft periods don't count), each
// floored at 0 so a period that's been overpaid can't offset arrears owed
// on another.
func GetLedger(ctx context.Context, q core.Querier, orgID, leaseID int64) (Ledger, error) {
	lease, err := GetLease(ctx, q, orgID, leaseID)
	if err != nil {
		return Ledger{}, err
	}

	rows, err := q.Query(ctx, `
		SELECT rp.period_start, rp.period_end, rp.amount_due, rp.invoice_id,
			coalesce(i.status, ''), coalesce(pay.paid, 0)
		FROM rent_periods rp
		LEFT JOIN invoices i ON i.org_id = rp.org_id AND i.id = rp.invoice_id
		LEFT JOIN (
			SELECT invoice_id, sum(amount) AS paid FROM payments WHERE org_id = $1 GROUP BY invoice_id
		) pay ON pay.invoice_id = rp.invoice_id
		WHERE rp.org_id = $1 AND rp.lease_id = $2
		ORDER BY rp.period_start`,
		orgID, leaseID,
	)
	if err != nil {
		return Ledger{}, err
	}
	defer rows.Close()

	ledger := Ledger{OpeningArrears: lease.OpeningArrears, Arrears: lease.OpeningArrears}
	for rows.Next() {
		var lp LedgerPeriod
		var amountDue, paid int64
		if err := rows.Scan(&lp.Start, &lp.End, &amountDue, &lp.InvoiceID, &lp.InvoiceStatus, &paid); err != nil {
			return Ledger{}, err
		}
		lp.AmountDue = core.Cents(amountDue)
		lp.Paid = core.Cents(paid)
		ledger.Periods = append(ledger.Periods, lp)

		if lp.InvoiceStatus == "open" || lp.InvoiceStatus == "paid" {
			outstanding := lp.AmountDue - lp.Paid
			if outstanding > 0 {
				ledger.Arrears += outstanding
			}
		}
	}
	if err := rows.Err(); err != nil {
		return Ledger{}, err
	}
	return ledger, nil
}

// auditRentIssueFailed records a rent_issue_failed audit entry with no
// actor. The diff carries a coarse error kind rather than err's own
// message, which may embed provider or internal details not meant for the
// audit log.
func auditRentIssueFailed(ctx context.Context, pool *pgxpool.Pool, orgID, invoiceID int64, cause error) {
	kind := "internal_error"
	if errors.Is(cause, billing.ErrTooFewPayOptions) {
		kind = "too_few_pay_options"
	}
	if err := core.Audit(ctx, pool, orgID, nil, "invoice", invoiceID, "rent_issue_failed", map[string]string{"error": kind}); err != nil {
		slog.Error("auditing rent_issue_failed", "invoice_id", invoiceID, "error", err)
	}
}
