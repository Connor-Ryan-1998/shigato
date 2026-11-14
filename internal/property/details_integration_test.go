//go:build integration

package property_test

import (
	"errors"
	"testing"

	"shigoto/internal/core"
	"shigoto/internal/property"
	"shigoto/internal/testkit"
)

func ptr[T any](v T) *T { return &v }

func TestUpdatePropertyRoundTrips(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	created := e.Property(org)

	in := property.Property{
		ID: created.ID, Name: "Renamed", Address: "9 New St", Kind: "unit", Residential: false,
		ContractDate: ptr(testkit.Date("2020-02-01")), SettlementDate: ptr(testkit.Date("2020-03-15")),
		PurchasePrice: ptr(core.Cents(65000000)), StampDuty: ptr(core.Cents(2100000)), PurchaseLegalCosts: ptr(core.Cents(180000)),
		YearBuilt: ptr(1995), LotPlan: "Lot 3 RP12345", LandAreaM2: ptr(607.5),
		CouncilAccountRef: "BCC-1", WaterAccountRef: "UU-2",
	}
	if _, err := property.UpdateProperty(e.Ctx, e.Pool, org.ID, in); err != nil {
		t.Fatalf("UpdateProperty: %v", err)
	}
	got, err := property.GetProperty(e.Ctx, e.Pool, org.ID, created.ID)
	if err != nil {
		t.Fatalf("GetProperty: %v", err)
	}
	if got.Name != "Renamed" || got.Address != "9 New St" || got.Kind != "unit" || got.Residential {
		t.Fatalf("identity = %+v", got)
	}
	if !got.ContractDate.Equal(testkit.Date("2020-02-01")) || !got.SettlementDate.Equal(testkit.Date("2020-03-15")) {
		t.Fatalf("dates = %v %v", got.ContractDate, got.SettlementDate)
	}
	if *got.PurchasePrice != 65000000 || *got.StampDuty != 2100000 || *got.PurchaseLegalCosts != 180000 {
		t.Fatalf("money = %v %v %v", *got.PurchasePrice, *got.StampDuty, *got.PurchaseLegalCosts)
	}
	if *got.YearBuilt != 1995 || got.LotPlan != "Lot 3 RP12345" || *got.LandAreaM2 != 607.5 || got.CouncilAccountRef != "BCC-1" || got.WaterAccountRef != "UU-2" {
		t.Fatalf("reference = %+v", got)
	}

	// Clearing optional fields stores NULL.
	if _, err := property.UpdateProperty(e.Ctx, e.Pool, org.ID, property.Property{ID: created.ID, Name: "Renamed", Residential: true}); err != nil {
		t.Fatalf("UpdateProperty clear: %v", err)
	}
	got, _ = property.GetProperty(e.Ctx, e.Pool, org.ID, created.ID)
	if got.ContractDate != nil || got.PurchasePrice != nil || got.YearBuilt != nil || got.LandAreaM2 != nil || got.Address != "" {
		t.Fatalf("cleared = %+v", got)
	}
}

func TestUpdatePropertyRejectsInvalid(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	p := e.Property(org)
	cases := map[string]property.Property{
		"negative money": {ID: p.ID, Name: "X", PurchasePrice: ptr(core.Cents(-1))},
		"empty name":     {ID: p.ID, Name: ""},
		"year too early": {ID: p.ID, Name: "X", YearBuilt: ptr(1700)},
		"negative area":  {ID: p.ID, Name: "X", LandAreaM2: ptr(-1.0)},
	}
	for name, in := range cases {
		if _, err := property.UpdateProperty(e.Ctx, e.Pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestUpdatePropertyCrossOrg(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	other := e.Org("other")
	p := e.Property(org)
	if _, err := property.UpdateProperty(e.Ctx, e.Pool, other.ID, property.Property{ID: p.ID, Name: "Stolen"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := property.UpdateNotes(e.Ctx, e.Pool, other.ID, p.ID, "x"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("notes err = %v, want ErrNotFound", err)
	}
}

func TestUpdateNotesLeavesDetails(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	p := e.Property(org, testkit.PropertyName("Cottage"))
	if err := property.UpdateNotes(e.Ctx, e.Pool, org.ID, p.ID, "Leaking tap\nfixed"); err != nil {
		t.Fatalf("UpdateNotes: %v", err)
	}
	got, _ := property.GetProperty(e.Ctx, e.Pool, org.ID, p.ID)
	if got.Notes != "Leaking tap\nfixed" || got.Name != "Cottage" {
		t.Fatalf("got %+v", got)
	}
}

func TestCurrentTenancy(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	tenant := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tina"))
	p := e.Property(org)

	none, err := property.CurrentTenancy(e.Ctx, e.Pool, org.ID, p.ID, testkit.Date("2026-03-01"))
	if err != nil || none != nil {
		t.Fatalf("vacant: %+v %v", none, err)
	}

	lease := e.Lease(org, p.ID, tenant.ID, testkit.Start(testkit.Date("2026-01-05")), testkit.Rent(50000))
	if _, _, err := property.CreateRentChange(e.Ctx, e.Pool, org.ID, lease.ID, testkit.Date("2026-06-01"), 55000); err != nil {
		t.Fatalf("CreateRentChange: %v", err)
	}
	got, err := property.CurrentTenancy(e.Ctx, e.Pool, org.ID, p.ID, testkit.Date("2026-03-01"))
	if err != nil || got == nil {
		t.Fatalf("active: %+v %v", got, err)
	}
	if got.LeaseID != lease.ID || got.Rent != 50000 || got.Frequency != property.Weekly || got.PrimaryTenantID != tenant.ID {
		t.Fatalf("tenancy = %+v", got)
	}

	if _, err := property.TerminateLease(e.Ctx, e.Pool, org.ID, lease.ID, testkit.Date("2026-04-01")); err != nil {
		t.Fatalf("TerminateLease: %v", err)
	}
	after, err := property.CurrentTenancy(e.Ctx, e.Pool, org.ID, p.ID, testkit.Date("2026-05-01"))
	if err != nil || after != nil {
		t.Fatalf("terminated: %+v %v", after, err)
	}
}
