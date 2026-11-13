//go:build integration

package billing_test

import (
	"errors"
	"testing"
	"time"

	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/payments"
	"shigoto/internal/testkit"
)

func TestServiceSendRejectsVoidedAfterInitialRead(t *testing.T) {
	testSendRejectsChangedState(t, "void")
}

func TestServiceSendRejectsPaidAfterInitialRead(t *testing.T) {
	testSendRejectsChangedState(t, "paid")
}

func TestServiceSendRejectsPartiallyPaidAfterInitialRead(t *testing.T) {
	testSendRejectsChangedState(t, "open")
}

func testSendRejectsChangedState(t *testing.T, status string) {
	t.Helper()
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID
	id := e.Invoice(org, contactID, testkit.Source("manual"), testkit.Amount(1000)).ID
	svc := e.BillingService()
	if _, err := svc.Issue(ctx, org.ID, org.Slug, id); err != nil {
		t.Fatal(err)
	}
	fake := &payments.Fake{}
	// The resolver runs after Send's initial read but before its row lock.
	svc.Payments = func(string) (payments.Provider, error) {
		if status == "void" {
			voider := e.BillingService()
			if _, err := voider.Void(ctx, org.ID, org.Slug, id); err != nil {
				t.Fatal(err)
			}
		} else {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			amount := core.Cents(1000)
			if status == "open" {
				amount = 400
			}
			if _, _, err := core.RecordPayment(ctx, tx, org.ID, core.NewPayment{
				InvoiceID: id, Amount: amount, PaidOn: time.Now(), Method: "bank_transfer",
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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID

	// A BSB and account number alone is only one pay option (bank
	// transfer); a rent invoice needs at least two.
	if _, err := pool.Exec(ctx, "UPDATE orgs SET bank_bsb=$1, bank_account=$2 WHERE id=$3", "062-000", "12345678", org.ID); err != nil {
		t.Fatalf("setting org bank details: %v", err)
	}

	draftID := e.Invoice(org, contactID, testkit.Source("rent"), testkit.Amount(1000)).ID

	svc := e.BillingService()
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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")

	getenv := func(key string) string {
		if key == "SHIGOTO_STRIPE_ACME_SECRET_KEY" {
			return "sk_test_dummy"
		}
		return ""
	}
	svc := e.BillingService()
	svc.GetEnv = getenv

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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID
	draftID := e.Invoice(org, contactID, testkit.Source("manual"), testkit.Amount(1000)).ID

	svc := e.BillingService()
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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	contactID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID
	svc := e.BillingService()

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

	draftID := e.Invoice(org, contactID, testkit.Source("manual"), testkit.Amount(1000)).ID
	if got := outstanding(draftID); got != 0 {
		t.Errorf("draft outstanding = %d, want 0", got)
	}

	voidID := e.Invoice(org, contactID, testkit.Source("manual"), testkit.Amount(2000)).ID
	if _, err := svc.Issue(ctx, org.ID, "acme", voidID); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := svc.Void(ctx, org.ID, "acme", voidID); err != nil {
		t.Fatalf("Void: %v", err)
	}
	if got := outstanding(voidID); got != 0 {
		t.Errorf("void outstanding = %d, want 0", got)
	}

	openID := e.Invoice(org, contactID, testkit.Source("manual"), testkit.Amount(3000)).ID
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
