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

// sentInvoice issues a 10000-cent manual invoice through svc, so the fake
// provider receives it and it gets a provider_ref.
func sentInvoice(t *testing.T, e *testkit.Env, org core.Org, svc *billing.Service) core.Invoice {
	t.Helper()
	id := e.Invoice(org, e.Contact(org).ID, testkit.Amount(10000), testkit.DueIn(14)).ID
	inv, err := svc.Issue(e.Ctx, org.ID, org.Slug, id)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if inv.ProviderRef == "" {
		t.Fatal("issued invoice has no provider_ref")
	}
	return inv
}

func auditReasons(t *testing.T, e *testkit.Env, org core.Org, invoiceID int64, action string) []string {
	t.Helper()
	rows, err := e.Pool.Query(e.Ctx,
		`SELECT diff->>'reason' FROM audit_log WHERE org_id=$1 AND entity='invoice' AND entity_id=$2 AND action=$3 ORDER BY id`,
		org.ID, invoiceID, action)
	if err != nil {
		t.Fatalf("reading audit_log: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestMarkUncollectibleCallsProvider(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	fake := &payments.Fake{}
	svc := e.BillingService(testkit.Provider(fake))
	inv := sentInvoice(t, e, org, svc)

	got, err := svc.MarkUncollectible(e.Ctx, org.ID, org.Slug, inv.ID, "Client insolvent")
	if err != nil {
		t.Fatalf("MarkUncollectible: %v", err)
	}
	if got.Status != "uncollectible" {
		t.Fatalf("status = %q, want uncollectible", got.Status)
	}
	if refs := fake.MarkedUncollectibleRefs(); len(refs) != 1 || refs[0] != inv.ProviderRef {
		t.Fatalf("provider marked %v, want [%s]", refs, inv.ProviderRef)
	}
	if reasons := auditReasons(t, e, org, inv.ID, "mark_uncollectible"); len(reasons) != 1 || reasons[0] != "Client insolvent" {
		t.Fatalf("audit reasons = %v, want [Client insolvent]", reasons)
	}
}

func TestMarkUncollectibleWithoutProviderRefIsLocal(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	svc := e.BillingService() // no provider
	inv := e.Invoice(org, e.Contact(org).ID, testkit.Amount(10000), testkit.Issued(time.Now()))

	got, err := svc.MarkUncollectible(e.Ctx, org.ID, org.Slug, inv.ID, "Gone")
	if err != nil || got.Status != "uncollectible" {
		t.Fatalf("MarkUncollectible: status %q err %v", got.Status, err)
	}
}

func TestMarkUncollectibleProviderFailureRollsBack(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	fake := &payments.Fake{}
	svc := e.BillingService(testkit.Provider(fake))
	inv := sentInvoice(t, e, org, svc)
	fake.MarkUncollectibleErr = errors.New("stripe down")

	_, err := svc.MarkUncollectible(e.Ctx, org.ID, org.Slug, inv.ID, "Client insolvent")
	if !errors.Is(err, billing.ErrProviderStatusFailed) {
		t.Fatalf("err = %v, want ErrProviderStatusFailed", err)
	}
	after, err := core.GetInvoice(e.Ctx, e.Pool, org.ID, inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "open" || after.WrittenOff {
		t.Fatalf("after failed mark: status %q written_off %v, want open false", after.Status, after.WrittenOff)
	}
	if reasons := auditReasons(t, e, org, inv.ID, "mark_uncollectible"); len(reasons) != 0 {
		t.Fatalf("audit rows = %v, want none", reasons)
	}
}

func TestReinstateIsLocalOnly(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	fake := &payments.Fake{}
	svc := e.BillingService(testkit.Provider(fake))
	inv := sentInvoice(t, e, org, svc)
	if _, err := svc.MarkUncollectible(e.Ctx, org.ID, org.Slug, inv.ID, "Client insolvent"); err != nil {
		t.Fatal(err)
	}

	got, err := svc.Reinstate(e.Ctx, org.ID, inv.ID, "Client resumed paying")
	if err != nil {
		t.Fatalf("Reinstate: %v", err)
	}
	if got.Status != "open" || got.WrittenOff || got.ProviderRef != inv.ProviderRef {
		t.Fatalf("after reinstate: status %q written_off %v ref %q", got.Status, got.WrittenOff, got.ProviderRef)
	}
	if n := len(fake.MarkedUncollectibleRefs()); n != 1 {
		t.Fatalf("provider status calls = %d, want 1 (reinstate is local)", n)
	}
	if len(fake.VoidedRefs()) != 0 {
		t.Fatal("reinstate voided the provider invoice")
	}
	if reasons := auditReasons(t, e, org, inv.ID, "reinstate"); len(reasons) != 1 || reasons[0] != "Client resumed paying" {
		t.Fatalf("audit reasons = %v", reasons)
	}
}

func TestVoidUncollectibleVoidsAtProvider(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	fake := &payments.Fake{}
	svc := e.BillingService(testkit.Provider(fake))
	inv := sentInvoice(t, e, org, svc)
	if _, err := svc.MarkUncollectible(e.Ctx, org.ID, org.Slug, inv.ID, "Client insolvent"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Void(e.Ctx, org.ID, org.Slug, inv.ID)
	if err != nil || got.Status != "void" {
		t.Fatalf("Void: status %q err %v", got.Status, err)
	}
	if refs := fake.VoidedRefs(); len(refs) != 1 || refs[0] != inv.ProviderRef {
		t.Fatalf("voided refs %v", refs)
	}
}
