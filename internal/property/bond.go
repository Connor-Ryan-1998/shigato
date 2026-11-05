package property

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/billing"
	"shigoto/internal/core"
)

// bondWarningDays is how many days after payment a held bond can go without
// an RTA lodgement before BondWarnings flags it.
const bondWarningDays = 10

// rtaRefMaxLen is the longest rta_ref LodgeBond accepts.
const rtaRefMaxLen = 64

// BondStatus derives a lease's bond status, never a stored column: "due"
// until its bond invoice is paid (a void bond invoice is still "due"), "held" once paid but not yet lodged with
// the RTA, "lodged" once an RTA reference is recorded. inv is the lease's
// bond invoice (nil if none exists yet); paidOn is the latest payment date
// against it once it's paid (see bondPaidOn).
func BondStatus(l Lease, inv *core.Invoice, paidOn *time.Time) string {
	if l.RTALodgementRef != "" {
		return "lodged"
	}
	if inv != nil && inv.Status == "paid" && paidOn != nil {
		return "held"
	}
	return "due"
}

// BondWarnings returns the 10-day RTA lodgement warning when status is
// "held" and today is more than bondWarningDays after paidOn. An exact
// 10-day anniversary does not warn - only strictly more than 10 days does.
func BondWarnings(status string, paidOn *time.Time, today time.Time) []string {
	if status != "held" || paidOn == nil {
		return nil
	}
	if today.After(paidOn.AddDate(0, 0, bondWarningDays)) {
		return []string{"bond not lodged with RTA within 10 days"}
	}
	return nil
}

// bondPaidOn returns the latest payments.paid_on recorded against inv,
// scoped to orgID, once inv is paid - nil otherwise (including when inv is
// nil, i.e. no bond invoice exists yet).
func bondPaidOn(ctx context.Context, q core.Querier, orgID int64, inv *core.Invoice) (*time.Time, error) {
	if inv == nil || inv.Status != "paid" {
		return nil, nil
	}
	var paidOn *time.Time
	if err := q.QueryRow(ctx,
		`SELECT max(paid_on) FROM payments WHERE org_id = $1 AND invoice_id = $2`,
		orgID, inv.ID,
	).Scan(&paidOn); err != nil {
		return nil, err
	}
	return paidOn, nil
}

// BondInfo loads l's bond invoice (if any) and computes its derived status
// and any warnings, as of today.
func BondInfo(ctx context.Context, q core.Querier, orgID int64, l Lease, today time.Time) (status string, warnings []string, err error) {
	var inv *core.Invoice
	if l.BondInvoiceID != nil {
		i, err := core.GetInvoice(ctx, q, orgID, *l.BondInvoiceID)
		if err != nil {
			return "", nil, err
		}
		inv = &i
	}
	paidOn, err := bondPaidOn(ctx, q, orgID, inv)
	if err != nil {
		return "", nil, err
	}
	status = BondStatus(l, inv, paidOn)
	return status, BondWarnings(status, paidOn, today), nil
}

// CreateBondInvoice creates and issues a lease's bond invoice: one line
// "Rental bond – <property name>" at GST code N-T for the lease's
// bond_amount, billed to the lease's primary tenant, due immediately.
//
// The draft invoice is created and leases.bond_invoice_id stamped in a
// single transaction holding a row lock (SELECT ... FOR UPDATE) on the
// lease, so two concurrent calls can't both create a bond invoice: the
// second sees bond_invoice_id already set and returns core.ErrInvalidState
// (409) without creating anything - unless that invoice was voided, in
// which case a new one is created and re-stamped in its place.
// billing.Issue then runs separately, after that transaction has
// committed. If Issue fails (e.g.
// billing.ErrTooFewPayOptions), the draft stays linked - a later call to
// this function still finds bond_invoice_id set and returns 409; the
// caller must issue the existing draft via the invoices API instead of
// retrying this endpoint.
func CreateBondInvoice(ctx context.Context, b *billing.Service, orgID int64, orgSlug string, leaseID int64) (core.Invoice, error) {
	invoiceID, err := createBondDraft(ctx, b, orgID, leaseID)
	if err != nil {
		return core.Invoice{}, err
	}
	return b.Issue(ctx, orgID, orgSlug, invoiceID)
}

// createBondDraft runs the locked check-then-create-then-stamp transaction
// CreateBondInvoice needs before it can call billing.Issue.
func createBondDraft(ctx context.Context, b *billing.Service, orgID, leaseID int64) (int64, error) {
	tx, err := b.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var bondAmount int64
	var existingInvoiceID *int64
	err = tx.QueryRow(ctx,
		`SELECT bond_amount, bond_invoice_id FROM leases WHERE org_id = $1 AND id = $2 FOR UPDATE`,
		orgID, leaseID,
	).Scan(&bondAmount, &existingInvoiceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, core.ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	// The invoice status is read in its own statement, after the lock is
	// held, so it sees a fresh snapshot: under READ COMMITTED a joined
	// row in the locking statement isn't re-fetched when the lock wait
	// ends, which would let a concurrent caller miss the invoice just
	// created. A voided bond invoice doesn't block a new one, which
	// re-stamps bond_invoice_id below.
	if existingInvoiceID != nil {
		var status string
		if err := tx.QueryRow(ctx,
			`SELECT status FROM invoices WHERE org_id = $1 AND id = $2`, orgID, *existingInvoiceID,
		).Scan(&status); err != nil {
			return 0, err
		}
		if status != "void" {
			return 0, core.ErrInvalidState
		}
	}
	if bondAmount == 0 {
		return 0, fmt.Errorf("%w: bond_amount is 0", core.ErrInvalidInput)
	}

	lease, err := GetLease(ctx, tx, orgID, leaseID)
	if err != nil {
		return 0, err
	}
	primaryContactID, hasPrimary := lease.primaryTenant()
	if !hasPrimary {
		return 0, fmt.Errorf("%w: lease has no primary tenant", core.ErrInvalidInput)
	}

	prop, err := GetProperty(ctx, tx, orgID, lease.PropertyID)
	if err != nil {
		return 0, err
	}

	inv, err := core.CreateDraftInvoice(ctx, tx, orgID, core.NewInvoice{
		ContactID: primaryContactID,
		Source:    "bond",
		DueInDays: 0,
		Lines: []core.NewLine{{
			Description:   fmt.Sprintf("Rental bond – %s", prop.Name),
			QuantityMilli: 1000,
			UnitAmount:    lease.BondAmount,
			GSTCode:       string(core.NT),
			PropertyID:    &prop.ID,
		}},
	})
	if err != nil {
		return 0, err
	}

	if _, err := tx.Exec(ctx, `UPDATE leases SET bond_invoice_id = $1 WHERE org_id = $2 AND id = $3`, inv.ID, orgID, leaseID); err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return inv.ID, nil
}

// primaryTenant returns l's primary tenant's contact id, or false if it has
// none (shouldn't happen - CreateLease enforces exactly one - but callers
// guard rather than invoice a lease with nowhere to send the bill).
func (l Lease) primaryTenant() (contactID int64, ok bool) {
	for _, t := range l.Tenants {
		if t.IsPrimary {
			return t.ContactID, true
		}
	}
	return 0, false
}

// LodgeBond records a lease's RTA lodgement (rta_ref and lodged_on) once
// its bond's derived status is "held"; any other status returns
// core.ErrInvalidState. It locks the lease row for the duration of the
// check-then-write so a concurrent lodgement attempt can't race. lodgedOn
// must not be before the bond's paid date, nor after today, and rtaRef
// must be non-empty and at most 64 characters. It does not start its own
// transaction: pass a tx if the caller needs the write to roll back with
// other work.
func LodgeBond(ctx context.Context, q core.Querier, orgID int64, actor *int64, leaseID int64, rtaRef string, lodgedOn, today time.Time) (Lease, error) {
	if rtaRef == "" {
		return Lease{}, fmt.Errorf("%w: rta_ref is required", core.ErrInvalidInput)
	}
	if len(rtaRef) > rtaRefMaxLen {
		return Lease{}, fmt.Errorf("%w: rta_ref must be at most %d characters", core.ErrInvalidInput, rtaRefMaxLen)
	}
	if lodgedOn.After(today) {
		return Lease{}, fmt.Errorf("%w: lodged_on must not be in the future", core.ErrInvalidInput)
	}

	var dummy int64
	err := q.QueryRow(ctx, `SELECT id FROM leases WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, leaseID).Scan(&dummy)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, core.ErrNotFound
	}
	if err != nil {
		return Lease{}, err
	}

	lease, err := GetLease(ctx, q, orgID, leaseID)
	if err != nil {
		return Lease{}, err
	}

	var inv *core.Invoice
	if lease.BondInvoiceID != nil {
		i, err := core.GetInvoice(ctx, q, orgID, *lease.BondInvoiceID)
		if err != nil {
			return Lease{}, err
		}
		inv = &i
	}
	paidOn, err := bondPaidOn(ctx, q, orgID, inv)
	if err != nil {
		return Lease{}, err
	}
	if BondStatus(lease, inv, paidOn) != "held" {
		return Lease{}, core.ErrInvalidState
	}
	if paidOn != nil && lodgedOn.Before(*paidOn) {
		return Lease{}, fmt.Errorf("%w: lodged_on must not be before the bond was paid", core.ErrInvalidInput)
	}

	if _, err := q.Exec(ctx,
		`UPDATE leases SET rta_lodgement_ref = $1, bond_lodged_on = $2 WHERE org_id = $3 AND id = $4`,
		rtaRef, lodgedOn, orgID, leaseID,
	); err != nil {
		return Lease{}, err
	}

	if err := core.Audit(ctx, q, orgID, actor, "lease", leaseID, "bond_lodge",
		map[string]string{"rta_ref": rtaRef, "lodged_on": lodgedOn.Format(dateLayout)},
	); err != nil {
		return Lease{}, err
	}

	return GetLease(ctx, q, orgID, leaseID)
}
