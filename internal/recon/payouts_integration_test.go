//go:build integration

package recon_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/payments"
	"shigoto/internal/recon"
)

// testStripePayment issues a new invoice totalling totalCents and records a
// webhook-style payment against it (method "stripe", the given
// provider_ref), returning the invoice id and payment id.
func testStripePayment(t *testing.T, pool *pgxpool.Pool, orgID, contactID int64, totalCents int64, paidOn time.Time, providerRef string) (invoiceID, paymentID int64) {
	t.Helper()
	inv := testInvoice(t, pool, orgID, contactID, totalCents)
	payment, _, err := core.RecordPayment(context.Background(), pool, orgID, core.NewPayment{
		InvoiceID: inv.ID, Amount: core.Cents(totalCents), PaidOn: paidOn, Method: "stripe", ProviderRef: providerRef,
	})
	if err != nil {
		t.Fatalf("RecordPayment: %v", err)
	}
	return inv.ID, payment.ID
}

// testBillingService builds a *billing.Service against pool whose Payments
// resolver always returns provider (or payments.ErrNoProvider if provider
// is nil), for MatchPayout/Suggest tests that never exercise the real
// Stripe credential lookup.
func testBillingService(pool *pgxpool.Pool, provider payments.Provider) *billing.Service {
	return &billing.Service{
		Pool:   pool,
		GetEnv: func(string) string { return "" },
		Payments: func(string) (payments.Provider, error) {
			if provider == nil {
				return nil, payments.ErrNoProvider
			}
			return provider, nil
		},
	}
}

func paymentPayoutAndLine(t *testing.T, pool *pgxpool.Pool, paymentID int64) (payoutID, bankLineID *int64, paidOn time.Time) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `SELECT payout_id, bank_line_id, paid_on FROM payments WHERE id = $1`, paymentID).
		Scan(&payoutID, &bankLineID, &paidOn); err != nil {
		t.Fatalf("reading payment: %v", err)
	}
	return payoutID, bankLineID, paidOn
}

// TestMatchPayoutLinksPaymentsAndBooksFees covers the happy path: two
// webhook payments (pi_1, pi_2) covered by one payout whose net amount is
// their total less Stripe's fees. MatchPayout must link both payments to
// the payout and the bank line, and book the fees as a "Stripe fees"
// expense with GST, while leaving each payment's original paid_on (cash
// basis) untouched.
func TestMatchPayoutLinksPaymentsAndBooksFees(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	setGSTRegistered(t, pool, org.ID, true)
	contactID := createContact(t, pool, org.ID)

	paid1 := mustDate(t, "2026-11-01")
	paid2 := mustDate(t, "2026-11-02")
	_, payment1 := testStripePayment(t, pool, org.ID, contactID, 10000, paid1, "pi_1")
	_, payment2 := testStripePayment(t, pool, org.ID, contactID, 20000, paid2, "pi_2")

	arrival := mustDate(t, "2026-11-05")
	netAmount := int64(10000 + 20000 - 330)
	lineID := testBankLineID(t, pool, org.ID, netAmount, arrival, "Stripe payout")

	fake := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_1": {Ref: "po_1", Amount: core.Cents(netAmount), ArrivalOn: arrival, Status: "paid"},
		},
		Transactions: map[string][]payments.BalanceTxn{
			"po_1": {
				{Ref: "txn_1", Type: "charge", PaymentRef: "pi_1", Amount: 10000, Fee: 110},
				{Ref: "txn_2", Type: "charge", PaymentRef: "pi_2", Amount: 20000, Fee: 220},
			},
		},
	}
	b := testBillingService(pool, fake)

	result, err := recon.MatchPayout(ctx, pool, b, org.ID, "acme", lineID, "po_1")
	if err != nil {
		t.Fatalf("MatchPayout: %v", err)
	}
	if result.PaymentsLinked != 2 {
		t.Fatalf("PaymentsLinked = %d, want 2", result.PaymentsLinked)
	}
	if result.Fees != 330 {
		t.Fatalf("Fees = %d, want 330", result.Fees)
	}
	if result.FeeExpenseID == nil {
		t.Fatalf("FeeExpenseID = nil, want set")
	}

	exp, err := recon.GetExpense(ctx, pool, org.ID, *result.FeeExpenseID)
	if err != nil {
		t.Fatalf("GetExpense: %v", err)
	}
	if len(exp.Lines) != 1 {
		t.Fatalf("len(exp.Lines) = %d, want 1", len(exp.Lines))
	}
	if exp.Lines[0].Amount != 330 || exp.Lines[0].GSTAmount != 30 {
		t.Fatalf("fee expense line = %+v, want amount 330, GST 30", exp.Lines[0])
	}
	stripeFeesCat := categoryByName(t, pool, org.ID, "Stripe fees")
	if exp.Lines[0].CategoryID != stripeFeesCat.ID {
		t.Fatalf("fee expense category = %d, want %d (Stripe fees)", exp.Lines[0].CategoryID, stripeFeesCat.ID)
	}

	for _, pid := range []int64{payment1, payment2} {
		payoutID, bankLineID, paidOn := paymentPayoutAndLine(t, pool, pid)
		if payoutID == nil {
			t.Errorf("payment %d: payout_id = nil, want set", pid)
		}
		if bankLineID == nil || *bankLineID != lineID {
			t.Errorf("payment %d: bank_line_id = %v, want %d", pid, bankLineID, lineID)
		}
		_ = paidOn
	}

	p1PaidOn := paymentPaidOn(t, pool, payment1)
	if !p1PaidOn.Equal(paid1) {
		t.Errorf("payment1 paid_on = %v, want unchanged %v", p1PaidOn, paid1)
	}
	p2PaidOn := paymentPaidOn(t, pool, payment2)
	if !p2PaidOn.Equal(paid2) {
		t.Errorf("payment2 paid_on = %v, want unchanged %v", p2PaidOn, paid2)
	}

	if status, _ := bankLineStatus(t, pool, lineID); status != "matched" {
		t.Fatalf("bank line status = %s, want matched", status)
	}
}

func paymentPaidOn(t *testing.T, pool *pgxpool.Pool, paymentID int64) time.Time {
	t.Helper()
	var paidOn time.Time
	if err := pool.QueryRow(context.Background(), `SELECT paid_on FROM payments WHERE id = $1`, paymentID).Scan(&paidOn); err != nil {
		t.Fatalf("reading paid_on: %v", err)
	}
	return paidOn
}

// TestMatchPayoutAmountMismatch409NoWrites covers that a payout whose
// amount doesn't match the bank line is rejected (409) before anything is
// written: the line stays unmatched and no payout row is created.
func TestMatchPayoutAmountMismatch409NoWrites(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)
	_, payment1 := testStripePayment(t, pool, org.ID, contactID, 10000, mustDate(t, "2026-11-01"), "pi_1")

	arrival := mustDate(t, "2026-11-05")
	lineID := testBankLineID(t, pool, org.ID, 9000, arrival, "Stripe payout")

	fake := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_1": {Ref: "po_1", Amount: 10000, ArrivalOn: arrival, Status: "paid"},
		},
		Transactions: map[string][]payments.BalanceTxn{
			"po_1": {{Ref: "txn_1", Type: "charge", PaymentRef: "pi_1", Amount: 10000, Fee: 0}},
		},
	}
	b := testBillingService(pool, fake)

	_, err := recon.MatchPayout(ctx, pool, b, org.ID, "acme", lineID, "po_1")
	if !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("err = %v, want ErrInvalidState", err)
	}

	if status, _ := bankLineStatus(t, pool, lineID); status != "unmatched" {
		t.Fatalf("bank line status = %s, want unmatched", status)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payouts WHERE org_id = $1`, org.ID).Scan(&n); err != nil {
		t.Fatalf("counting payouts: %v", err)
	}
	if n != 0 {
		t.Fatalf("payouts rows = %d, want 0", n)
	}
	if payoutID, _, _ := paymentPayoutAndLine(t, pool, payment1); payoutID != nil {
		t.Fatalf("payment payout_id = %v, want nil", payoutID)
	}
}

// TestMatchPayoutUnknownCharge409 covers that a payout transaction whose
// payment intent matches no recorded payment is rejected (409) and nothing
// is written.
func TestMatchPayoutUnknownCharge409(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")

	arrival := mustDate(t, "2026-11-05")
	lineID := testBankLineID(t, pool, org.ID, 10000, arrival, "Stripe payout")

	fake := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_1": {Ref: "po_1", Amount: 10000, ArrivalOn: arrival, Status: "paid"},
		},
		Transactions: map[string][]payments.BalanceTxn{
			"po_1": {{Ref: "txn_unknown", Type: "charge", PaymentRef: "pi_does_not_exist", Amount: 10000, Fee: 110}},
		},
	}
	b := testBillingService(pool, fake)

	_, err := recon.MatchPayout(ctx, pool, b, org.ID, "acme", lineID, "po_1")
	if !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("err = %v, want ErrInvalidState", err)
	}
	if status, _ := bankLineStatus(t, pool, lineID); status != "unmatched" {
		t.Fatalf("bank line status = %s, want unmatched", status)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payouts WHERE org_id = $1`, org.ID).Scan(&n); err != nil {
		t.Fatalf("counting payouts: %v", err)
	}
	if n != 0 {
		t.Fatalf("payouts rows = %d, want 0", n)
	}
}

// TestUnmatchPayoutReverses covers that Unmatch on a payout line clears the
// linked payments' payout_id/bank_line_id (without deleting them), deletes
// the fee expense and the payout row, and marks the line unmatched again.
func TestUnmatchPayoutReverses(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)

	paidOn := mustDate(t, "2026-11-01")
	_, payment1 := testStripePayment(t, pool, org.ID, contactID, 10000, paidOn, "pi_1")

	arrival := mustDate(t, "2026-11-03")
	netAmount := int64(10000 - 110)
	lineID := testBankLineID(t, pool, org.ID, netAmount, arrival, "Stripe payout")

	fake := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_1": {Ref: "po_1", Amount: core.Cents(netAmount), ArrivalOn: arrival, Status: "paid"},
		},
		Transactions: map[string][]payments.BalanceTxn{
			"po_1": {{Ref: "txn_1", Type: "charge", PaymentRef: "pi_1", Amount: 10000, Fee: 110}},
		},
	}
	b := testBillingService(pool, fake)

	result, err := recon.MatchPayout(ctx, pool, b, org.ID, "acme", lineID, "po_1")
	if err != nil {
		t.Fatalf("MatchPayout: %v", err)
	}
	feeExpenseID := *result.FeeExpenseID

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if err := recon.Unmatch(ctx, tx, org.ID, lineID); err != nil {
		t.Fatalf("Unmatch: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if status, reason := bankLineStatus(t, pool, lineID); status != "unmatched" || reason != nil {
		t.Fatalf("bank line after unmatch = (%s, %v), want (unmatched, nil)", status, reason)
	}
	payoutID, bankLineID, _ := paymentPayoutAndLine(t, pool, payment1)
	if payoutID != nil {
		t.Errorf("payment payout_id after unmatch = %v, want nil", payoutID)
	}
	if bankLineID != nil {
		t.Errorf("payment bank_line_id after unmatch = %v, want nil", bankLineID)
	}

	var stillExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payments WHERE id = $1)`, payment1).Scan(&stillExists); err != nil {
		t.Fatalf("checking payment existence: %v", err)
	}
	if !stillExists {
		t.Fatalf("payment %d was deleted; webhook payments must never be deleted", payment1)
	}

	if _, err := recon.GetExpense(ctx, pool, org.ID, feeExpenseID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("fee expense after unmatch: err = %v, want ErrNotFound", err)
	}

	var payoutRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payouts WHERE org_id = $1`, org.ID).Scan(&payoutRows); err != nil {
		t.Fatalf("counting payouts: %v", err)
	}
	if payoutRows != 0 {
		t.Fatalf("payouts rows after unmatch = %d, want 0", payoutRows)
	}
}
