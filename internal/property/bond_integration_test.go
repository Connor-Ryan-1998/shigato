//go:build integration

package property

import (
	"context"
	"errors"
	"testing"

	"shigoto/internal/core"
	"shigoto/internal/db"
)

func TestBondInvoiceNTAndOnce(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	propertyID := testProperty(t, pool, org.ID)
	tenantID := testContact(t, pool, org.ID, "tenant", "Test Tenant")
	setOrgPayOptions(t, pool, org.ID)

	lease, err := CreateLease(ctx, pool, org.ID, NewLease{
		PropertyID: propertyID, StartDate: mustDate(t, "2026-11-01"), Frequency: Weekly,
		RentAmount: 50000, BondAmount: 140000,
		Tenants: []NewLeaseTenant{{ContactID: tenantID, Primary: true}},
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}

	b := testService(pool)
	inv, err := CreateBondInvoice(ctx, b, org.ID, "acme", lease.ID)
	if err != nil {
		t.Fatalf("CreateBondInvoice: %v", err)
	}
	if inv.Status != "open" {
		t.Fatalf("status = %s, want open", inv.Status)
	}
	if len(inv.Lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(inv.Lines))
	}
	line := inv.Lines[0]
	if line.GSTCode != core.NT {
		t.Errorf("gst_code = %s, want N-T", line.GSTCode)
	}
	if line.GSTAmount != 0 {
		t.Errorf("gst_amount = %d, want 0", line.GSTAmount)
	}
	if line.Amount != 140000 {
		t.Errorf("amount = %d, want 140000 (bond_amount)", line.Amount)
	}

	// A second POST (second call) must not create a second bond invoice.
	if _, err := CreateBondInvoice(ctx, b, org.ID, "acme", lease.ID); !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("second CreateBondInvoice error = %v, want core.ErrInvalidState (409)", err)
	}
}

func TestBondLodgeRequiresHeld(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	propertyID := testProperty(t, pool, org.ID)
	tenantID := testContact(t, pool, org.ID, "tenant", "Test Tenant")
	setOrgPayOptions(t, pool, org.ID)

	lease, err := CreateLease(ctx, pool, org.ID, NewLease{
		PropertyID: propertyID, StartDate: mustDate(t, "2026-11-01"), Frequency: Weekly,
		RentAmount: 50000, BondAmount: 140000,
		Tenants: []NewLeaseTenant{{ContactID: tenantID, Primary: true}},
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}

	b := testService(pool)
	inv, err := CreateBondInvoice(ctx, b, org.ID, "acme", lease.ID)
	if err != nil {
		t.Fatalf("CreateBondInvoice: %v", err)
	}

	// Lodging before payment: the derived status is still "due".
	if _, err := LodgeBond(ctx, pool, org.ID, nil, lease.ID, "QLD-REF-1", mustDate(t, "2026-11-05"), mustDate(t, "2026-11-10")); !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("lodge before payment error = %v, want core.ErrInvalidState (409)", err)
	}

	// Manually record payment in full.
	if _, _, err := core.RecordPayment(ctx, pool, org.ID, core.NewPayment{
		InvoiceID: inv.ID, Amount: inv.Total, PaidOn: mustDate(t, "2026-11-03"), Method: "bank_transfer",
	}); err != nil {
		t.Fatalf("RecordPayment: %v", err)
	}

	updated, err := LodgeBond(ctx, pool, org.ID, nil, lease.ID, "QLD-REF-1", mustDate(t, "2026-11-05"), mustDate(t, "2026-11-10"))
	if err != nil {
		t.Fatalf("LodgeBond after payment: %v", err)
	}
	if updated.RTALodgementRef != "QLD-REF-1" {
		t.Errorf("rta_lodgement_ref = %q, want QLD-REF-1", updated.RTALodgementRef)
	}

	status, _, err := BondInfo(ctx, pool, org.ID, updated, mustDate(t, "2026-11-10"))
	if err != nil {
		t.Fatalf("BondInfo: %v", err)
	}
	if status != "lodged" {
		t.Errorf("bond_status = %s, want lodged", status)
	}
}
