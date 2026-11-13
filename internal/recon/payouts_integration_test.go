//go:build integration

package recon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/module"
	"shigoto/internal/payments"
	"shigoto/internal/recon"
	"shigoto/internal/testkit"
)

// testStripePayment issues a new invoice totalling totalCents and records a
// webhook-style payment against it (method "stripe", the given
// provider_ref), returning the invoice id and payment id.
func testStripePayment(e *testkit.Env, org core.Org, contactID int64, totalCents int64, paidOn time.Time, providerRef string) (invoiceID, paymentID int64) {
	e.T.Helper()
	inv := e.Invoice(org, contactID, testkit.Amount(totalCents), testkit.Issued(time.Now()))
	payment, _, err := core.RecordPayment(e.Ctx, e.Pool, org.ID, core.NewPayment{
		InvoiceID: inv.ID, Amount: core.Cents(totalCents), PaidOn: paidOn, Method: "stripe", ProviderRef: providerRef,
	})
	if err != nil {
		e.T.Fatalf("RecordPayment: %v", err)
	}
	return inv.ID, payment.ID
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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	e.Settings(org, testkit.GSTRegistered(true))
	contactID := e.Contact(org).ID

	paid1 := testkit.Date("2026-11-01")
	paid2 := testkit.Date("2026-11-02")
	_, payment1 := testStripePayment(e, org, contactID, 10000, paid1, "pi_1")
	_, payment2 := testStripePayment(e, org, contactID, 20000, paid2, "pi_2")

	arrival := testkit.Date("2026-11-05")
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
	b := e.BillingService(testkit.Provider(fake))

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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org).ID
	_, payment1 := testStripePayment(e, org, contactID, 10000, testkit.Date("2026-11-01"), "pi_1")

	arrival := testkit.Date("2026-11-05")
	lineID := testBankLineID(t, pool, org.ID, 9000, arrival, "Stripe payout")

	fake := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_1": {Ref: "po_1", Amount: 10000, ArrivalOn: arrival, Status: "paid"},
		},
		Transactions: map[string][]payments.BalanceTxn{
			"po_1": {{Ref: "txn_1", Type: "charge", PaymentRef: "pi_1", Amount: 10000, Fee: 0}},
		},
	}
	b := e.BillingService(testkit.Provider(fake))

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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")

	arrival := testkit.Date("2026-11-05")
	lineID := testBankLineID(t, pool, org.ID, 10000, arrival, "Stripe payout")

	fake := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_1": {Ref: "po_1", Amount: 10000, ArrivalOn: arrival, Status: "paid"},
		},
		Transactions: map[string][]payments.BalanceTxn{
			"po_1": {{Ref: "txn_unknown", Type: "charge", PaymentRef: "pi_does_not_exist", Amount: 10000, Fee: 110}},
		},
	}
	b := e.BillingService(testkit.Provider(fake))

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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org).ID

	paidOn := testkit.Date("2026-11-01")
	_, payment1 := testStripePayment(e, org, contactID, 10000, paidOn, "pi_1")

	arrival := testkit.Date("2026-11-03")
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
	b := e.BillingService(testkit.Provider(fake))

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

// TestMatchPayoutRouteRespondsWithOffendingRefs covers the route end to
// end: a 409 from an unmatched payout transaction must reach the client
// with a body naming the offending transaction ref (core.PublicError routed
// through module.WriteCoreError), not the generic "invalid state" text.
func TestMatchPayoutRouteRespondsWithOffendingRefs(t *testing.T) {
	fake := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_1": {Ref: "po_1", Amount: 10000, ArrivalOn: testkit.Date("2026-11-05"), Status: "paid"},
		},
		Transactions: map[string][]payments.BalanceTxn{
			"po_1": {{Ref: "txn_unknown", Type: "charge", PaymentRef: "pi_does_not_exist", Amount: 10000, Fee: 0}},
		},
	}
	e := testkit.New(t)
	pool := e.Pool
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler(testkit.Payments(func(string) (payments.Provider, error) { return fake, nil })))
	lineID := testBankLineID(t, pool, org.ID, 10000, testkit.Date("2026-11-05"), "Stripe payout")

	body, _ := json.Marshal(map[string]string{"payout_ref": "po_1"})
	url := fmt.Sprintf("/api/v1/orgs/acme/bank-lines/%d/match-payout", lineID)
	rr := s.Do(http.MethodPost, url, bytes.NewReader(body))

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rr.Code, rr.Body)
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rr.Body, &resp); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if resp.Error == "invalid state" {
		t.Fatalf("error = %q, want the specific offending-ref message, not the generic text", resp.Error)
	}
	if !bytes.Contains([]byte(resp.Error), []byte("txn_unknown")) {
		t.Fatalf("error = %q, want it to name txn_unknown", resp.Error)
	}
}

// TestMatchPayoutFallsBackToInvoiceRef covers a real-world webhook payment
// whose provider_ref is the Stripe invoice id (the fallback paymentRef uses
// when a real invoice.paid event doesn't expand `payments`), not the
// payment intent id: the balance transaction's PaymentRef (the PI) won't
// match it, but its InvoiceRef must.
func TestMatchPayoutFallsBackToInvoiceRef(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	e.Settings(org, testkit.GSTRegistered(true))
	contactID := e.Contact(org).ID

	paidOn := testkit.Date("2026-11-01")
	_, payment1 := testStripePayment(e, org, contactID, 10000, paidOn, "in_test_789")

	arrival := testkit.Date("2026-11-03")
	netAmount := int64(10000 - 110)
	lineID := testBankLineID(t, pool, org.ID, netAmount, arrival, "Stripe payout")

	fake := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_1": {Ref: "po_1", Amount: core.Cents(netAmount), ArrivalOn: arrival, Status: "paid"},
		},
		Transactions: map[string][]payments.BalanceTxn{
			"po_1": {{Ref: "txn_1", Type: "charge", PaymentRef: "pi_test_456", InvoiceRef: "in_test_789", Amount: 10000, Fee: 110}},
		},
	}
	b := e.BillingService(testkit.Provider(fake))

	result, err := recon.MatchPayout(ctx, pool, b, org.ID, "acme", lineID, "po_1")
	if err != nil {
		t.Fatalf("MatchPayout: %v", err)
	}
	if result.PaymentsLinked != 1 {
		t.Fatalf("PaymentsLinked = %d, want 1", result.PaymentsLinked)
	}
	payoutID, _, _ := paymentPayoutAndLine(t, pool, payment1)
	if payoutID == nil {
		t.Fatalf("payment payout_id = nil, want set (matched via InvoiceRef fallback)")
	}
}

// TestMatchPayoutDoubleMatch409 covers that matching the same payout ref
// twice - to a second bank line - is rejected with a 409, not a crash or a
// duplicate payouts row, both when caught by the pre-check and (via a
// second payout sharing the same ref/line setup) the insert's unique
// constraint.
func TestMatchPayoutDoubleMatch409(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	e.Settings(org, testkit.GSTRegistered(true))
	contactID := e.Contact(org).ID

	_, payment1 := testStripePayment(e, org, contactID, 10000, testkit.Date("2026-11-01"), "pi_1")
	arrival := testkit.Date("2026-11-03")
	netAmount := int64(10000 - 110)
	line1 := testBankLineID(t, pool, org.ID, netAmount, arrival, "Stripe payout")
	line2 := testBankLineID(t, pool, org.ID, netAmount, arrival, "Stripe payout duplicate")

	fake := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_1": {Ref: "po_1", Amount: core.Cents(netAmount), ArrivalOn: arrival, Status: "paid"},
		},
		Transactions: map[string][]payments.BalanceTxn{
			"po_1": {{Ref: "txn_1", Type: "charge", PaymentRef: "pi_1", Amount: 10000, Fee: 110}},
		},
	}
	b := e.BillingService(testkit.Provider(fake))

	if _, err := recon.MatchPayout(ctx, pool, b, org.ID, "acme", line1, "po_1"); err != nil {
		t.Fatalf("first MatchPayout: %v", err)
	}
	_ = payment1

	_, err := recon.MatchPayout(ctx, pool, b, org.ID, "acme", line2, "po_1")
	if !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("second MatchPayout: err = %v, want ErrInvalidState", err)
	}
	if status, _ := bankLineStatus(t, pool, line2); status != "unmatched" {
		t.Fatalf("line2 status = %s, want unmatched", status)
	}
	var payoutRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payouts WHERE org_id = $1 AND provider_ref = 'po_1'`, org.ID).Scan(&payoutRows); err != nil {
		t.Fatalf("counting payouts: %v", err)
	}
	if payoutRows != 1 {
		t.Fatalf("payouts rows for po_1 = %d, want 1", payoutRows)
	}
}

// TestMatchPayoutRequiresPaidStatus409 covers that a payout whose status
// isn't "paid" (still pending/in_transit, or failed) is rejected before any
// write.
func TestMatchPayoutRequiresPaidStatus409(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	arrival := testkit.Date("2026-11-05")
	lineID := testBankLineID(t, pool, org.ID, 10000, arrival, "Stripe payout")

	fake := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_1": {Ref: "po_1", Amount: 10000, ArrivalOn: arrival, Status: "in_transit"},
		},
	}
	b := e.BillingService(testkit.Provider(fake))

	_, err := recon.MatchPayout(ctx, pool, b, org.ID, "acme", lineID, "po_1")
	if !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("err = %v, want ErrInvalidState", err)
	}
	if status, _ := bankLineStatus(t, pool, lineID); status != "unmatched" {
		t.Fatalf("bank line status = %s, want unmatched", status)
	}
}

// TestMatchPayoutEmptyRef422 covers that an empty payout_ref is rejected
// before any provider call.
func TestMatchPayoutEmptyRef422(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	lineID := testBankLineID(t, pool, org.ID, 10000, testkit.Date("2026-11-05"), "Stripe payout")

	b := e.BillingService(testkit.Provider(&payments.Fake{}))
	_, err := recon.MatchPayout(ctx, pool, b, org.ID, "acme", lineID, "")
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

// TestMatchPayoutNotFound422 covers that a payout the provider doesn't
// recognise (payments.ErrPayoutNotFound) is reported as 422, not a generic
// failure.
func TestMatchPayoutNotFound422(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	lineID := testBankLineID(t, pool, org.ID, 10000, testkit.Date("2026-11-05"), "Stripe payout")

	b := e.BillingService(testkit.Provider(&payments.Fake{}))
	_, err := recon.MatchPayout(ctx, pool, b, org.ID, "acme", lineID, "po_does_not_exist")
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

// TestMatchPayoutSkipsPayoutTxnAndChecksSum covers that the payout's own
// balance transaction (type "payout") is skipped entirely, and that the
// remaining transactions' net total (amount - fee) must equal the payout
// amount: a payout whose listed transactions don't actually add up (e.g. a
// truncated list, or extra untracked activity) is rejected rather than
// silently booking whatever happened to map.
func TestMatchPayoutSkipsPayoutTxnAndChecksSum(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	e.Settings(org, testkit.GSTRegistered(true))
	contactID := e.Contact(org).ID

	_, payment1 := testStripePayment(e, org, contactID, 10000, testkit.Date("2026-11-01"), "pi_1")
	_ = payment1
	arrival := testkit.Date("2026-11-03")
	netAmount := int64(10000 - 110)
	lineID := testBankLineID(t, pool, org.ID, netAmount, arrival, "Stripe payout")

	// Sum mismatch: the charge's net (9890) alone would equal netAmount,
	// but an extra stripe_fee transaction throws the total off.
	fakeMismatch := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_1": {Ref: "po_1", Amount: core.Cents(netAmount), ArrivalOn: arrival, Status: "paid"},
		},
		Transactions: map[string][]payments.BalanceTxn{
			"po_1": {
				{Ref: "txn_1", Type: "charge", PaymentRef: "pi_1", Amount: 10000, Fee: 110},
				{Ref: "txn_2", Type: "stripe_fee", Amount: -50, Fee: 0},
			},
		},
	}
	b := e.BillingService(testkit.Provider(fakeMismatch))
	_, err := recon.MatchPayout(ctx, pool, b, org.ID, "acme", lineID, "po_1")
	if !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("sum mismatch: err = %v, want ErrInvalidState", err)
	}
	if status, _ := bankLineStatus(t, pool, lineID); status != "unmatched" {
		t.Fatalf("bank line status after mismatch = %s, want unmatched", status)
	}

	// The payout's own balance transaction (type "payout") is included in
	// the list (as a real Stripe response would, alongside the charge),
	// but must be skipped rather than rejected as an unexpected type or
	// counted into the sum.
	fakeOK := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_1": {Ref: "po_1", Amount: core.Cents(netAmount), ArrivalOn: arrival, Status: "paid"},
		},
		Transactions: map[string][]payments.BalanceTxn{
			"po_1": {
				{Ref: "txn_1", Type: "charge", PaymentRef: "pi_1", Amount: 10000, Fee: 110},
				{Ref: "txn_payout", Type: "payout", Amount: core.Cents(-netAmount), Fee: 0},
			},
		},
	}
	b2 := e.BillingService(testkit.Provider(fakeOK))
	result, err := recon.MatchPayout(ctx, pool, b2, org.ID, "acme", lineID, "po_1")
	if err != nil {
		t.Fatalf("MatchPayout with payout txn present: %v", err)
	}
	if result.PaymentsLinked != 1 {
		t.Fatalf("PaymentsLinked = %d, want 1", result.PaymentsLinked)
	}
}

// TestMatchExpenseRejectsFeeExpense409 covers that a "Stripe fees" expense
// MatchPayout created (linked via payouts.fee_expense_id) can't be matched
// directly to a debit line - it isn't a free-standing expense.
func TestMatchExpenseRejectsFeeExpense409(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	e.Settings(org, testkit.GSTRegistered(true))
	contactID := e.Contact(org).ID

	_, payment1 := testStripePayment(e, org, contactID, 10000, testkit.Date("2026-11-01"), "pi_1")
	_ = payment1
	arrival := testkit.Date("2026-11-03")
	netAmount := int64(10000 - 110)
	payoutLineID := testBankLineID(t, pool, org.ID, netAmount, arrival, "Stripe payout")

	fake := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_1": {Ref: "po_1", Amount: core.Cents(netAmount), ArrivalOn: arrival, Status: "paid"},
		},
		Transactions: map[string][]payments.BalanceTxn{
			"po_1": {{Ref: "txn_1", Type: "charge", PaymentRef: "pi_1", Amount: 10000, Fee: 110}},
		},
	}
	b := e.BillingService(testkit.Provider(fake))
	result, err := recon.MatchPayout(ctx, pool, b, org.ID, "acme", payoutLineID, "po_1")
	if err != nil {
		t.Fatalf("MatchPayout: %v", err)
	}

	debitLineID := testBankLineID(t, pool, org.ID, -110, testkit.Date("2026-11-03"), "Stripe fee debit, unrelated")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	err = recon.MatchExpense(ctx, tx, org.ID, debitLineID, *result.FeeExpenseID)
	if !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("MatchExpense on fee expense: err = %v, want ErrInvalidState", err)
	}
}

// TestSuggestExcludesFeeExpenseAndMatchedPayout covers two Suggest
// exclusions: a "Stripe fees" expense (linked via payouts.fee_expense_id,
// not bank_line_id) must never be offered as a debit-line suggestion, and a
// payout already matched to a bank line must not be offered again for a
// different line with the same amount.
func TestSuggestExcludesFeeExpenseAndMatchedPayout(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	e.Settings(org, testkit.GSTRegistered(true))
	contactID := e.Contact(org).ID

	_, payment1 := testStripePayment(e, org, contactID, 10000, testkit.Date("2026-11-01"), "pi_1")
	_ = payment1
	arrival := testkit.Date("2026-11-03")
	netAmount := int64(10000 - 110)
	payoutLineID := testBankLineID(t, pool, org.ID, netAmount, arrival, "Stripe payout")

	fake := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_1": {Ref: "po_1", Amount: core.Cents(netAmount), ArrivalOn: arrival, Status: "paid"},
		},
		Transactions: map[string][]payments.BalanceTxn{
			"po_1": {{Ref: "txn_1", Type: "charge", PaymentRef: "pi_1", Amount: 10000, Fee: 110}},
		},
	}
	b := e.BillingService(testkit.Provider(fake))
	result, err := recon.MatchPayout(ctx, pool, b, org.ID, "acme", payoutLineID, "po_1")
	if err != nil {
		t.Fatalf("MatchPayout: %v", err)
	}

	// A debit line of the same amount as the fee expense must not suggest
	// it.
	debitLineID := testBankLineID(t, pool, org.ID, -110, testkit.Date("2026-11-03"), "Unrelated debit")
	debitSuggestions, err := recon.Suggest(ctx, pool, b, org.ID, "acme", debitLineID)
	if err != nil {
		t.Fatalf("Suggest (debit): %v", err)
	}
	for _, s := range debitSuggestions {
		if s.Kind == "expense" && s.ID == *result.FeeExpenseID {
			t.Fatalf("Suggest offered the fee expense %d as a match", *result.FeeExpenseID)
		}
	}

	// A second credit line of the same amount/arrival as the now-matched
	// payout must not suggest it again.
	secondCreditLine := testBankLineID(t, pool, org.ID, netAmount, arrival, "Second Stripe payout line")
	creditSuggestions, err := recon.Suggest(ctx, pool, b, org.ID, "acme", secondCreditLine)
	if err != nil {
		t.Fatalf("Suggest (credit): %v", err)
	}
	for _, s := range creditSuggestions {
		if s.Kind == "payout" && s.Ref == "po_1" {
			t.Fatalf("Suggest offered already-matched payout %q again", s.Ref)
		}
	}
}

// matchFeePayout records a $100 webhook payment and matches a payout for it
// that carries a $1.10 fee, returning the payout's bank line and the "Stripe
// fees" expense MatchPayout booked.
func matchFeePayout(e *testkit.Env, org core.Org) (payoutLineID, feeExpenseID int64) {
	e.T.Helper()
	contactID := e.Contact(org).ID
	testStripePayment(e, org, contactID, 10000, testkit.Date("2026-11-01"), "pi_fee")
	arrival := testkit.Date("2026-11-03")
	payoutLineID = testBankLineID(e.T, e.Pool, org.ID, 10000-110, arrival, "Stripe payout")
	fake := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_fee": {Ref: "po_fee", Amount: 10000 - 110, ArrivalOn: arrival, Status: "paid"},
		},
		Transactions: map[string][]payments.BalanceTxn{
			"po_fee": {{Ref: "txn_fee", Type: "charge", PaymentRef: "pi_fee", Amount: 10000, Fee: 110}},
		},
	}
	result, err := recon.MatchPayout(e.Ctx, e.Pool, e.BillingService(testkit.Provider(fake)), org.ID, "acme", payoutLineID, "po_fee")
	if err != nil {
		e.T.Fatalf("MatchPayout: %v", err)
	}
	if result.FeeExpenseID == nil {
		e.T.Fatalf("FeeExpenseID = nil, want set")
	}
	return payoutLineID, *result.FeeExpenseID
}

// wantPublicConflict fails t unless err is a 409 core.PublicError whose
// message contains want.
func wantPublicConflict(t *testing.T, err error, want string) {
	t.Helper()
	var pub *core.PublicError
	if !errors.Is(err, core.ErrInvalidState) || !errors.As(err, &pub) {
		t.Fatalf("err = %v, want a core.PublicError wrapping ErrInvalidState", err)
	}
	if !strings.Contains(pub.Msg, want) {
		t.Fatalf("message = %q, want it to contain %q", pub.Msg, want)
	}
}

// TestMatchPayoutPublicConflicts covers that each explanatory 409 from
// MatchPayout reaches the client with its reason (core.PublicError), not
// the generic "invalid state" text.
func TestMatchPayoutPublicConflicts(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org).ID
	testStripePayment(e, org, contactID, 10000, testkit.Date("2026-11-01"), "pi_1")
	arrival := testkit.Date("2026-11-05")
	fake := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_1": {Ref: "po_1", Amount: 10000, ArrivalOn: arrival, Status: "paid"},
		},
		Transactions: map[string][]payments.BalanceTxn{
			"po_1": {{Ref: "txn_1", Type: "charge", PaymentRef: "pi_1", Amount: 10000}},
		},
	}
	b := e.BillingService(testkit.Provider(fake))

	mismatch := testBankLineID(t, pool, org.ID, 9000, arrival, "Stripe payout")
	_, err := recon.MatchPayout(ctx, pool, b, org.ID, "acme", mismatch, "po_1")
	wantPublicConflict(t, err, "payout amount $100.00 does not match the bank line amount $90.00")

	late := testBankLineID(t, pool, org.ID, 10000, testkit.Date("2026-11-20"), "Stripe payout")
	_, err = recon.MatchPayout(ctx, pool, b, org.ID, "acme", late, "po_1")
	wantPublicConflict(t, err, "more than 5 days")

	ignored := testBankLineID(t, pool, org.ID, 10000, arrival, "Stripe payout")
	if err := module.RunInTx(ctx, pool, func(tx pgx.Tx) error {
		return recon.Ignore(ctx, tx, org.ID, ignored, "other")
	}); err != nil {
		t.Fatalf("Ignore: %v", err)
	}
	_, err = recon.MatchPayout(ctx, pool, b, org.ID, "acme", ignored, "po_1")
	wantPublicConflict(t, err, "bank line is already ignored")
}

// TestSuggestOnlyPaidPayouts covers that a payout still in transit (or
// otherwise not "paid") is never offered as a suggestion, since
// MatchPayout would only reject it.
func TestSuggestOnlyPaidPayouts(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	arrival := testkit.Date("2026-11-05")
	lineID := testBankLineID(t, pool, org.ID, 10000, arrival, "Stripe payout")
	fake := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_paid":    {Ref: "po_paid", Amount: 10000, ArrivalOn: arrival, Status: "paid"},
			"po_transit": {Ref: "po_transit", Amount: 10000, ArrivalOn: arrival, Status: "in_transit"},
		},
	}
	suggestions, err := recon.Suggest(ctx, pool, e.BillingService(testkit.Provider(fake)), org.ID, "acme", lineID)
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	var refs []string
	for _, s := range suggestions {
		if s.Kind == "payout" {
			refs = append(refs, s.Ref)
		}
	}
	if len(refs) != 1 || refs[0] != "po_paid" {
		t.Fatalf("payout suggestions = %v, want [po_paid]", refs)
	}
}
