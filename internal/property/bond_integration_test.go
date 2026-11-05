//go:build integration

package property

import (
	"context"
	"errors"
	"testing"
	"time"

	"shigoto/internal/config"
	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/module"
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

// TestBondInvoiceZeroAmountRejected covers the brief's "422 if
// bond_amount == 0" rule.
func TestBondInvoiceZeroAmountRejected(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	propertyID := testProperty(t, pool, org.ID)
	tenantID := testContact(t, pool, org.ID, "tenant", "Test Tenant")
	setOrgPayOptions(t, pool, org.ID)

	lease, err := CreateLease(ctx, pool, org.ID, NewLease{
		PropertyID: propertyID, StartDate: mustDate(t, "2026-11-01"), Frequency: Weekly,
		RentAmount: 50000, BondAmount: 0,
		Tenants: []NewLeaseTenant{{ContactID: tenantID, Primary: true}},
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}

	if _, err := CreateBondInvoice(ctx, testService(pool), org.ID, "acme", lease.ID); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("bond_amount 0 error = %v, want core.ErrInvalidInput (422)", err)
	}
}

// TestTodayUsesCalendarDateInLocation pins routes.go's clockNow to an early
// morning instant in a +10:00 zone (Australia/Brisbane) - still "yesterday"
// in UTC - and checks today(d) derives the *local* calendar date rather
// than truncating the UTC instant. This is the root cause of a bug where a
// same-day bond lodgement (or BondWarnings' 10-day boundary) was computed
// against the wrong day for operators east of UTC.
func TestTodayUsesCalendarDateInLocation(t *testing.T) {
	loc, err := time.LoadLocation("Australia/Brisbane")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	orig := clockNow
	defer func() { clockNow = orig }()
	clockNow = func() time.Time { return time.Date(2026, 11, 5, 8, 0, 0, 0, loc) }

	got := today(module.Deps{Cfg: config.Config{Location: loc}})
	want := mustDate(t, "2026-11-05")
	if !got.Equal(want) {
		t.Errorf("today() = %s, want %s", got.Format(dateLayout), want.Format(dateLayout))
	}
}

// TestBondLodgementSameDayInBrisbaneTime exercises today(d) together with
// LodgeBond's date check at the same early-morning instant as
// TestTodayUsesCalendarDateInLocation: a lodgement dated "today" must
// succeed, and one dated tomorrow must be rejected as in the future -
// neither of which held before today(d) was fixed to return a calendar
// date instead of an instant.
func TestBondLodgementSameDayInBrisbaneTime(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	propertyID := testProperty(t, pool, org.ID)
	setOrgPayOptions(t, pool, org.ID)

	loc, err := time.LoadLocation("Australia/Brisbane")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	orig := clockNow
	defer func() { clockNow = orig }()
	clockNow = func() time.Time { return time.Date(2026, 11, 5, 8, 0, 0, 0, loc) }
	todayDate := today(module.Deps{Cfg: config.Config{Location: loc}})

	newHeldLease := func(t *testing.T) Lease {
		t.Helper()
		tenantID := testContact(t, pool, org.ID, "tenant", "Test Tenant")
		lease, err := CreateLease(ctx, pool, org.ID, NewLease{
			PropertyID: propertyID, StartDate: mustDate(t, "2026-11-01"), Frequency: Weekly,
			RentAmount: 50000, BondAmount: 140000,
			Tenants: []NewLeaseTenant{{ContactID: tenantID, Primary: true}},
		})
		if err != nil {
			t.Fatalf("CreateLease: %v", err)
		}
		inv, err := CreateBondInvoice(ctx, testService(pool), org.ID, "acme", lease.ID)
		if err != nil {
			t.Fatalf("CreateBondInvoice: %v", err)
		}
		if _, _, err := core.RecordPayment(ctx, pool, org.ID, core.NewPayment{
			InvoiceID: inv.ID, Amount: inv.Total, PaidOn: mustDate(t, "2026-11-03"), Method: "bank_transfer",
		}); err != nil {
			t.Fatalf("RecordPayment: %v", err)
		}
		return lease
	}

	sameDay := newHeldLease(t)
	if _, err := LodgeBond(ctx, pool, org.ID, nil, sameDay.ID, "QLD-REF-SAME-DAY", mustDate(t, "2026-11-05"), todayDate); err != nil {
		t.Fatalf("same-day lodgement rejected: %v", err)
	}

	tomorrow := newHeldLease(t)
	if _, err := LodgeBond(ctx, pool, org.ID, nil, tomorrow.ID, "QLD-REF-TOMORROW", mustDate(t, "2026-11-06"), todayDate); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("tomorrow's lodgement error = %v, want core.ErrInvalidInput (422)", err)
	}
}
