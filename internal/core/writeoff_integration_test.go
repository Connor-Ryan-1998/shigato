//go:build integration

package core_test

import (
	"errors"
	"testing"
	"time"

	"shigoto/internal/core"
	"shigoto/internal/testkit"
)

func TestMarkUncollectibleAndReinstate(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	inv := e.Invoice(org, e.Contact(org).ID, testkit.Amount(10000), testkit.Issued(time.Now()))

	marked, err := core.MarkInvoiceUncollectible(e.Ctx, e.Pool, org.ID, inv.ID)
	if err != nil {
		t.Fatalf("MarkInvoiceUncollectible: %v", err)
	}
	if marked.Status != "uncollectible" || !marked.WrittenOff {
		t.Fatalf("after mark: status %q written_off %v, want uncollectible true", marked.Status, marked.WrittenOff)
	}

	reinstated, err := core.ReinstateInvoice(e.Ctx, e.Pool, org.ID, inv.ID)
	if err != nil {
		t.Fatalf("ReinstateInvoice: %v", err)
	}
	if reinstated.Status != "open" || reinstated.WrittenOff {
		t.Fatalf("after reinstate: status %q written_off %v, want open false", reinstated.Status, reinstated.WrittenOff)
	}
}

func TestMarkUncollectibleRejectsNonOpen(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	contact := e.Contact(org).ID

	draft := e.Invoice(org, contact)
	paid := e.Invoice(org, contact, testkit.Amount(1000), testkit.Issued(time.Now()))
	if _, _, err := core.RecordPayment(e.Ctx, e.Pool, org.ID, core.NewPayment{InvoiceID: paid.ID, Amount: 1000, PaidOn: time.Now(), Method: "cash"}); err != nil {
		t.Fatalf("RecordPayment: %v", err)
	}
	void := e.Invoice(org, contact)
	if _, err := core.VoidInvoice(e.Ctx, e.Pool, org.ID, void.ID); err != nil {
		t.Fatalf("VoidInvoice: %v", err)
	}

	for name, id := range map[string]int64{"draft": draft.ID, "paid": paid.ID, "void": void.ID} {
		if _, err := core.MarkInvoiceUncollectible(e.Ctx, e.Pool, org.ID, id); !errors.Is(err, core.ErrInvalidState) {
			t.Errorf("mark %s: err = %v, want ErrInvalidState", name, err)
		}
	}

	open := e.Invoice(org, contact, testkit.Issued(time.Now()))
	if _, err := core.ReinstateInvoice(e.Ctx, e.Pool, org.ID, open.ID); !errors.Is(err, core.ErrInvalidState) {
		t.Errorf("reinstate open: err = %v, want ErrInvalidState", err)
	}
	if _, err := core.MarkInvoiceUncollectible(e.Ctx, e.Pool, org.ID, 999999); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("mark missing: err = %v, want ErrNotFound", err)
	}
}

func TestVoidUncollectible(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	contact := e.Contact(org).ID

	clean := e.Invoice(org, contact, testkit.Amount(10000), testkit.Issued(time.Now()))
	if _, err := core.MarkInvoiceUncollectible(e.Ctx, e.Pool, org.ID, clean.ID); err != nil {
		t.Fatalf("mark: %v", err)
	}
	voided, err := core.VoidInvoice(e.Ctx, e.Pool, org.ID, clean.ID)
	if err != nil || voided.Status != "void" {
		t.Fatalf("void uncollectible: status %q err %v, want void nil", voided.Status, err)
	}

	partPaid := e.Invoice(org, contact, testkit.Amount(10000), testkit.Issued(time.Now()))
	if _, _, err := core.RecordPayment(e.Ctx, e.Pool, org.ID, core.NewPayment{InvoiceID: partPaid.ID, Amount: 4000, PaidOn: time.Now(), Method: "cash"}); err != nil {
		t.Fatalf("RecordPayment: %v", err)
	}
	if _, err := core.MarkInvoiceUncollectible(e.Ctx, e.Pool, org.ID, partPaid.ID); err != nil {
		t.Fatalf("mark part-paid: %v", err)
	}
	if _, err := core.VoidInvoice(e.Ctx, e.Pool, org.ID, partPaid.ID); !errors.Is(err, core.ErrInvoiceHasPayments) {
		t.Fatalf("void part-paid uncollectible: err = %v, want ErrInvoiceHasPayments", err)
	}
}

func TestUncollectiblePaymentMarksPaid(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	inv := e.Invoice(org, e.Contact(org).ID, testkit.Amount(10000), testkit.Issued(time.Now()))
	if _, err := core.MarkInvoiceUncollectible(e.Ctx, e.Pool, org.ID, inv.ID); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if _, _, err := core.RecordPayment(e.Ctx, e.Pool, org.ID, core.NewPayment{InvoiceID: inv.ID, Amount: 10000, PaidOn: time.Now(), Method: "cash"}); err != nil {
		t.Fatalf("RecordPayment: %v", err)
	}
	got, err := core.GetInvoice(e.Ctx, e.Pool, org.ID, inv.ID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if got.Status != "paid" || !got.WrittenOff {
		t.Fatalf("status %q written_off %v, want paid true", got.Status, got.WrittenOff)
	}
}
