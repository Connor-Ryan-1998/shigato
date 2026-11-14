//go:build integration

package property_test

import (
	"bytes"
	"errors"
	"testing"

	"shigoto/internal/core"
	"shigoto/internal/property"
	"shigoto/internal/testkit"
)

func saveFile(e *testkit.Env, org core.Org, name string, data []byte) core.File {
	e.T.Helper()
	f, err := core.SaveFile(e.Ctx, e.Pool, org.ID, e.T.TempDir(), name, bytes.NewReader(data))
	if err != nil {
		e.T.Fatalf("SaveFile: %v", err)
	}
	return f
}

func aircon(propertyID int64) property.DepreciationItem {
	return property.DepreciationItem{
		PropertyID: propertyID, Name: "Split-system aircon", Type: "plant_equipment",
		InstalledOn: testkit.Date("2026-08-10"), Cost: ptr(core.Cents(320000)), Notes: "Living room",
		Amounts: []property.DepreciationAmount{{FY: 2027, Amount: 45000}, {FY: 2028, Amount: 38000}},
	}
}

func TestDepreciationItemRoundTrip(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	p := e.Property(org)
	exp := expense(e, org, "Equipment", 320000, testkit.Date("2026-08-10"), &p.ID)
	doc := saveFile(e, org, "invoice.png", testkit.PNG)

	in := aircon(p.ID)
	in.ExpenseID = &exp.ID
	in.Documents = []core.File{{ID: doc.ID}}
	created, err := property.CreateDepreciationItem(e.Ctx, e.Pool, org.ID, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	list, err := property.ListDepreciationItems(e.Ctx, e.Pool, org.ID, p.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	got := list[0]
	if got.ID != created.ID || got.Name != "Split-system aircon" || got.Type != "plant_equipment" || *got.Cost != 320000 || *got.ExpenseID != exp.ID || got.Notes != "Living room" {
		t.Fatalf("item = %+v", got)
	}
	if len(got.Amounts) != 2 || got.Amounts[0] != (property.DepreciationAmount{FY: 2027, Amount: 45000}) {
		t.Fatalf("amounts = %+v", got.Amounts)
	}
	if len(got.Documents) != 1 || got.Documents[0].ID != doc.ID || got.Documents[0].OriginalName != "invoice.png" {
		t.Fatalf("documents = %+v", got.Documents)
	}
	if ok, _ := property.FileAttachedToProperty(e.Ctx, e.Pool, org.ID, p.ID, doc.ID); !ok {
		t.Fatal("document not attached to property")
	}

	if err := property.DeleteDepreciationItem(e.Ctx, e.Pool, org.ID, p.ID, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if list, _ := property.ListDepreciationItems(e.Ctx, e.Pool, org.ID, p.ID); len(list) != 0 {
		t.Fatalf("after delete = %+v", list)
	}
}

func TestDepreciationRejectsInvalid(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	p := e.Property(org)
	cases := map[string]func(*property.DepreciationItem){
		"duplicate FY": func(i *property.DepreciationItem) {
			i.Amounts = append(i.Amounts, property.DepreciationAmount{FY: 2027, Amount: 1})
		},
		"zero amount":    func(i *property.DepreciationItem) { i.Amounts[0].Amount = 0 },
		"unknown type":   func(i *property.DepreciationItem) { i.Type = "car" },
		"no name":        func(i *property.DepreciationItem) { i.Name = "" },
		"negative cost":  func(i *property.DepreciationItem) { i.Cost = ptr(core.Cents(-1)) },
		"no install day": func(i *property.DepreciationItem) { i.InstalledOn = testkit.Date("0001-01-01") },
	}
	for name, mutate := range cases {
		in := aircon(p.ID)
		mutate(&in)
		if _, err := property.CreateDepreciationItem(e.Ctx, e.Pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestDepreciationUpdateReplacesAmountsAndDocuments(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	p := e.Property(org)
	docA := saveFile(e, org, "a.png", testkit.PNG)
	docB := saveFile(e, org, "b.pdf", []byte("%PDF-1.4\n%%EOF\n"))

	in := aircon(p.ID)
	in.Documents = []core.File{{ID: docA.ID}}
	created, err := property.CreateDepreciationItem(e.Ctx, e.Pool, org.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	created.Name = "Ducted aircon"
	created.Amounts = []property.DepreciationAmount{{FY: 2029, Amount: 1000}}
	created.Documents = []core.File{{ID: docB.ID}}
	got, err := property.UpdateDepreciationItem(e.Ctx, e.Pool, org.ID, created)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Name != "Ducted aircon" || len(got.Amounts) != 1 || got.Amounts[0].FY != 2029 || len(got.Documents) != 1 || got.Documents[0].ID != docB.ID {
		t.Fatalf("updated = %+v", got)
	}
	if ok, _ := property.FileAttachedToProperty(e.Ctx, e.Pool, org.ID, p.ID, docA.ID); ok {
		t.Fatal("removed document still attached")
	}
}

func TestDepreciationCrossOrgReferencesRejected(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	other := e.Org("other")
	p := e.Property(org)
	foreignExp := expense(e, other, "Equipment", 1000, testkit.Date("2026-08-10"), nil)
	foreignDoc := saveFile(e, other, "x.png", testkit.PNG)

	in := aircon(p.ID)
	in.ExpenseID = &foreignExp.ID
	if _, err := property.CreateDepreciationItem(e.Ctx, e.Pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("foreign expense: err = %v", err)
	}
	in = aircon(p.ID)
	in.Documents = []core.File{{ID: foreignDoc.ID}}
	if _, err := property.CreateDepreciationItem(e.Ctx, e.Pool, org.ID, in); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("foreign document: err = %v", err)
	}
	created, err := property.CreateDepreciationItem(e.Ctx, e.Pool, org.ID, aircon(p.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := property.UpdateDepreciationItem(e.Ctx, e.Pool, other.ID, created); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("cross-org update: err = %v", err)
	}
	if _, err := property.ListDepreciationItems(e.Ctx, e.Pool, other.ID, p.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("cross-org list: err = %v", err)
	}
}
