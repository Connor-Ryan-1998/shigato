//go:build integration

package recon_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/core"
	"shigoto/internal/module"
	"shigoto/internal/recon"
	"shigoto/internal/testkit"
)

// writtenOffInvoice issues a 10000-cent invoice and marks it uncollectible.
func writtenOffInvoice(t *testing.T, e *testkit.Env, org core.Org) core.Invoice {
	t.Helper()
	inv := e.Invoice(org, e.Contact(org).ID, testkit.Amount(10000), testkit.InvoiceGST("N-T"), testkit.Issued(time.Now()))
	inv, err := core.MarkInvoiceUncollectible(e.Ctx, e.Pool, org.ID, inv.ID)
	if err != nil {
		t.Fatalf("MarkInvoiceUncollectible: %v", err)
	}
	return inv
}

func inTx(t *testing.T, e *testkit.Env, fn func(pgx.Tx) error) {
	t.Helper()
	if err := module.RunInTx(e.Ctx, e.Pool, fn); err != nil {
		t.Fatal(err)
	}
}

func invoiceStatus(t *testing.T, e *testkit.Env, org core.Org, id int64) string {
	t.Helper()
	inv, err := core.GetInvoice(e.Ctx, e.Pool, org.ID, id)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	return inv.Status
}

func TestMatchInvoiceAcceptsUncollectible(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	inv := writtenOffInvoice(t, e, org)
	lineID := testBankLineID(t, e.Pool, org.ID, 4000, testkit.Date("2026-11-02"), "part payment")

	inTx(t, e, func(tx pgx.Tx) error {
		_, err := recon.MatchInvoice(e.Ctx, tx, org.ID, lineID, inv.ID, "bank_transfer")
		return err
	})
	if got := invoiceStatus(t, e, org, inv.ID); got != "uncollectible" {
		t.Fatalf("status after part match = %q, want uncollectible", got)
	}
}

func TestUnmatchRestoresUncollectible(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	inv := writtenOffInvoice(t, e, org)
	lineID := testBankLineID(t, e.Pool, org.ID, 10000, testkit.Date("2026-11-02"), "late payment")

	inTx(t, e, func(tx pgx.Tx) error {
		_, err := recon.MatchInvoice(e.Ctx, tx, org.ID, lineID, inv.ID, "bank_transfer")
		return err
	})
	if got := invoiceStatus(t, e, org, inv.ID); got != "paid" {
		t.Fatalf("status after full match = %q, want paid", got)
	}

	inTx(t, e, func(tx pgx.Tx) error { return recon.Unmatch(e.Ctx, tx, org.ID, lineID) })
	if got := invoiceStatus(t, e, org, inv.ID); got != "uncollectible" {
		t.Fatalf("status after unmatch = %q, want uncollectible", got)
	}
}

func TestAutoMatchSkipsUncollectible(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	inv := writtenOffInvoice(t, e, org)
	lineID := testBankLineID(t, e.Pool, org.ID, 10000, testkit.Date("2026-11-02"), "Payment "+inv.PaymentReference)

	n, err := recon.AutoMatch(e.Ctx, e.Pool, org.ID)
	if err != nil {
		t.Fatalf("AutoMatch: %v", err)
	}
	if n != 0 {
		t.Fatalf("AutoMatch matched %d lines, want 0", n)
	}
	suggestions, err := recon.Suggest(e.Ctx, e.Pool, e.BillingService(), org.ID, org.Slug, lineID)
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	for _, s := range suggestions {
		if s.Kind == "invoice" {
			t.Fatalf("Suggest offered invoice %d, want no invoice suggestions", s.ID)
		}
	}
}
