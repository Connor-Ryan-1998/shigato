//go:build integration

// Package core_test exercises core's invoice/payment flows, plus one test
// that goes through the real HTTP handlers (for the cross-org 404 and the
// QLD pay-options rule), so it needs to import httpapi and auth alongside
// core without an import cycle.
package core_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/config"
	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/httpapi"
	"shigoto/internal/payments"
)

// sessionCookieName mirrors the unexported constant of the same name in
// package auth (internal/auth/session.go); there's no exported way to get
// it from here.
const sessionCookieName = "shigoto_session"

func createOrg(t *testing.T, pool *pgxpool.Pool, slug string) core.Org {
	t.Helper()
	org, err := core.CreateOrg(context.Background(), pool, slug, slug+" org")
	if err != nil {
		t.Fatalf("CreateOrg(%s): %v", slug, err)
	}
	return org
}

func createContact(t *testing.T, pool *pgxpool.Pool, orgID int64) int64 {
	t.Helper()
	c, err := core.CreateContact(context.Background(), pool, orgID, core.Contact{Kind: "client", Name: "Test Contact"})
	if err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	return c.ID
}

func createUser(t *testing.T, pool *pgxpool.Pool, email, password string) int64 {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	var userID int64
	err = pool.QueryRow(context.Background(),
		"INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id",
		email, email, hash,
	).Scan(&userID)
	if err != nil {
		t.Fatalf("inserting user %s: %v", email, err)
	}
	return userID
}

func addMembership(t *testing.T, pool *pgxpool.Pool, userID, orgID int64, role auth.Role) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		"INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, $3)",
		userID, orgID, string(role),
	)
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}
}

// createDraft creates a one-line draft invoice whose line totals to
// exactly totalCents (GST-free, so subtotal == total), with the given
// source and a 14-day term. It returns the draft's id.
func createDraft(t *testing.T, pool *pgxpool.Pool, orgID, contactID int64, source string, totalCents int64) int64 {
	t.Helper()
	inv, err := core.CreateDraftInvoice(context.Background(), pool, orgID, core.NewInvoice{
		ContactID: contactID,
		Source:    source,
		DueInDays: 14,
		Lines: []core.NewLine{
			{Description: "line", QuantityMilli: 1000, UnitAmount: core.Cents(totalCents), GSTCode: "N-T"},
		},
	})
	if err != nil {
		t.Fatalf("CreateDraftInvoice: %v", err)
	}
	return inv.ID
}

// createIssuedInvoice creates and issues a draft invoice totalling
// totalCents, returning its id.
func createIssuedInvoice(t *testing.T, pool *pgxpool.Pool, orgID, contactID int64, totalCents int64) int64 {
	t.Helper()
	draftID := createDraft(t, pool, orgID, contactID, "manual", totalCents)

	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	inv, err := core.IssueInvoice(ctx, tx, orgID, draftID, time.Now())
	if err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return inv.ID
}

func TestIssueNumbersSequentialConcurrent(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := createOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)

	const n = 10
	numbers := make([]int, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		draftID := createDraft(t, pool, org.ID, contactID, "manual", 1000)
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
	pool := db.TestPool(t)
	ctx := context.Background()
	org := createOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)

	draft1 := createDraft(t, pool, org.ID, contactID, "manual", 1000)
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

	draft2 := createDraft(t, pool, org.ID, contactID, "manual", 1000)
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
	pool := db.TestPool(t)
	ctx := context.Background()
	org := createOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)
	invID := createIssuedInvoice(t, pool, org.ID, contactID, 10000)

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
	pool := db.TestPool(t)
	ctx := context.Background()
	org := createOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)
	invID := createIssuedInvoice(t, pool, org.ID, contactID, 10000)

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
	pool := db.TestPool(t)
	ctx := context.Background()
	org := createOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)
	inv1 := createIssuedInvoice(t, pool, org.ID, contactID, 10000)
	inv2 := createIssuedInvoice(t, pool, org.ID, contactID, 10000)

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
	pool := db.TestPool(t)
	ctx := context.Background()
	org := createOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)
	invID := createIssuedInvoice(t, pool, org.ID, contactID, 1000)

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
	pool := db.TestPool(t)
	ctx := context.Background()
	org := createOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)
	draftID := createDraft(t, pool, org.ID, contactID, "manual", 1000)

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
	pool := db.TestPool(t)
	ctx := context.Background()
	org := createOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)

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
	manualDraftID := createDraft(t, pool, org.ID, contactID, "manual", 1000)
	tx3, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx3.Rollback(ctx)
	manual, err := core.IssueInvoice(ctx, tx3, org.ID, manualDraftID, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	wantDue := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC) // 14-day term from createDraft
	if manual.DueDate == nil || !manual.DueDate.Equal(wantDue) {
		t.Fatalf("due_date = %v, want %v (due_in_days unaffected by absent DueOn)", manual.DueDate, wantDue)
	}
}

// TestCreateDraftInvoiceStoresPropertyID verifies a NewLine's PropertyID is
// persisted on its invoice_lines row, so callers (e.g. property's rent run)
// can attribute rent/charge lines to the property they're for.
func TestCreateDraftInvoiceStoresPropertyID(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := createOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)

	var propertyID int64
	if err := pool.QueryRow(ctx,
		"INSERT INTO properties (org_id, name) VALUES ($1, $2) RETURNING id",
		org.ID, "1 Test St",
	).Scan(&propertyID); err != nil {
		t.Fatalf("inserting property: %v", err)
	}

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

func TestCrossOrgInvoice404(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	orgA := createOrg(t, pool, "org-a")
	orgB := createOrg(t, pool, "org-b")
	contactA := createContact(t, pool, orgA.ID)
	invID := createIssuedInvoice(t, pool, orgA.ID, contactA, 1000)

	userID := createUser(t, pool, "owner-b@example.com", "correct-password")
	addMembership(t, pool, userID, orgB.ID, auth.Owner)

	a := auth.New(pool, false)
	handler := httpapi.New(httpapi.Deps{Pool: pool, Auth: a, Cfg: config.Config{Modules: map[string]bool{}}})

	token, err := auth.Login(ctx, pool, "owner-b@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/orgs/org-b/invoices/%d", invID), nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

func TestRentInvoiceNeedsTwoPayOptions(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := createOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)
	userID := createUser(t, pool, "owner@example.com", "correct-password")
	addMembership(t, pool, userID, org.ID, auth.Owner)

	a := auth.New(pool, false)
	// getenv returns "" for everything: a nil GetEnv would fall back to
	// os.Getenv, and a Stripe env var set in the test runner's own shell
	// would silently change which pay-options branch this test exercises.
	getenv := func(string) string { return "" }
	handler := httpapi.New(httpapi.Deps{Pool: pool, Auth: a, Cfg: config.Config{Modules: map[string]bool{}}, GetEnv: getenv})

	token, err := auth.Login(ctx, pool, "owner@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	issueDraft := func(source string) *httptest.ResponseRecorder {
		draftID := createDraft(t, pool, org.ID, contactID, source, 1000)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/issue", draftID), nil)
		req.Header.Set("X-Shigoto-CSRF", "1")
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	// No bank, no PayID, no Stripe config: fewer than 2 pay options.
	if rr := issueDraft("rent"); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("issue with no pay options: status = %d, want 422, body=%s", rr.Code, rr.Body.String())
	}

	// Bank details plus a PayID: 2 pay options.
	if _, err := pool.Exec(ctx, "UPDATE orgs SET bank_bsb=$1, bank_account=$2, payid=$3 WHERE id=$4",
		"062-000", "12345678", "owner@payid.example", org.ID); err != nil {
		t.Fatalf("updating org bank details: %v", err)
	}

	if rr := issueDraft("rent"); rr.Code != http.StatusOK {
		t.Fatalf("issue with bank + PayID: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	// Drop back to one pay option (no bank/PayID), but add Stripe keys:
	// bank details + Stripe keys configured should also clear the bar.
	if _, err := pool.Exec(ctx, "UPDATE orgs SET bank_bsb=$1, bank_account=$2, payid=NULL WHERE id=$3",
		"062-000", "12345678", org.ID); err != nil {
		t.Fatalf("clearing org payid: %v", err)
	}
	getenv = func(key string) string {
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
	// goes through Deps.Payments, which is overridden here to a Fake so
	// this test makes no real network call to Stripe.
	handler = httpapi.New(httpapi.Deps{
		Pool: pool, Auth: a, Cfg: config.Config{Modules: map[string]bool{}}, GetEnv: getenv,
		Payments: func(string) (payments.Provider, error) { return &payments.Fake{Secret: "whsec_test_dummy"}, nil },
	})

	if rr := issueDraft("rent"); rr.Code != http.StatusOK {
		t.Fatalf("issue with bank details + Stripe keys configured: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
}

// TestInvoiceValidation422 covers the invalid inputs creating and issuing
// an invoice must reject with 422 rather than persist.
func TestInvoiceValidation422(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := createOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)
	userID := createUser(t, pool, "owner@example.com", "correct-password")
	addMembership(t, pool, userID, org.ID, auth.Owner)

	getenv := func(string) string { return "" }
	handler := httpapi.New(httpapi.Deps{Pool: pool, Auth: auth.New(pool, false), Cfg: config.Config{Modules: map[string]bool{}}, GetEnv: getenv})
	token, err := auth.Login(ctx, pool, "owner@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Shigoto-CSRF", "1")
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	okLine := `{"description":"x","quantity_milli":1000,"unit_amount":100,"gst_code":"GST"}`
	cases := []struct {
		name, lines string
		dueInDays   int
	}{
		{"empty lines", ``, 14},
		{"zero quantity", `{"description":"x","quantity_milli":0,"unit_amount":100,"gst_code":"GST"}`, 14},
		{"negative quantity", `{"description":"x","quantity_milli":-1,"unit_amount":100,"gst_code":"GST"}`, 14},
		{"negative unit_amount", `{"description":"x","quantity_milli":1000,"unit_amount":-1,"gst_code":"GST"}`, 14},
		{"bad gst_code", `{"description":"x","quantity_milli":1000,"unit_amount":100,"gst_code":"VAT"}`, 14},
		{"negative due_in_days", okLine, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"contact_id":%d,"source":"manual","due_in_days":%d,"lines":[%s]}`, contactID, tc.dueInDays, tc.lines)
			if rr := post("/api/v1/orgs/acme/invoices", body); rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422, body=%s", rr.Code, rr.Body.String())
			}
		})
	}

	// A zero-total draft can be created but not issued: there's nothing to
	// collect, and a provider would reject (or zero-pay) it.
	t.Run("issue zero total", func(t *testing.T) {
		draftID := createDraft(t, pool, org.ID, contactID, "manual", 0)
		if rr := post(fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/issue", draftID), ""); rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422, body=%s", rr.Code, rr.Body.String())
		}
		inv, err := core.GetInvoice(ctx, pool, org.ID, draftID)
		if err != nil {
			t.Fatalf("GetInvoice: %v", err)
		}
		if inv.Status != "draft" || inv.Number != nil {
			t.Fatalf("status=%s number=%v, want draft with no number", inv.Status, inv.Number)
		}
	})
}

// TestCreateDraftRejectsHugeQuantity covers CreateDraftInvoice's upper
// quantity_milli bound: one over 10,000,000 is rejected rather than
// risking LineAmount's multiplication overflowing.
func TestCreateDraftRejectsHugeQuantity(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := createOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)

	_, err := core.CreateDraftInvoice(ctx, pool, org.ID, core.NewInvoice{
		ContactID: contactID,
		Source:    "manual",
		DueInDays: 14,
		Lines: []core.NewLine{
			{Description: "line", QuantityMilli: 10_000_001, UnitAmount: 100, GSTCode: "N-T"},
		},
	})
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

// TestCreateDraftAcceptsMaxQuantityAndUnitAmount covers the max corner of
// both bounds at once (quantity_milli 10,000,000, unit_amount
// 100,000,000,000): LineAmount's intermediate quantityMilli*unit
// multiplication is exactly 1e18, within int64 range, so it must compute
// the correct amount rather than silently wrapping.
func TestCreateDraftAcceptsMaxQuantityAndUnitAmount(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := createOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)

	const quantityMilli = 10_000_000
	const unitAmount = 100_000_000_000
	// LineAmount = quantityMilli * unit / 1000 (exact, no rounding needed).
	const wantAmount = quantityMilli * unitAmount / 1000

	inv, err := core.CreateDraftInvoice(ctx, pool, org.ID, core.NewInvoice{
		ContactID: contactID,
		Source:    "manual",
		DueInDays: 14,
		Lines: []core.NewLine{
			{Description: "line", QuantityMilli: quantityMilli, UnitAmount: unitAmount, GSTCode: "N-T"},
		},
	})
	if err != nil {
		t.Fatalf("CreateDraftInvoice: %v", err)
	}
	if len(inv.Lines) != 1 || inv.Lines[0].Amount != core.Cents(wantAmount) {
		t.Fatalf("line amount = %v, want %d (no overflow)", inv.Lines, wantAmount)
	}
	if inv.Total != core.Cents(wantAmount) {
		t.Fatalf("total = %d, want %d", inv.Total, wantAmount)
	}
}

// TestCreateDraftRejectsGSTWhenUnregistered covers the GST-registration
// guard: a manual invoice line coded GST in an org that isn't
// GST-registered is rejected, closing the gap rent/timesheet runs don't
// have (they always pick FRE/INP/N-T based on settings.GSTRegistered).
func TestCreateDraftRejectsGSTWhenUnregistered(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := createOrg(t, pool, "acme") // gst_registered defaults to false
	contactID := createContact(t, pool, org.ID)

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
	pool := db.TestPool(t)
	ctx := context.Background()
	org := createOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)
	invID := createIssuedInvoice(t, pool, org.ID, contactID, 10000)

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
