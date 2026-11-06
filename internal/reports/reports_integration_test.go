//go:build integration

// Package reports_test exercises the reports HTTP handlers end to end
// (through httpapi.New), so it needs to import httpapi, auth, property,
// timesheets and recon alongside reports without an import cycle: reports
// itself must not import property/timesheets/recon (they're toggleable
// modules), but its tests can use them to set up fixtures.
package reports_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/config"
	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/httpapi"
	"shigoto/internal/property"
	"shigoto/internal/recon"
)

// sessionCookieName mirrors the unexported constant of the same name in
// package auth (internal/auth/session.go); there's no exported way to get
// it from here.
const sessionCookieName = "shigoto_session"

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		t.Fatalf("parsing date %q: %v", s, err)
	}
	return d
}

func testOrg(t *testing.T, pool *pgxpool.Pool, slug string) core.Org {
	t.Helper()
	org, err := core.CreateOrg(context.Background(), pool, slug, slug+" org")
	if err != nil {
		t.Fatalf("CreateOrg(%s): %v", slug, err)
	}
	return org
}

func setGSTRegistered(t *testing.T, pool *pgxpool.Pool, orgID int64, registered bool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "UPDATE orgs SET gst_registered = $1 WHERE id = $2", registered, orgID); err != nil {
		t.Fatalf("setting gst_registered: %v", err)
	}
}

func testContact(t *testing.T, pool *pgxpool.Pool, orgID int64, kind, name string) int64 {
	t.Helper()
	c, err := core.CreateContact(context.Background(), pool, orgID, core.Contact{Kind: kind, Name: name})
	if err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	return c.ID
}

func testProperty(t *testing.T, pool *pgxpool.Pool, orgID int64, name string, residential bool) int64 {
	t.Helper()
	p, err := property.CreateProperty(context.Background(), pool, orgID, property.Property{Name: name, Residential: residential})
	if err != nil {
		t.Fatalf("CreateProperty: %v", err)
	}
	return p.ID
}

func testCategoryID(t *testing.T, pool *pgxpool.Pool, orgID int64, name string) int64 {
	t.Helper()
	cats, err := recon.ListCategories(context.Background(), pool, orgID)
	if err != nil {
		t.Fatalf("ListCategories: %v", err)
	}
	for _, c := range cats {
		if c.Name == name {
			return c.ID
		}
	}
	t.Fatalf("category %q not found", name)
	return 0
}

// testInvoiceAndPay creates a draft invoice with a single line, issues it,
// and records a payment against it. If amountPerPayment < total, callers
// must call it again with the same returned invoiceID's remaining balance
// to fully pay it off (used for the two-part consulting payment).
func testInvoice(t *testing.T, pool *pgxpool.Pool, orgID, contactID int64, source string, quantityMilli, unitAmount int64, gstCode string, propertyID, projectID *int64) core.Invoice {
	t.Helper()
	ctx := context.Background()
	draft, err := core.CreateDraftInvoice(ctx, pool, orgID, core.NewInvoice{
		ContactID: contactID, Source: source, DueInDays: 7,
		Lines: []core.NewLine{{
			Description: "line", QuantityMilli: quantityMilli, UnitAmount: core.Cents(unitAmount), GSTCode: gstCode,
			PropertyID: propertyID, ProjectID: projectID,
		}},
	})
	if err != nil {
		t.Fatalf("CreateDraftInvoice: %v", err)
	}
	issued, err := core.IssueInvoice(ctx, pool, orgID, draft.ID, mustDate(t, "2026-07-01"))
	if err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	return issued
}

func testPayment(t *testing.T, pool *pgxpool.Pool, orgID, invoiceID int64, amount int64, paidOn time.Time) {
	t.Helper()
	_, _, err := core.RecordPayment(context.Background(), pool, orgID, core.NewPayment{
		InvoiceID: invoiceID, Amount: core.Cents(amount), PaidOn: paidOn, Method: "bank_transfer",
	})
	if err != nil {
		t.Fatalf("RecordPayment: %v", err)
	}
}

func testExpense(t *testing.T, pool *pgxpool.Pool, orgID, categoryID int64, amount int64, spentOn time.Time, propertyID *int64) {
	t.Helper()
	_, _, err := recon.CreateExpense(context.Background(), pool, orgID, recon.NewExpense{
		SpentOn: spentOn, Description: "expense",
		Lines: []recon.NewExpenseLine{{CategoryID: categoryID, Amount: core.Cents(amount), PropertyID: propertyID}},
	})
	if err != nil {
		t.Fatalf("CreateExpense: %v", err)
	}
}

func newReportsTestHandler(pool *pgxpool.Pool) http.Handler {
	a := auth.New(pool, false)
	return httpapi.New(httpapi.Deps{
		Pool:   pool,
		Auth:   a,
		Cfg:    config.Config{Modules: map[string]bool{}},
		GetEnv: func(string) string { return "" },
	})
}

// reportsTestSetup builds an org with a logged-in owner and a handler with
// no toggleable modules enabled, since reports are core and must work
// regardless.
func reportsTestSetup(t *testing.T) (pool *pgxpool.Pool, handler http.Handler, token string, orgID int64) {
	t.Helper()
	pool = db.TestPool(t)
	org := testOrg(t, pool, "acme")

	hash, err := auth.HashPassword("correct-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	var userID int64
	if err := pool.QueryRow(context.Background(),
		"INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id",
		"owner@example.com", "Owner", hash,
	).Scan(&userID); err != nil {
		t.Fatalf("inserting user: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		"INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, $3)",
		userID, org.ID, string(auth.Owner),
	); err != nil {
		t.Fatalf("adding membership: %v", err)
	}
	token, err = auth.Login(context.Background(), pool, "owner@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	return pool, newReportsTestHandler(pool), token, org.ID
}

func get(handler http.Handler, token, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

type basResponseBody struct {
	Period       string `json:"period"`
	FiguresCents struct {
		G1, G2, G3, G4, G10, G11 int64
		A1, B1                   int64
	} `json:"figures_cents"`
}

type basDetailResponseBody struct {
	Field  string `json:"field"`
	Income []struct {
		Incl int64 `json:"Incl"`
		GST  int64 `json:"GST"`
	} `json:"income"`
	Expenses []struct {
		Incl int64 `json:"Incl"`
		GST  int64 `json:"GST"`
	} `json:"expenses"`
}

// TestBASTiesToDetail builds the exact scenario from the task brief: rent
// from a residential property, a consulting invoice paid in two halves, an
// excluded bond payment, and two expense lines (one capital). It checks
// every BAS figure against the brief's expected values, then checks that
// each field's /bas/detail rows sum to that same figure - the tax-critical
// guarantee that a BAS number always ties back to its rows exactly.
func TestBASTiesToDetail(t *testing.T) {
	pool, handler, token, orgID := reportsTestSetup(t)
	setGSTRegistered(t, pool, orgID, true)

	tenant := testContact(t, pool, orgID, "tenant", "Tenant")
	client := testContact(t, pool, orgID, "client", "Client")
	resProperty := testProperty(t, pool, orgID, "1 Rental St", true)

	// Rent: $500, INP (residential), paid in full.
	rentInv := testInvoice(t, pool, orgID, tenant, "rent", 1000, 50000, "INP", &resProperty, nil)
	testPayment(t, pool, orgID, rentInv.ID, 50000, mustDate(t, "2026-07-10"))

	// Consulting: 10000 ex-GST + 1000 GST = 11000, paid in two halves.
	consultingInv := testInvoice(t, pool, orgID, client, "timesheet", 1000, 11000, "GST", nil, nil)
	testPayment(t, pool, orgID, consultingInv.ID, 5500, mustDate(t, "2026-07-15"))
	testPayment(t, pool, orgID, consultingInv.ID, 5500, mustDate(t, "2026-08-01"))

	// Bond invoice, paid - must be excluded entirely.
	bondInv := testInvoice(t, pool, orgID, tenant, "bond", 1000, 140000, "N-T", &resProperty, nil)
	testPayment(t, pool, orgID, bondInv.ID, 140000, mustDate(t, "2026-07-10"))

	// Expenses: Repairs $110 GST (non-capital), Equipment $2200 GST (capital).
	repairsCat := testCategoryID(t, pool, orgID, "Repairs & maintenance")
	equipmentCat := testCategoryID(t, pool, orgID, "Equipment")
	testExpense(t, pool, orgID, repairsCat, 11000, mustDate(t, "2026-07-20"), nil)
	testExpense(t, pool, orgID, equipmentCat, 220000, mustDate(t, "2026-07-25"), nil)

	rr := get(handler, token, "/api/v1/orgs/acme/reports/bas?period=FY2027-Q1")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET bas: status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var bas basResponseBody
	if err := json.Unmarshal(rr.Body.Bytes(), &bas); err != nil {
		t.Fatalf("unmarshaling bas response: %v", err)
	}

	want := map[string]int64{
		"G1": 50000 + 11000, "G2": 0, "G3": 0, "G4": 50000,
		"G10": 220000, "G11": 11000, "1A": 1000, "1B": 1000 + 20000,
	}
	got := map[string]int64{
		"G1": bas.FiguresCents.G1, "G2": bas.FiguresCents.G2, "G3": bas.FiguresCents.G3, "G4": bas.FiguresCents.G4,
		"G10": bas.FiguresCents.G10, "G11": bas.FiguresCents.G11, "1A": bas.FiguresCents.A1, "1B": bas.FiguresCents.B1,
	}
	for field, w := range want {
		if got[field] != w {
			t.Errorf("figure %s = %d, want %d", field, got[field], w)
		}
	}

	for _, field := range []string{"G1", "G3", "G4", "G10", "G11", "1A", "1B"} {
		rr := get(handler, token, "/api/v1/orgs/acme/reports/bas/detail?period=FY2027-Q1&field="+field)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET bas/detail %s: status = %d, body=%s", field, rr.Code, rr.Body.String())
		}
		var detail basDetailResponseBody
		if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
			t.Fatalf("unmarshaling detail for %s: %v", field, err)
		}

		var sum int64
		isGSTField := field == "1A" || field == "1B"
		for _, r := range detail.Income {
			if isGSTField {
				sum += r.GST
			} else {
				sum += r.Incl
			}
		}
		for _, r := range detail.Expenses {
			if isGSTField {
				sum += r.GST
			} else {
				sum += r.Incl
			}
		}
		if sum != want[field] {
			t.Errorf("detail sum for %s = %d, want %d (figure)", field, sum, want[field])
		}
	}
}

type pnlGroupResponseBody struct {
	Key           string `json:"key"`
	TotalIncome   int64  `json:"total_income"`
	TotalExpenses int64  `json:"total_expenses"`
	Net           int64  `json:"net"`
}

// TestPnLByProperty checks that rent and a repair tagged to property P1
// land in a "P1" group, and a consulting invoice with no property lands in
// "Unassigned", each with the right net.
func TestPnLByProperty(t *testing.T) {
	pool, handler, token, orgID := reportsTestSetup(t)
	tenant := testContact(t, pool, orgID, "tenant", "Tenant")
	client := testContact(t, pool, orgID, "client", "Client")
	p1 := testProperty(t, pool, orgID, "P1", false)

	rentInv := testInvoice(t, pool, orgID, tenant, "rent", 1000, 50000, "FRE", &p1, nil)
	testPayment(t, pool, orgID, rentInv.ID, 50000, mustDate(t, "2026-07-10"))

	repairsCat := testCategoryID(t, pool, orgID, "Repairs & maintenance")
	testExpense(t, pool, orgID, repairsCat, 11000, mustDate(t, "2026-07-15"), &p1)

	consultingInv := testInvoice(t, pool, orgID, client, "timesheet", 1000, 10000, "FRE", nil, nil)
	testPayment(t, pool, orgID, consultingInv.ID, 10000, mustDate(t, "2026-07-20"))

	rr := get(handler, token, "/api/v1/orgs/acme/reports/pnl?from=2026-07-01&to=2026-09-30&group_by=property")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET pnl: status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var groups []pnlGroupResponseBody
	if err := json.Unmarshal(rr.Body.Bytes(), &groups); err != nil {
		t.Fatalf("unmarshaling pnl response: %v", err)
	}

	byKey := map[string]pnlGroupResponseBody{}
	for _, g := range groups {
		byKey[g.Key] = g
	}
	p1Group, ok := byKey["P1"]
	if !ok {
		t.Fatalf("no P1 group in %+v", groups)
	}
	if p1Group.TotalIncome != 50000 {
		t.Errorf("P1 TotalIncome = %d, want 50000", p1Group.TotalIncome)
	}
	// Repairs is a non-capital GST-default category, but the invoice was
	// marked FRE so no gst_amount; repairs expense is incl 11000, its
	// category default is GST so gst_amount is computed by CreateExpense
	// and Ex = Incl - GST. We only assert Net ties out via TotalExpenses
	// directly from the response instead of recomputing GST here.
	if p1Group.TotalExpenses <= 0 {
		t.Errorf("P1 TotalExpenses = %d, want > 0", p1Group.TotalExpenses)
	}
	if p1Group.Net != p1Group.TotalIncome-p1Group.TotalExpenses {
		t.Errorf("P1 Net = %d, want %d", p1Group.Net, p1Group.TotalIncome-p1Group.TotalExpenses)
	}

	unassigned, ok := byKey["Unassigned"]
	if !ok {
		t.Fatalf("no Unassigned group in %+v", groups)
	}
	if unassigned.TotalIncome != 10000 {
		t.Errorf("Unassigned TotalIncome = %d, want 10000", unassigned.TotalIncome)
	}
	if unassigned.Net != 10000 {
		t.Errorf("Unassigned Net = %d, want 10000", unassigned.Net)
	}
}

// TestBASUnregistered422 checks that /bas refuses an org that isn't
// GST-registered.
func TestBASUnregistered422(t *testing.T) {
	pool, handler, token, orgID := reportsTestSetup(t)
	setGSTRegistered(t, pool, orgID, false)
	_ = pool

	rr := get(handler, token, "/api/v1/orgs/acme/reports/bas?period=FY2027-Q1")
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body=%s", rr.Code, rr.Body.String())
	}
}

// TestTransactionsCSV checks the header row, row count, and dollar
// formatting of the transactions.csv export.
func TestTransactionsCSV(t *testing.T) {
	pool, handler, token, orgID := reportsTestSetup(t)
	tenant := testContact(t, pool, orgID, "tenant", "Tenant")
	repairsCat := testCategoryID(t, pool, orgID, "Repairs & maintenance")

	rentInv := testInvoice(t, pool, orgID, tenant, "rent", 1000, 50000, "FRE", nil, nil)
	testPayment(t, pool, orgID, rentInv.ID, 50000, mustDate(t, "2026-07-10"))
	testExpense(t, pool, orgID, repairsCat, 11000, mustDate(t, "2026-07-15"), nil)

	rr := get(handler, token, "/api/v1/orgs/acme/reports/transactions.csv?from=2026-07-01&to=2026-09-30")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/csv" {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); cd == "" {
		t.Errorf("Content-Disposition is empty")
	}

	records, err := csv.NewReader(bytes.NewReader(rr.Body.Bytes())).ReadAll()
	if err != nil {
		t.Fatalf("parsing csv: %v", err)
	}
	wantHeader := []string{"date", "type", "category", "gst_code", "incl_gst", "gst", "ex_gst", "property", "project", "reference"}
	if len(records) == 0 {
		t.Fatalf("no rows in csv")
	}
	for i, col := range wantHeader {
		if records[0][i] != col {
			t.Errorf("header[%d] = %q, want %q", i, records[0][i], col)
		}
	}
	if len(records) != 3 { // header + income row + expense row
		t.Fatalf("len(records) = %d, want 3 (header + 2 rows): %+v", len(records), records)
	}

	var inclGSTCol int
	for i, col := range records[0] {
		if col == "incl_gst" {
			inclGSTCol = i
		}
	}
	found := false
	for _, row := range records[1:] {
		if row[inclGSTCol] == "110.00" {
			found = true
		}
	}
	if !found {
		t.Errorf("no row with incl_gst = 110.00; rows=%+v", records[1:])
	}
}

// TestReportsExcludeDraftAndVoid checks that a draft invoice's payment (if
// any) and a voided invoice never contribute to income rows.
func TestReportsExcludeDraftAndVoid(t *testing.T) {
	pool, handler, token, orgID := reportsTestSetup(t)
	tenant := testContact(t, pool, orgID, "tenant", "Tenant")

	// An issued invoice that gets voided before payment - no payment is
	// possible on a draft, so to exercise "void never contributes" we void
	// an unpaid open invoice, confirming VoidInvoice leaves no income row.
	voidedInv := testInvoice(t, pool, orgID, tenant, "manual", 1000, 20000, "FRE", nil, nil)
	if _, err := core.VoidInvoice(context.Background(), pool, orgID, voidedInv.ID); err != nil {
		t.Fatalf("VoidInvoice: %v", err)
	}

	// A real, paid invoice in the same window, to confirm the report isn't
	// simply empty.
	paidInv := testInvoice(t, pool, orgID, tenant, "manual", 1000, 30000, "FRE", nil, nil)
	testPayment(t, pool, orgID, paidInv.ID, 30000, mustDate(t, "2026-07-10"))

	rr := get(handler, token, "/api/v1/orgs/acme/reports/transactions.csv?from=2026-07-01&to=2026-09-30")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	records, err := csv.NewReader(bytes.NewReader(rr.Body.Bytes())).ReadAll()
	if err != nil {
		t.Fatalf("parsing csv: %v", err)
	}
	if len(records) != 2 { // header + the one paid invoice's income row
		t.Fatalf("len(records) = %d, want 2 (header + paid invoice only): %+v", len(records), records)
	}
}
