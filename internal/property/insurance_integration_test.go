//go:build integration

package property_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"shigoto/internal/core"
	"shigoto/internal/property"
	"shigoto/internal/testkit"
)

func policy(propertyID int64, start, renewal string) property.InsurancePolicy {
	return property.InsurancePolicy{
		PropertyID: propertyID, Insurer: "Acme Mutual", Type: "landlord", PolicyNumber: "LL-1",
		StartDate: testkit.Date(start), RenewalDate: testkit.Date(renewal),
		SumInsured: ptr(core.Cents(50000000)), Premium: ptr(core.Cents(120000)), Excess: ptr(core.Cents(50000)),
	}
}

func TestInsuranceCRUD(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	p := e.Property(org)

	created, err := property.CreateInsurancePolicy(e.Ctx, e.Pool, org.ID, policy(p.ID, "2025-07-01", "2026-07-01"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	newer, err := property.CreateInsurancePolicy(e.Ctx, e.Pool, org.ID, policy(p.ID, "2026-07-01", "2027-07-01"))
	if err != nil {
		t.Fatalf("Create newer: %v", err)
	}

	list, err := property.ListInsurancePolicies(e.Ctx, e.Pool, org.ID, p.ID)
	if err != nil || len(list) != 2 || list[0].ID != newer.ID {
		t.Fatalf("List = %+v, %v; want newest renewal first", list, err)
	}

	upd := created
	upd.Insurer = "Other Co"
	upd.Excess = nil
	got, err := property.UpdateInsurancePolicy(e.Ctx, e.Pool, org.ID, upd)
	if err != nil || got.Insurer != "Other Co" || got.Excess != nil || *got.Premium != 120000 {
		t.Fatalf("Update = %+v, %v", got, err)
	}

	other := e.Org("other")
	if _, err := property.UpdateInsurancePolicy(e.Ctx, e.Pool, other.ID, upd); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cross-org update err = %v", err)
	}
	if err := property.DeleteInsurancePolicy(e.Ctx, e.Pool, other.ID, p.ID, created.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cross-org delete err = %v", err)
	}
	if err := property.DeleteInsurancePolicy(e.Ctx, e.Pool, org.ID, p.ID, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, _ = property.ListInsurancePolicies(e.Ctx, e.Pool, org.ID, p.ID)
	if len(list) != 1 {
		t.Fatalf("after delete len = %d", len(list))
	}
}

func TestInsuranceRejectsInvalid(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	p := e.Property(org)
	other := e.Org("other")
	otherFile, err := core.SaveFile(e.Ctx, e.Pool, other.ID, t.TempDir(), "p.png", bytes.NewReader(testkit.PNG))
	if err != nil {
		t.Fatal(err)
	}

	bad := map[string]property.InsurancePolicy{}
	b := policy(p.ID, "2026-07-01", "2026-06-30")
	bad["renewal before start"] = b
	b = policy(p.ID, "2026-07-01", "2027-07-01")
	b.Type = "car"
	bad["unknown type"] = b
	b = policy(p.ID, "2026-07-01", "2027-07-01")
	b.Insurer = ""
	bad["no insurer"] = b
	b = policy(p.ID, "2026-07-01", "2027-07-01")
	b.Premium = ptr(core.Cents(-1))
	bad["negative premium"] = b
	b = policy(p.ID, "2026-07-01", "2027-07-01")
	b.FileID = &otherFile.ID
	bad["foreign file"] = b
	for name, in := range bad {
		if _, err := property.CreateInsurancePolicy(e.Ctx, e.Pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}

	foreignProp := e.Property(other)
	if _, err := property.CreateInsurancePolicy(e.Ctx, e.Pool, org.ID, policy(foreignProp.ID, "2026-07-01", "2027-07-01")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("foreign property: err = %v, want ErrNotFound", err)
	}
}

func TestPropertyFileDownloadRequiresAttachment(t *testing.T) {
	e := testkit.New(t)
	org, s := e.OwnerSession("acme", e.Handler())
	a := e.Property(org, testkit.PropertyName("A"))
	b := e.Property(org, testkit.PropertyName("B"))

	rr := s.Upload(fmt.Sprintf("/api/v1/orgs/acme/properties/%d/files", a.ID), "policy.png", testkit.PNG)
	if rr.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rr.Code, rr.Body)
	}
	var f struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rr.Body, &f); err != nil {
		t.Fatal(err)
	}
	download := func(propID int64) int {
		return s.Do(http.MethodGet, fmt.Sprintf("/api/v1/orgs/acme/properties/%d/files/%d", propID, f.ID), nil).Code
	}
	if code := download(a.ID); code != http.StatusNotFound {
		t.Fatalf("unattached download = %d, want 404", code)
	}

	pol := policy(a.ID, "2026-07-01", "2027-07-01")
	pol.FileID = &f.ID
	if _, err := property.CreateInsurancePolicy(e.Ctx, e.Pool, org.ID, pol); err != nil {
		t.Fatal(err)
	}
	if code := download(a.ID); code != http.StatusOK {
		t.Fatalf("attached download = %d, want 200", code)
	}
	if code := download(b.ID); code != http.StatusNotFound {
		t.Fatalf("other property download = %d, want 404", code)
	}

	rr = s.Do(http.MethodGet, fmt.Sprintf("/api/v1/orgs/acme/properties/%d/insurance", a.ID), nil)
	var list []map[string]any
	if err := json.Unmarshal(rr.Body, &list); err != nil || len(list) != 1 || list[0]["file_name"] != "policy.png" {
		t.Fatalf("list = %s (%v)", rr.Body, err)
	}
}

func TestDashboardInsuranceRenewingSoon(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	today := testkit.Date("2026-11-14")
	day := func(n int) string { return today.AddDate(0, 0, n).Format(property.DateLayout) }

	soon := e.Property(org, testkit.PropertyName("Soon"))
	property.CreateInsurancePolicy(e.Ctx, e.Pool, org.ID, policy(soon.ID, day(-335), day(30)))
	later := e.Property(org, testkit.PropertyName("Later"))
	property.CreateInsurancePolicy(e.Ctx, e.Pool, org.ID, policy(later.ID, day(-334), day(31)))
	// renewed: an old policy renewing soon is superseded by a newer one.
	renewed := e.Property(org, testkit.PropertyName("Renewed"))
	property.CreateInsurancePolicy(e.Ctx, e.Pool, org.ID, policy(renewed.ID, day(-345), day(20)))
	property.CreateInsurancePolicy(e.Ctx, e.Pool, org.ID, policy(renewed.ID, day(20), day(385)))

	got, err := property.InsuranceRenewingSoon(e.Ctx, e.Pool, org.ID, today)
	if err != nil || got != 1 {
		t.Fatalf("InsuranceRenewingSoon = %d, %v; want 1", got, err)
	}
}
