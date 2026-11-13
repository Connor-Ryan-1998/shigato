//go:build integration

// Package core_test exercises core's invoice/payment flows, plus tests that
// go through the real HTTP handlers (zero-total issue and Stripe as a
// pay option) via testkit, which imports httpapi and so needs
// an external test package to avoid an import cycle.
package core_test

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/payments"
	"shigoto/internal/testkit"
)

func TestIssueNumbersSequentialConcurrent(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org).ID

	const n = 10
	numbers := make([]int, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		draftID := e.Invoice(org, contactID).ID
		wg.Add(1)
		go func(i, draftID int64) {
			defer wg.Done()
			tx, err := pool.Begin(ctx)
			if err != nil {
				errs[i] = err
				return
			}
			inv, err := core.IssueInvoice(ctx, tx, org.ID, draftID, time.Now())
			if err != nil {
				_ = tx.Rollback(ctx)
				errs[i] = err
				return
			}
			if err := tx.Commit(ctx); err != nil {
				errs[i] = err
				return
			}
			if inv.Number == nil {
				errs[i] = fmt.Errorf("issued invoice has no number")
				return
			}
			numbers[i] = *inv.Number
		}(int64(i), draftID)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	sort.Ints(numbers)
	for i, got := range numbers {
		if got != i+1 {
			t.Fatalf("numbers = %v, want {1..10}", numbers)
		}
	}
}

func TestIssueRollbackLeavesNoGap(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org).ID

	draft1 := e.Invoice(org, contactID).ID
	tx1, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := core.IssueInvoice(ctx, tx1, org.ID, draft1, time.Now()); err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	if err := tx1.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	draft2 := e.Invoice(org, contactID).ID
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx2.Rollback(ctx)
	inv2, err := core.IssueInvoice(ctx, tx2, org.ID, draft2, time.Now())
	if err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if inv2.Number == nil || *inv2.Number != 1 {
		t.Fatalf("number = %v, want 1", inv2.Number)
	}
}

func TestPartialThenFullPayment(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org).ID
	invID := e.Invoice(org, contactID, testkit.Amount(10000), testkit.Issued(time.Now())).ID

	pay := func(amount int64) core.Invoice {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		_, _, err = core.RecordPayment(ctx, tx, org.ID, core.NewPayment{
			InvoiceID: invID, Amount: core.Cents(amount), PaidOn: time.Now(), Method: "cash",
		})
		if err != nil {
			t.Fatalf("RecordPayment(%d): %v", amount, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		inv, err := core.GetInvoice(ctx, pool, org.ID, invID)
		if err != nil {
			t.Fatalf("GetInvoice: %v", err)
		}
		return inv
	}

	if inv := pay(4000); inv.Status != "open" {
		t.Fatalf("after 4000: status = %s, want open", inv.Status)
	}
	if inv := pay(6000); inv.Status != "paid" {
		t.Fatalf("after 6000 more: status = %s, want paid", inv.Status)
	}
	if inv := pay(500); inv.Status != "paid" {
		t.Fatalf("after 500 more: status = %s, want paid", inv.Status)
	}

	var sum int64
	if err := pool.QueryRow(ctx, "SELECT coalesce(sum(amount),0) FROM payments WHERE org_id=$1 AND invoice_id=$2", org.ID, invID).Scan(&sum); err != nil {
		t.Fatalf("summing payments: %v", err)
	}
	if sum != 10500 {
		t.Fatalf("sum of payments = %d, want 10500", sum)
	}
}

func TestRecordPaymentIdempotentProviderRef(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org).ID
	invID := e.Invoice(org, contactID, testkit.Amount(10000), testkit.Issued(time.Now())).ID

	record := func() (core.Payment, bool) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		p, created, err := core.RecordPayment(ctx, tx, org.ID, core.NewPayment{
			InvoiceID: invID, Amount: 5000, PaidOn: time.Now(), Method: "bank_transfer", ProviderRef: "ref-1",
		})
		if err != nil {
			t.Fatalf("RecordPayment: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return p, created
	}

	p1, created1 := record()
	if !created1 {
		t.Fatal("first call: created = false, want true")
	}

	p2, created2 := record()
	if created2 {
		t.Fatal("second call: created = true, want false")
	}
	if p2.ID != p1.ID {
		t.Fatalf("second call returned payment %d, want %d (the first one)", p2.ID, p1.ID)
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM payments WHERE org_id=$1 AND invoice_id=$2", org.ID, invID).Scan(&count); err != nil {
		t.Fatalf("counting payments: %v", err)
	}
	if count != 1 {
		t.Fatalf("payment rows = %d, want 1", count)
	}
}

func TestRecordPaymentConflictingProviderRef(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org).ID
	inv1 := e.Invoice(org, contactID, testkit.Amount(10000), testkit.Issued(time.Now())).ID
	inv2 := e.Invoice(org, contactID, testkit.Amount(10000), testkit.Issued(time.Now())).ID

	record := func(invoiceID, amount int64) error {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		_, _, err = core.RecordPayment(ctx, tx, org.ID, core.NewPayment{
			InvoiceID: invoiceID, Amount: core.Cents(amount), PaidOn: time.Now(), Method: "stripe", ProviderRef: "pi_shared",
		})
		if err == nil {
			if cerr := tx.Commit(ctx); cerr != nil {
				t.Fatalf("commit: %v", cerr)
			}
		}
		return err
	}

	if err := record(inv1, 5000); err != nil {
		t.Fatalf("first RecordPayment: %v", err)
	}

	// Same provider_ref, different invoice: a genuine conflict, not a
	// retry of the same payment.
	if err := record(inv2, 5000); !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("different invoice: err = %v, want ErrInvalidState", err)
	}

	// Same provider_ref and invoice, different amount: also a conflict.
	if err := record(inv1, 6000); !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("different amount: err = %v, want ErrInvalidState", err)
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM payments WHERE org_id=$1 AND provider_ref=$2", org.ID, "pi_shared").Scan(&count); err != nil {
		t.Fatalf("counting payments: %v", err)
	}
	if count != 1 {
		t.Fatalf("payment rows = %d, want 1", count)
	}
}

func TestIssueNonDraftInvalid(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org).ID
	invID := e.Invoice(org, contactID, testkit.Issued(time.Now())).ID

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	_, err = core.IssueInvoice(ctx, tx, org.ID, invID, time.Now())
	if !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("err = %v, want ErrInvalidState", err)
	}
}

func TestIssueInvoiceUsesCallersCalendarDate(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org).ID
	draftID := e.Invoice(org, contactID).ID

	// 08:00 in +10:00 (Queensland) is 22:00 the previous day in UTC. A
	// naive UTC-truncation of "today" would wrongly give issue_date
	// 2026-11-04.
	today, err := time.Parse(time.RFC3339, "2026-11-05T08:00:00+10:00")
	if err != nil {
		t.Fatalf("parsing today: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	inv, err := core.IssueInvoice(ctx, tx, org.ID, draftID, today)
	if err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if inv.IssueDate == nil || inv.IssueDate.Format(time.DateOnly) != "2026-11-05" {
		t.Fatalf("issue_date = %v, want 2026-11-05", inv.IssueDate)
	}
}

// TestIssueInvoiceDueOnPinsDueDate verifies NewInvoice.DueOn (e.g. a rent
// invoice's period start) drives IssueInvoice's due_date, including when
// issuing happens days after the draft was created - which due_in_days
// alone would get wrong, since it's measured from issue time.
func TestIssueInvoiceDueOnPinsDueDate(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org).ID

	periodStart := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)

	newRentDraft := func() int64 {
		inv, err := core.CreateDraftInvoice(ctx, pool, org.ID, core.NewInvoice{
			ContactID: contactID,
			Source:    "rent",
			DueInDays: 0,
			DueOn:     &periodStart,
			Lines: []core.NewLine{
				{Description: "Rent", QuantityMilli: 1000, UnitAmount: core.Cents(50000), GSTCode: "INP"},
			},
		})
		if err != nil {
			t.Fatalf("CreateDraftInvoice: %v", err)
		}
		return inv.ID
	}

	// Created 7 days before the period start, then issued 5 days later
	// (still 2 days before the period start, e.g. a late manual retry of
	// a failed auto-issue): due_date must be the period start, not
	// issue_date+0.
	draftID := newRentDraft()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	issued, err := core.IssueInvoice(ctx, tx, org.ID, draftID, periodStart.AddDate(0, 0, -2))
	if err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if issued.DueDate == nil || !issued.DueDate.Equal(periodStart) {
		t.Fatalf("due_date = %v, want period start %v", issued.DueDate, periodStart)
	}

	// Issued after the period start (a very late retry): due_date is the
	// issue date, not the now-past period start.
	draftID2 := newRentDraft()
	lateIssue := periodStart.AddDate(0, 0, 5)
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	issued2, err := core.IssueInvoice(ctx, tx2, org.ID, draftID2, lateIssue)
	if err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if issued2.DueDate == nil || !issued2.DueDate.Equal(lateIssue) {
		t.Fatalf("due_date = %v, want issue date %v (due_on already past)", issued2.DueDate, lateIssue)
	}

	// A non-rent invoice with no DueOn is unaffected: due_date is still
	// issue_date + due_in_days.
	manualDraftID := e.Invoice(org, contactID).ID
	tx3, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx3.Rollback(ctx)
	manual, err := core.IssueInvoice(ctx, tx3, org.ID, manualDraftID, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	wantDue := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC) // 14-day term from testkit.Invoice
	if manual.DueDate == nil || !manual.DueDate.Equal(wantDue) {
		t.Fatalf("due_date = %v, want %v (due_in_days unaffected by absent DueOn)", manual.DueDate, wantDue)
	}
}

// TestCreateDraftInvoiceStoresPropertyID verifies a NewLine's PropertyID is
// persisted on its invoice_lines row, so callers (e.g. property's rent run)
// can attribute rent/charge lines to the property they're for.
func TestCreateDraftInvoiceStoresPropertyID(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org).ID

	propertyID := e.Property(org).ID

	inv, err := core.CreateDraftInvoice(ctx, pool, org.ID, core.NewInvoice{
		ContactID: contactID,
		Source:    "rent",
		DueInDays: 7,
		Lines: []core.NewLine{
			{Description: "Rent", QuantityMilli: 1000, UnitAmount: core.Cents(50000), GSTCode: "INP", PropertyID: &propertyID},
		},
	})
	if err != nil {
		t.Fatalf("CreateDraftInvoice: %v", err)
	}

	var gotPropertyID *int64
	if err := pool.QueryRow(ctx,
		"SELECT property_id FROM invoice_lines WHERE invoice_id = $1", inv.ID,
	).Scan(&gotPropertyID); err != nil {
		t.Fatalf("selecting property_id: %v", err)
	}
	if gotPropertyID == nil || *gotPropertyID != propertyID {
		t.Fatalf("property_id = %v, want %d", gotPropertyID, propertyID)
	}
}

// TestRentInvoiceStripeCountsAsPayOption checks that configured Stripe
// keys count toward a rent invoice's two pay options: bank details plus
// Stripe clear the bar. The no-options 422 and the bank-plus-PayID 200 are
// cases in internal/httpapi/testdata/cases/invoices.yaml.
func TestRentInvoiceStripeCountsAsPayOption(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org).ID
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)

	// Bank details only: one pay option without Stripe.
	if _, err := pool.Exec(ctx, "UPDATE orgs SET bank_bsb=$1, bank_account=$2 WHERE id=$3",
		"062-000", "12345678", org.ID); err != nil {
		t.Fatalf("updating org bank details: %v", err)
	}
	getenv := func(key string) string {
		switch key {
		case "SHIGOTO_STRIPE_ACME_SECRET_KEY":
			return "sk_test_dummy"
		case "SHIGOTO_STRIPE_ACME_WEBHOOK_SECRET":
			return "whsec_test_dummy"
		default:
			return ""
		}
	}
	// payOptionsFor only needs config.StripeFor (via getenv) to see
	// Stripe credentials; the issue route's automatic send afterwards
	// goes through the handler's Payments, which is overridden here to a Fake so
	// this test makes no real network call to Stripe.
	s := e.Login(owner, e.Handler(
		testkit.GetEnv(getenv),
		testkit.Payments(func(string) (payments.Provider, error) { return &payments.Fake{Secret: "whsec_test_dummy"}, nil }),
	))

	draftID := e.Invoice(org, contactID, testkit.Source("rent")).ID
	if rr := s.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/issue", draftID), nil); rr.Code != http.StatusOK {
		t.Fatalf("issue with bank details + Stripe keys configured: status = %d, want 200, body=%s", rr.Code, rr.Body)
	}
}

// TestIssueZeroTotalInvoice422 checks that a zero-total draft can be
// created but not issued: there's nothing to collect, and a provider would
// reject (or zero-pay) it. The create-time validation 422s are cases in
// internal/httpapi/testdata/cases/invoices.yaml.
func TestIssueZeroTotalInvoice422(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org).ID
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler())

	draftID := e.Invoice(org, contactID, testkit.Amount(0)).ID
	if rr := s.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/issue", draftID), nil); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body=%s", rr.Code, rr.Body)
	}
	inv, err := core.GetInvoice(ctx, pool, org.ID, draftID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "draft" || inv.Number != nil {
		t.Fatalf("status=%s number=%v, want draft with no number", inv.Status, inv.Number)
	}
}

// TestCreateDraftRejectsGSTWhenUnregistered covers the GST-registration
// guard: a manual invoice line coded GST in an org that isn't
// GST-registered is rejected, closing the gap rent/timesheet runs don't
// have (they always pick FRE/INP/N-T based on settings.GSTRegistered).
func TestCreateDraftRejectsGSTWhenUnregistered(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme") // gst_registered defaults to false
	contactID := e.Contact(org).ID

	_, err := core.CreateDraftInvoice(ctx, pool, org.ID, core.NewInvoice{
		ContactID: contactID,
		Source:    "manual",
		DueInDays: 14,
		Lines: []core.NewLine{
			{Description: "line", QuantityMilli: 1000, UnitAmount: 100, GSTCode: "GST"},
		},
	})
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "org is not GST-registered") {
		t.Fatalf("err = %v, want message containing %q", err, "org is not GST-registered")
	}
}

// TestVoidRefusesInvoiceWithPayments covers core.VoidInvoice's payment
// guard directly (the httpapi-level TestVoidRefusesInvoiceWithPayments in
// package httpapi covers the same rule end to end through billing.Void and
// the HTTP response).
func TestVoidRefusesInvoiceWithPayments(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org).ID
	invID := e.Invoice(org, contactID, testkit.Amount(10000), testkit.Issued(time.Now())).ID

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// A partial payment, not the full amount: it still counts as "has
	// payments" for the void guard.
	if _, _, err := core.RecordPayment(ctx, tx, org.ID, core.NewPayment{
		InvoiceID: invID, Amount: 4000, PaidOn: time.Now(), Method: "cash",
	}); err != nil {
		t.Fatalf("RecordPayment: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx2.Rollback(ctx)
	if _, err := core.VoidInvoice(ctx, tx2, org.ID, invID); !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("VoidInvoice err = %v, want ErrInvalidState", err)
	} else if err.Error() != "invoice has payments; record a credit note instead" {
		t.Fatalf("VoidInvoice err = %q, want %q", err.Error(), "invoice has payments; record a credit note instead")
	}
	if err := tx2.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	inv, err := core.GetInvoice(ctx, pool, org.ID, invID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "open" {
		t.Fatalf("status = %s, want open (refused void must not change it)", inv.Status)
	}
}
