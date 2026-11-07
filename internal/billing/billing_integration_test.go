//go:build integration

package billing_test

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
)

func testOrg(t *testing.T, pool *pgxpool.Pool, slug string) core.Org {
	t.Helper()
	org, err := core.CreateOrg(context.Background(), pool, slug, slug+" org")
	if err != nil {
		t.Fatalf("CreateOrg(%s): %v", slug, err)
	}
	return org
}

func testContact(t *testing.T, pool *pgxpool.Pool, orgID int64) int64 {
	t.Helper()
	c, err := core.CreateContact(context.Background(), pool, orgID, core.Contact{Kind: "tenant", Name: "Test Tenant"})
	if err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	return c.ID
}

// testDraft creates a one-line draft invoice whose line totals to exactly
// totalCents (GST-free), with the given source and a 14-day term.
func testDraft(t *testing.T, pool *pgxpool.Pool, orgID, contactID int64, source string, totalCents int64) int64 {
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

// noProvider is a billing.Service.Payments func that reports every org has
// no payment provider configured, so Issue's best-effort send is a no-op.
func noProvider(string) (payments.Provider, error) {
	return nil, payments.ErrNoProvider
}

func TestServiceSendRejectsVoidedAfterInitialRead(t *testing.T) {
	testSendRejectsChangedState(t, "void")
}

func TestServiceSendRejectsPaidAfterInitialRead(t *testing.T) {
	testSendRejectsChangedState(t, "paid")
}

func testSendRejectsChangedState(t *testing.T, status string) {
	t.Helper()
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	contactID := testContact(t, pool, org.ID)
	id := testDraft(t, pool, org.ID, contactID, "manual", 1000)
	svc := billing.New(pool, func(string) string { return "" }, noProvider, time.UTC)
	if _, err := svc.Issue(ctx, org.ID, org.Slug, id); err != nil {
		t.Fatal(err)
	}
	fake := &payments.Fake{}
	// The resolver runs after Send's initial read but before its row lock.
	svc.Payments = func(string) (payments.Provider, error) {
		if status == "void" {
			voider := billing.New(pool, func(string) string { return "" }, noProvider, time.UTC)
			if _, err := voider.Void(ctx, org.ID, org.Slug, id); err != nil {
				t.Fatal(err)
			}
		} else {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, _, err := core.RecordPayment(ctx, tx, org.ID, core.NewPayment{
				InvoiceID: id, Amount: 1000, PaidOn: time.Now(), Method: "bank_transfer",
			}); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
		return fake, nil
	}
	if _, err := svc.Send(ctx, org.ID, org.Slug, id); !errors.Is(err, core.ErrInvalidState) {
		t.Errorf("Send error = %v, want invalid state", err)
	}
	inv, err := core.GetInvoice(ctx, pool, org.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Status != status || inv.ProviderRef != "" {
		t.Errorf("invoice unexpectedly sent: status=%s provider_ref=%q", inv.Status, inv.ProviderRef)
	}
	if len(fake.Calls()) != 0 || len(fake.Customers()) != 0 {
		t.Error("provider side effects occurred after invoice stopped being payable")
	}
}

func TestServiceIssueEnforcesPayOptions(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	contactID := testContact(t, pool, org.ID)

	// A BSB and account number alone is only one pay option (bank
	// transfer); a rent invoice needs at least two.
	if _, err := pool.Exec(ctx, "UPDATE orgs SET bank_bsb=$1, bank_account=$2 WHERE id=$3", "062-000", "12345678", org.ID); err != nil {
		t.Fatalf("setting org bank details: %v", err)
	}

	draftID := testDraft(t, pool, org.ID, contactID, "rent", 1000)

	svc := &billing.Service{Pool: pool, GetEnv: func(string) string { return "" }, Payments: noProvider, Location: time.UTC}
	_, err := svc.Issue(ctx, org.ID, "acme", draftID)
	if !errors.Is(err, billing.ErrTooFewPayOptions) {
		t.Fatalf("Issue error = %v, want ErrTooFewPayOptions", err)
	}

	inv, err := core.GetInvoice(ctx, pool, org.ID, draftID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "draft" {
		t.Fatalf("status = %s, want draft (rejected issue must not change it)", inv.Status)
	}
	if inv.Number != nil {
		t.Fatalf("number = %v, want nil (rejected issue must not burn a number)", *inv.Number)
	}
}

func TestPayOptionsStripeNeedsBothKeys(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")

	getenv := func(key string) string {
		if key == "SHIGOTO_STRIPE_ACME_SECRET_KEY" {
			return "sk_test_dummy"
		}
		return ""
	}
	svc := &billing.Service{Pool: pool, GetEnv: getenv, Payments: noProvider, Location: time.UTC}

	opts, err := svc.PayOptions(ctx, pool, org.ID)
	if err != nil {
		t.Fatalf("PayOptions: %v", err)
	}
	for _, o := range opts {
		if o == "stripe" {
			t.Fatalf("pay options = %v, want no stripe with only the secret key set", opts)
		}
	}
}

func TestServiceIssueNoActorAudits(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	contactID := testContact(t, pool, org.ID)
	draftID := testDraft(t, pool, org.ID, contactID, "manual", 1000)

	svc := &billing.Service{Pool: pool, GetEnv: func(string) string { return "" }, Payments: noProvider, Location: time.UTC}
	if _, err := svc.Issue(ctx, org.ID, "acme", draftID); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM audit_log WHERE org_id=$1 AND action='issue' AND actor_user_id IS NULL", org.ID,
	).Scan(&n); err != nil {
		t.Fatalf("counting audit_log: %v", err)
	}
	if n != 1 {
		t.Fatalf("audit_log rows with actor_user_id IS NULL = %d, want 1", n)
	}
}

// TestInvoiceJSONOutstandingOnlyForOpen covers ruling 47: only an open
// invoice owes anything. A draft (never issued) and a void invoice report
// 0 outstanding, while an open, partly paid one reports total - paid.
func TestInvoiceJSONOutstandingOnlyForOpen(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	contactID := testContact(t, pool, org.ID)
	svc := &billing.Service{Pool: pool, GetEnv: func(string) string { return "" }, Payments: noProvider, Location: time.UTC}

	outstanding := func(id int64) int64 {
		t.Helper()
		inv, err := core.GetInvoice(ctx, pool, org.ID, id)
		if err != nil {
			t.Fatalf("GetInvoice: %v", err)
		}
		resp, err := svc.InvoiceJSON(ctx, pool, inv)
		if err != nil {
			t.Fatalf("InvoiceJSON: %v", err)
		}
		return resp.Outstanding
	}

	draftID := testDraft(t, pool, org.ID, contactID, "manual", 1000)
	if got := outstanding(draftID); got != 0 {
		t.Errorf("draft outstanding = %d, want 0", got)
	}

	voidID := testDraft(t, pool, org.ID, contactID, "manual", 2000)
	if _, err := svc.Issue(ctx, org.ID, "acme", voidID); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := svc.Void(ctx, org.ID, "acme", voidID); err != nil {
		t.Fatalf("Void: %v", err)
	}
	if got := outstanding(voidID); got != 0 {
		t.Errorf("void outstanding = %d, want 0", got)
	}

	openID := testDraft(t, pool, org.ID, contactID, "manual", 3000)
	if _, err := svc.Issue(ctx, org.ID, "acme", openID); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, _, err := core.RecordPayment(ctx, pool, org.ID, core.NewPayment{
		InvoiceID: openID, Amount: 1200, PaidOn: time.Now(), Method: "bank_transfer",
	}); err != nil {
		t.Fatalf("RecordPayment: %v", err)
	}
	if got := outstanding(openID); got != 1800 {
		t.Errorf("open partly-paid outstanding = %d, want 1800", got)
	}
}
