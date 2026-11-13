//go:build integration

// Package property_test holds property's integration tests. They live
// outside package property so they can use internal/testkit, which imports
// property; export_test.go re-exports the unexported symbols they need.
package property_test

import (
	"errors"
	"testing"

	"shigoto/internal/core"
	"shigoto/internal/property"
	"shigoto/internal/testkit"
)

func TestCreatePropertyAndGet(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")

	created, err := property.CreateProperty(ctx, pool, org.ID, property.Property{Name: "1 Test St", Address: "1 Test St, Brisbane", Kind: "house", Residential: true})
	if err != nil {
		t.Fatalf("CreateProperty: %v", err)
	}

	got, err := property.GetProperty(ctx, pool, org.ID, created.ID)
	if err != nil {
		t.Fatalf("GetProperty: %v", err)
	}
	if got.Name != "1 Test St" || got.Address != "1 Test St, Brisbane" || !got.Residential {
		t.Fatalf("GetProperty = %+v, want matching created property", got)
	}

	other := e.Org("other")
	if _, err := property.GetProperty(ctx, pool, other.ID, created.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cross-org GetProperty: err = %v, want ErrNotFound", err)
	}
}

func TestCreateLeaseRequiresOnePrimary(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID
	tenantID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant One")).ID
	tenant2ID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant Two")).ID

	base := property.NewLease{
		PropertyID: propertyID, StartDate: testkit.Date("2026-11-02"),
		Frequency: property.Weekly, RentAmount: 50000,
	}

	// No primary.
	in := base
	in.Tenants = []property.NewLeaseTenant{{ContactID: tenantID, Primary: false}}
	if _, err := property.CreateLease(ctx, pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("no primary: err = %v, want ErrInvalidInput", err)
	}

	// Two primaries.
	in = base
	in.Tenants = []property.NewLeaseTenant{{ContactID: tenantID, Primary: true}, {ContactID: tenant2ID, Primary: true}}
	if _, err := property.CreateLease(ctx, pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("two primaries: err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateLeaseRejectsCrossOrgTenant(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	other := e.Org("other")
	propertyID := e.Property(org).ID
	otherTenantID := e.Contact(other, testkit.Kind("tenant"), testkit.Name("Other Org Tenant")).ID

	in := property.NewLease{
		PropertyID: propertyID, StartDate: testkit.Date("2026-11-02"),
		Frequency: property.Weekly, RentAmount: 50000,
		Tenants: []property.NewLeaseTenant{{ContactID: otherTenantID, Primary: true}},
	}
	if _, err := property.CreateLease(ctx, pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("cross-org tenant: err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateLeaseRejectsNonTenantContact(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID
	clientID := e.Contact(org, testkit.Kind("client"), testkit.Name("Not A Tenant")).ID

	in := property.NewLease{
		PropertyID: propertyID, StartDate: testkit.Date("2026-11-02"),
		Frequency: property.Weekly, RentAmount: 50000,
		Tenants: []property.NewLeaseTenant{{ContactID: clientID, Primary: true}},
	}
	if _, err := property.CreateLease(ctx, pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("non-tenant contact: err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateLeaseSuccessStoresInitialRentChange(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID
	tenantID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant One")).ID

	in := property.NewLease{
		PropertyID: propertyID, StartDate: testkit.Date("2026-11-02"),
		Frequency: property.Weekly, RentAmount: 50000,
		Tenants: []property.NewLeaseTenant{{ContactID: tenantID, Primary: true}},
	}
	lease, err := property.CreateLease(ctx, pool, org.ID, in)
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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID
	tenantID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant One")).ID

	lease := e.Lease(org, propertyID, tenantID, testkit.Start(testkit.Date("2025-01-01")))

	// First increase: no prior non-initial increase, so no warning.
	_, warnings, err := property.CreateRentChange(ctx, pool, org.ID, lease.ID, testkit.Date("2025-06-01"), 55000)
	if err != nil {
		t.Fatalf("first increase: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("first increase warnings = %v, want none", warnings)
	}

	// Second increase 6 months later: within 365 days of the first increase.
	_, warnings, err = property.CreateRentChange(ctx, pool, org.ID, lease.ID, testkit.Date("2025-12-01"), 60000)
	if err != nil {
		t.Fatalf("second increase: %v", err)
	}
	if len(warnings) != 1 || warnings[0] != property.WarnMoreThanOneIncreaseWithin12Months {
		t.Fatalf("second increase warnings = %v, want [%s]", warnings, property.WarnMoreThanOneIncreaseWithin12Months)
	}

	got, err := property.GetLease(ctx, pool, org.ID, lease.ID)
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if len(got.RentChanges) != 3 {
		t.Fatalf("RentChanges = %d rows, want 3 (initial + 2 increases, both stored)", len(got.RentChanges))
	}
}

func TestCreateChargeAndTerminateLease(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID
	tenantID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant One")).ID

	lease := e.Lease(org, propertyID, tenantID, testkit.Start(testkit.Date("2026-11-02")))

	charge, err := property.CreateCharge(ctx, pool, org.ID, lease.ID, property.NewCharge{
		Description: "Water", Amount: 2000, StartDate: testkit.Date("2026-11-09"),
	})
	if err != nil {
		t.Fatalf("CreateCharge: %v", err)
	}
	if charge.Description != "Water" || charge.Amount != 2000 {
		t.Fatalf("charge = %+v, want Water/2000", charge)
	}

	updated, err := property.TerminateLease(ctx, pool, org.ID, lease.ID, testkit.Date("2026-12-01"))
	if err != nil {
		t.Fatalf("TerminateLease: %v", err)
	}
	if updated.TerminationDate == nil || !updated.TerminationDate.Equal(testkit.Date("2026-12-01")) {
		t.Fatalf("TerminationDate = %v, want 2026-12-01", updated.TerminationDate)
	}

	if _, err := property.TerminateLease(ctx, pool, org.ID, 999999, testkit.Date("2026-12-01")); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("terminate unknown lease: err = %v, want ErrNotFound", err)
	}
}

func TestCreateLeasePaidToDateAlignment(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	tenantID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant One")).ID

	base := func() property.NewLease {
		return property.NewLease{
			PropertyID: e.Property(org).ID, StartDate: testkit.Date("2026-11-02"),
			Frequency: property.Weekly, RentAmount: 50000,
			Tenants: []property.NewLeaseTenant{{ContactID: tenantID, Primary: true}},
		}
	}

	// Accepted: start-1 (nothing paid yet).
	in := base()
	notPaid := testkit.Date("2026-11-01")
	in.PaidToDate = &notPaid
	if _, err := property.CreateLease(ctx, pool, org.ID, in); err != nil {
		t.Fatalf("paid_to_date = start-1: err = %v, want nil", err)
	}

	// Accepted: a period's natural last day.
	in = base()
	periodEnd := testkit.Date("2026-11-15") // end of the 2nd weekly period.
	in.PaidToDate = &periodEnd
	if _, err := property.CreateLease(ctx, pool, org.ID, in); err != nil {
		t.Fatalf("paid_to_date = period end: err = %v, want nil", err)
	}

	// Rejected: a misaligned, mid-period date.
	in = base()
	misaligned := testkit.Date("2026-11-10")
	in.PaidToDate = &misaligned
	if _, err := property.CreateLease(ctx, pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("misaligned paid_to_date: err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateLeaseRejectsDuplicateTenant(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID
	tenantID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant One")).ID

	in := property.NewLease{
		PropertyID: propertyID, StartDate: testkit.Date("2026-11-02"),
		Frequency: property.Weekly, RentAmount: 50000,
		Tenants: []property.NewLeaseTenant{
			{ContactID: tenantID, Primary: true},
			{ContactID: tenantID, Primary: false},
		},
	}
	if _, err := property.CreateLease(ctx, pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("duplicate tenant contact_id: err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateLeaseRejectsEndBeforeStartAndNegativeAmounts(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	tenantID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant One")).ID

	base := func() property.NewLease {
		return property.NewLease{
			PropertyID: e.Property(org).ID, StartDate: testkit.Date("2026-11-02"),
			Frequency: property.Weekly, RentAmount: 50000,
			Tenants: []property.NewLeaseTenant{{ContactID: tenantID, Primary: true}},
		}
	}

	in := base()
	end := testkit.Date("2026-11-01")
	in.EndDate = &end
	if _, err := property.CreateLease(ctx, pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("end_date before start_date: err = %v, want ErrInvalidInput", err)
	}

	in = base()
	in.OpeningArrears = -1
	if _, err := property.CreateLease(ctx, pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("negative opening_arrears: err = %v, want ErrInvalidInput", err)
	}

	in = base()
	in.BondAmount = -1
	if _, err := property.CreateLease(ctx, pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("negative bond_amount: err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateRentChangeRejectsDuplicateEffectiveDate(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID
	tenantID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant One")).ID

	lease := e.Lease(org, propertyID, tenantID, testkit.Start(testkit.Date("2026-11-02")))

	// Same effective_date as the initial rent row.
	if _, _, err := property.CreateRentChange(ctx, pool, org.ID, lease.ID, testkit.Date("2026-11-02"), 60000); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("duplicate effective_date (initial): err = %v, want ErrInvalidInput", err)
	}

	if _, _, err := property.CreateRentChange(ctx, pool, org.ID, lease.ID, testkit.Date("2026-12-01"), 60000); err != nil {
		t.Fatalf("first distinct rent change: %v", err)
	}
	if _, _, err := property.CreateRentChange(ctx, pool, org.ID, lease.ID, testkit.Date("2026-12-01"), 70000); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("duplicate effective_date (repeat): err = %v, want ErrInvalidInput", err)
	}
}

// TestRentChangeWarningExactAnniversaryNoWarning covers the ruling that an
// increase exactly 365/366 days (one calendar year) after the prior
// increase is the boundary itself, not "within 12 months", so it must not
// warn.
func TestRentChangeWarningExactAnniversaryNoWarning(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID
	tenantID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant One")).ID

	lease := e.Lease(org, propertyID, tenantID, testkit.Start(testkit.Date("2024-01-01")))

	if _, warnings, err := property.CreateRentChange(ctx, pool, org.ID, lease.ID, testkit.Date("2024-03-01"), 55000); err != nil || len(warnings) != 0 {
		t.Fatalf("first increase: err = %v, warnings = %v, want nil, none", err, warnings)
	}
	_, warnings, err := property.CreateRentChange(ctx, pool, org.ID, lease.ID, testkit.Date("2025-03-01"), 60000)
	if err != nil {
		t.Fatalf("anniversary increase: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("anniversary increase (exactly 1 year later) warnings = %v, want none", warnings)
	}
}

func TestCreateChargeRejectsEndBeforeStart(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID
	tenantID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant One")).ID

	lease := e.Lease(org, propertyID, tenantID, testkit.Start(testkit.Date("2026-11-02")))

	end := testkit.Date("2026-11-01")
	_, err := property.CreateCharge(ctx, pool, org.ID, lease.ID, property.NewCharge{
		Description: "Water", Amount: 2000, StartDate: testkit.Date("2026-11-09"), EndDate: &end,
	})
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("charge end_date before start_date: err = %v, want ErrInvalidInput", err)
	}
}

func TestTerminateLeaseRejectsTerminationBeforeStart(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID
	tenantID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant One")).ID

	lease := e.Lease(org, propertyID, tenantID, testkit.Start(testkit.Date("2026-11-02")))

	if _, err := property.TerminateLease(ctx, pool, org.ID, lease.ID, testkit.Date("2026-11-01")); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("termination_date before start_date: err = %v, want ErrInvalidInput", err)
	}
}

func TestListLeasesByProperty(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID
	otherPropertyID := e.Property(org).ID
	tenantID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant One")).ID

	e.Lease(org, propertyID, tenantID, testkit.Start(testkit.Date("2026-11-02")))
	e.Lease(org, otherPropertyID, tenantID, testkit.Start(testkit.Date("2026-11-02")), testkit.Rent(60000))

	leases, err := property.ListLeasesByProperty(ctx, pool, org.ID, propertyID)
	if err != nil {
		t.Fatalf("ListLeasesByProperty: %v", err)
	}
	if len(leases) != 1 || leases[0].PropertyID != propertyID {
		t.Fatalf("leases = %+v, want exactly 1 for property %d", leases, propertyID)
	}
}
