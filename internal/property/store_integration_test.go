//go:build integration

package property

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/core"
	"shigoto/internal/db"
)

func testOrg(t *testing.T, pool *pgxpool.Pool, slug string) core.Org {
	t.Helper()
	org, err := core.CreateOrg(context.Background(), pool, slug, slug+" org")
	if err != nil {
		t.Fatalf("CreateOrg(%s): %v", slug, err)
	}
	return org
}

func testContact(t *testing.T, pool *pgxpool.Pool, orgID int64, kind, name string) int64 {
	t.Helper()
	c, err := core.CreateContact(context.Background(), pool, orgID, core.Contact{Kind: kind, Name: name})
	if err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	return c.ID
}

func testProperty(t *testing.T, pool *pgxpool.Pool, orgID int64) int64 {
	t.Helper()
	p, err := CreateProperty(context.Background(), pool, orgID, Property{Name: "1 Test St", Residential: true})
	if err != nil {
		t.Fatalf("CreateProperty: %v", err)
	}
	return p.ID
}

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.ParseInLocation(dateLayout, s, time.UTC)
	if err != nil {
		t.Fatalf("parsing date %s: %v", s, err)
	}
	return d
}

func TestCreatePropertyAndGet(t *testing.T) {
	pool := db.TestPool(t)
	org := testOrg(t, pool, "acme")
	ctx := context.Background()

	created, err := CreateProperty(ctx, pool, org.ID, Property{Name: "1 Test St", Address: "1 Test St, Brisbane", Kind: "house", Residential: true})
	if err != nil {
		t.Fatalf("CreateProperty: %v", err)
	}

	got, err := GetProperty(ctx, pool, org.ID, created.ID)
	if err != nil {
		t.Fatalf("GetProperty: %v", err)
	}
	if got.Name != "1 Test St" || got.Address != "1 Test St, Brisbane" || !got.Residential {
		t.Fatalf("GetProperty = %+v, want matching created property", got)
	}

	other := testOrg(t, pool, "other")
	if _, err := GetProperty(ctx, pool, other.ID, created.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cross-org GetProperty: err = %v, want ErrNotFound", err)
	}
}

func TestCreateLeaseRequiresOnePrimary(t *testing.T) {
	pool := db.TestPool(t)
	org := testOrg(t, pool, "acme")
	ctx := context.Background()
	propertyID := testProperty(t, pool, org.ID)
	tenantID := testContact(t, pool, org.ID, "tenant", "Tenant One")
	tenant2ID := testContact(t, pool, org.ID, "tenant", "Tenant Two")

	base := NewLease{
		PropertyID: propertyID, StartDate: mustDate(t, "2026-11-02"),
		Frequency: Weekly, RentAmount: 50000,
	}

	// No primary.
	in := base
	in.Tenants = []NewLeaseTenant{{ContactID: tenantID, Primary: false}}
	if _, err := CreateLease(ctx, pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("no primary: err = %v, want ErrInvalidInput", err)
	}

	// Two primaries.
	in = base
	in.Tenants = []NewLeaseTenant{{ContactID: tenantID, Primary: true}, {ContactID: tenant2ID, Primary: true}}
	if _, err := CreateLease(ctx, pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("two primaries: err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateLeaseRejectsCrossOrgTenant(t *testing.T) {
	pool := db.TestPool(t)
	org := testOrg(t, pool, "acme")
	other := testOrg(t, pool, "other")
	ctx := context.Background()
	propertyID := testProperty(t, pool, org.ID)
	otherTenantID := testContact(t, pool, other.ID, "tenant", "Other Org Tenant")

	in := NewLease{
		PropertyID: propertyID, StartDate: mustDate(t, "2026-11-02"),
		Frequency: Weekly, RentAmount: 50000,
		Tenants: []NewLeaseTenant{{ContactID: otherTenantID, Primary: true}},
	}
	if _, err := CreateLease(ctx, pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("cross-org tenant: err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateLeaseRejectsNonTenantContact(t *testing.T) {
	pool := db.TestPool(t)
	org := testOrg(t, pool, "acme")
	ctx := context.Background()
	propertyID := testProperty(t, pool, org.ID)
	clientID := testContact(t, pool, org.ID, "client", "Not A Tenant")

	in := NewLease{
		PropertyID: propertyID, StartDate: mustDate(t, "2026-11-02"),
		Frequency: Weekly, RentAmount: 50000,
		Tenants: []NewLeaseTenant{{ContactID: clientID, Primary: true}},
	}
	if _, err := CreateLease(ctx, pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("non-tenant contact: err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateLeaseSuccessStoresInitialRentChange(t *testing.T) {
	pool := db.TestPool(t)
	org := testOrg(t, pool, "acme")
	ctx := context.Background()
	propertyID := testProperty(t, pool, org.ID)
	tenantID := testContact(t, pool, org.ID, "tenant", "Tenant One")

	in := NewLease{
		PropertyID: propertyID, StartDate: mustDate(t, "2026-11-02"),
		Frequency: Weekly, RentAmount: 50000,
		Tenants: []NewLeaseTenant{{ContactID: tenantID, Primary: true}},
	}
	lease, err := CreateLease(ctx, pool, org.ID, in)
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if len(lease.Tenants) != 1 || !lease.Tenants[0].IsPrimary {
		t.Fatalf("Tenants = %+v, want one primary tenant", lease.Tenants)
	}
	if len(lease.RentChanges) != 1 || lease.RentChanges[0].Amount != 50000 || !lease.RentChanges[0].EffectiveDate.Equal(in.StartDate) {
		t.Fatalf("RentChanges = %+v, want one row of 50000 dated %s", lease.RentChanges, in.StartDate)
	}
}

func TestRentChangeWarning(t *testing.T) {
	pool := db.TestPool(t)
	org := testOrg(t, pool, "acme")
	ctx := context.Background()
	propertyID := testProperty(t, pool, org.ID)
	tenantID := testContact(t, pool, org.ID, "tenant", "Tenant One")

	lease, err := CreateLease(ctx, pool, org.ID, NewLease{
		PropertyID: propertyID, StartDate: mustDate(t, "2025-01-01"),
		Frequency: Weekly, RentAmount: 50000,
		Tenants: []NewLeaseTenant{{ContactID: tenantID, Primary: true}},
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}

	// First increase: no prior non-initial increase, so no warning.
	_, warnings, err := CreateRentChange(ctx, pool, org.ID, lease.ID, mustDate(t, "2025-06-01"), 55000)
	if err != nil {
		t.Fatalf("first increase: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("first increase warnings = %v, want none", warnings)
	}

	// Second increase 6 months later: within 365 days of the first increase.
	_, warnings, err = CreateRentChange(ctx, pool, org.ID, lease.ID, mustDate(t, "2025-12-01"), 60000)
	if err != nil {
		t.Fatalf("second increase: %v", err)
	}
	if len(warnings) != 1 || warnings[0] != warnMoreThanOneIncreaseWithin12Months {
		t.Fatalf("second increase warnings = %v, want [%s]", warnings, warnMoreThanOneIncreaseWithin12Months)
	}

	got, err := GetLease(ctx, pool, org.ID, lease.ID)
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if len(got.RentChanges) != 3 {
		t.Fatalf("RentChanges = %d rows, want 3 (initial + 2 increases, both stored)", len(got.RentChanges))
	}
}

func TestCreateChargeAndTerminateLease(t *testing.T) {
	pool := db.TestPool(t)
	org := testOrg(t, pool, "acme")
	ctx := context.Background()
	propertyID := testProperty(t, pool, org.ID)
	tenantID := testContact(t, pool, org.ID, "tenant", "Tenant One")

	lease, err := CreateLease(ctx, pool, org.ID, NewLease{
		PropertyID: propertyID, StartDate: mustDate(t, "2026-11-02"),
		Frequency: Weekly, RentAmount: 50000,
		Tenants: []NewLeaseTenant{{ContactID: tenantID, Primary: true}},
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}

	charge, err := CreateCharge(ctx, pool, org.ID, lease.ID, NewCharge{
		Description: "Water", Amount: 2000, StartDate: mustDate(t, "2026-11-09"),
	})
	if err != nil {
		t.Fatalf("CreateCharge: %v", err)
	}
	if charge.Description != "Water" || charge.Amount != 2000 {
		t.Fatalf("charge = %+v, want Water/2000", charge)
	}

	updated, err := TerminateLease(ctx, pool, org.ID, lease.ID, mustDate(t, "2026-12-01"))
	if err != nil {
		t.Fatalf("TerminateLease: %v", err)
	}
	if updated.TerminationDate == nil || !updated.TerminationDate.Equal(mustDate(t, "2026-12-01")) {
		t.Fatalf("TerminationDate = %v, want 2026-12-01", updated.TerminationDate)
	}

	if _, err := TerminateLease(ctx, pool, org.ID, 999999, mustDate(t, "2026-12-01")); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("terminate unknown lease: err = %v, want ErrNotFound", err)
	}
}

func TestListLeasesByProperty(t *testing.T) {
	pool := db.TestPool(t)
	org := testOrg(t, pool, "acme")
	ctx := context.Background()
	propertyID := testProperty(t, pool, org.ID)
	otherPropertyID := testProperty(t, pool, org.ID)
	tenantID := testContact(t, pool, org.ID, "tenant", "Tenant One")

	if _, err := CreateLease(ctx, pool, org.ID, NewLease{
		PropertyID: propertyID, StartDate: mustDate(t, "2026-11-02"), Frequency: Weekly, RentAmount: 50000,
		Tenants: []NewLeaseTenant{{ContactID: tenantID, Primary: true}},
	}); err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if _, err := CreateLease(ctx, pool, org.ID, NewLease{
		PropertyID: otherPropertyID, StartDate: mustDate(t, "2026-11-02"), Frequency: Weekly, RentAmount: 60000,
		Tenants: []NewLeaseTenant{{ContactID: tenantID, Primary: true}},
	}); err != nil {
		t.Fatalf("CreateLease: %v", err)
	}

	leases, err := ListLeasesByProperty(ctx, pool, org.ID, propertyID)
	if err != nil {
		t.Fatalf("ListLeasesByProperty: %v", err)
	}
	if len(leases) != 1 || leases[0].PropertyID != propertyID {
		t.Fatalf("leases = %+v, want exactly 1 for property %d", leases, propertyID)
	}
}
