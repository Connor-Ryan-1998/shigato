//go:build integration

package property_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"shigoto/internal/core"
	"shigoto/internal/property"
	"shigoto/internal/testkit"
)

func TestBillsFYBoundary(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	p := e.Property(org)
	other := e.Property(org, testkit.PropertyName("Other"))
	expense(e, org, "Water", 9000, testkit.Date("2026-06-30"), &p.ID)
	inFY := expense(e, org, "Council rates", 80000, testkit.Date("2026-07-01"), &p.ID)
	expense(e, org, "Water", 7000, testkit.Date("2026-07-02"), &other.ID)
	expense(e, org, "Water", 6000, testkit.Date("2026-07-03"), nil)

	lines, err := property.Bills(e.Ctx, e.Pool, org.ID, &p.ID, testkit.Date("2026-07-01"), testkit.Date("2027-06-30"))
	if err != nil {
		t.Fatalf("Bills: %v", err)
	}
	if len(lines) != 1 || lines[0].ExpenseID != inFY.ID || lines[0].Category != "Council rates" || lines[0].Ex != 80000 {
		t.Fatalf("lines = %+v", lines)
	}
	if lines[0].Label == nil || *lines[0].Label != core.Item21CouncilRates || lines[0].PropertyID != p.ID {
		t.Fatalf("label/property = %+v", lines[0])
	}

	all, err := property.Bills(e.Ctx, e.Pool, org.ID, nil, testkit.Date("2026-07-01"), testkit.Date("2027-06-30"))
	if err != nil || len(all) != 2 {
		t.Fatalf("all-property bills = %+v, %v; want 2 property-tagged lines", all, err)
	}
}

type item21Body struct {
	FY         int `json:"fy"`
	Properties []struct {
		PropertyID      int64            `json:"property_id"`
		Name            string           `json:"name"`
		GrossRent       int64            `json:"gross_rent"`
		OtherIncome     int64            `json:"other_income"`
		Deductions      map[string]int64 `json:"deductions"`
		TotalDeductions int64            `json:"total_deductions"`
		Net             int64            `json:"net"`
		Warnings        []string         `json:"warnings"`
	} `json:"properties"`
}

func TestItem21CashBasisRent(t *testing.T) {
	e := testkit.New(t)
	org, s := e.OwnerSession("acme", e.Handler())
	tenant := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tina"))
	p := e.Property(org, testkit.PropertyName("Cottage"))

	// Issued in FY2026, paid in FY2027: counts in FY2027.
	rent := e.Invoice(org, tenant.ID, testkit.Amount(50000), testkit.Source("rent"), testkit.LineProperty(p.ID), testkit.Issued(testkit.Date("2026-06-20")))
	pay(e, org, rent.ID, 50000, "2026-07-03")
	// A bond paid in FY2027 is a liability, never income.
	bond := e.Invoice(org, tenant.ID, testkit.Amount(200000), testkit.Source("bond"), testkit.LineProperty(p.ID), testkit.Issued(testkit.Date("2026-07-01")))
	pay(e, org, bond.ID, 200000, "2026-07-04")
	// Paid in FY2026: not this year's.
	early := e.Invoice(org, tenant.ID, testkit.Amount(40000), testkit.Source("rent"), testkit.LineProperty(p.ID), testkit.Issued(testkit.Date("2026-06-01")))
	pay(e, org, early.ID, 40000, "2026-06-10")
	expense(e, org, "Council rates", 80000, testkit.Date("2026-08-01"), &p.ID)
	expense(e, org, "Equipment", 320000, testkit.Date("2026-08-10"), &p.ID)
	if _, err := property.CreateDepreciationItem(e.Ctx, e.Pool, org.ID, aircon(p.ID)); err != nil {
		t.Fatal(err)
	}

	rr := s.Do(http.MethodGet, fmt.Sprintf("/api/v1/orgs/acme/rental-reports/item21?fy=2027&property_id=%d", p.ID), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d %s", rr.Code, rr.Body)
	}
	var body item21Body
	if err := json.Unmarshal(rr.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.FY != 2027 || len(body.Properties) != 1 {
		t.Fatalf("body = %+v", body)
	}
	got := body.Properties[0]
	if got.Name != "Cottage" || got.GrossRent != 50000 || got.OtherIncome != 0 {
		t.Fatalf("income = %+v", got)
	}
	want := map[string]int64{"council_rates": 80000, "capital_allowances": 45000}
	if fmt.Sprint(got.Deductions) != fmt.Sprint(want) || got.TotalDeductions != 125000 || got.Net != 50000-125000 {
		t.Fatalf("deductions = %+v", got)
	}
}

func TestRentalReportCSV(t *testing.T) {
	e := testkit.New(t)
	org, s := e.OwnerSession("acme", e.Handler())
	p := e.Property(org, testkit.PropertyName("Cottage"))
	expense(e, org, "Council rates", 80000, testkit.Date("2026-08-01"), &p.ID)

	rr := s.Do(http.MethodGet, "/api/v1/orgs/acme/rental-reports/accountant?fy=2027&format=csv", nil)
	if rr.Code != http.StatusOK || !strings.HasPrefix(rr.Header.Get("Content-Type"), "text/csv") {
		t.Fatalf("accountant csv: %d %q", rr.Code, rr.Header.Get("Content-Type"))
	}
	if got := rr.Header.Get("Content-Disposition"); got != `attachment; filename="rental-accountant-FY2027.csv"` {
		t.Errorf("Content-Disposition = %q", got)
	}
	lines := strings.Split(strings.TrimSpace(string(rr.Body)), "\n")
	if lines[0] != "property,section,date,category,item21_label,description,supplier,incl_gst,gst,ex_gst,receipt" {
		t.Fatalf("header = %q", lines[0])
	}
	if !strings.Contains(string(rr.Body), "Cottage,expense,2026-08-01,Council rates,council_rates,Council rates bill,,800.00,0.00,800.00,") {
		t.Fatalf("body = %s", rr.Body)
	}

	rr = s.Do(http.MethodGet, "/api/v1/orgs/acme/rental-reports/item21?fy=2027&format=csv", nil)
	lines = strings.Split(strings.TrimSpace(string(rr.Body)), "\n")
	if rr.Code != http.StatusOK || lines[0] != "property,label,amount" || !strings.Contains(string(rr.Body), "Cottage,Council rates,800.00") {
		t.Fatalf("item21 csv: %d %s", rr.Code, rr.Body)
	}
}

func pay(e *testkit.Env, org core.Org, invoiceID int64, amount core.Cents, on string) {
	e.T.Helper()
	if _, _, err := core.RecordPayment(e.Ctx, e.Pool, org.ID, core.NewPayment{
		InvoiceID: invoiceID, Amount: amount, PaidOn: testkit.Date(on), Method: "cash",
	}); err != nil {
		e.T.Fatalf("RecordPayment: %v", err)
	}
}

func TestRentalReportCSVGuardsFormulas(t *testing.T) {
	e := testkit.New(t)
	org, s := e.OwnerSession("acme", e.Handler())
	p := e.Property(org, testkit.PropertyName("=cmd|' /C calc'!A0"))
	exp := expense(e, org, "Council rates", 80000, testkit.Date("2026-08-01"), &p.ID)
	if _, err := e.Pool.Exec(e.Ctx, `UPDATE expenses SET description = '@SUM(1+1)' WHERE id = $1`, exp.ID); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"accountant", "item21"} {
		rr := s.Do(http.MethodGet, "/api/v1/orgs/acme/rental-reports/"+kind+"?fy=2027&format=csv", nil)
		body := string(rr.Body)
		if strings.Contains(body, "\n=cmd") || strings.Contains(body, ",@SUM") {
			t.Errorf("%s csv has an unguarded formula cell:\n%s", kind, body)
		}
		if !strings.Contains(body, "'=cmd") {
			t.Errorf("%s csv missing guarded property name:\n%s", kind, body)
		}
	}
}
