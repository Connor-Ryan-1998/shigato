//go:build integration

// Package reports_test exercises the reports HTTP handlers end to end
// (through httpapi.New), so it needs to import httpapi, auth, property,
// timesheets and recon alongside reports without an import cycle: reports
// itself must not import property/timesheets/recon (they're toggleable
// modules), but its tests can use them - and billing/payments' Fake - to
// set up fixtures.
package reports_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/config"
	"shigoto/internal/core"
	"shigoto/internal/importers"
	"shigoto/internal/module"
	"shigoto/internal/payments"
	"shigoto/internal/recon"
	"shigoto/internal/reports"
	"shigoto/internal/testkit"
)

func TestLargePaymentReports(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("large")
	e.Settings(org, testkit.GSTRegistered(true))
	contactID := e.Contact(org, testkit.Name("Large client")).ID
	inv, err := core.CreateDraftInvoice(ctx, pool, org.ID, core.NewInvoice{
		ContactID: contactID, Source: "manual", DueInDays: 7,
		Lines: []core.NewLine{
			{Description: "Taxable", QuantityMilli: 1000, UnitAmount: 3_000_000_000, GSTCode: "GST"},
			{Description: "GST free", QuantityMilli: 1000, UnitAmount: 3_300_000_000, GSTCode: "FRE"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	date := testkit.Date("2026-07-01")
	if _, err := core.IssueInvoice(ctx, pool, org.ID, inv.ID, date); err != nil {
		t.Fatal(err)
	}
	testPayment(t, pool, org.ID, inv.ID, 6_600_000_000, date)
	rows, err := reports.IncomeRows(ctx, pool, org.ID, date, date)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("income rows = %d, want 2", len(rows))
	}
	if rows[0].Incl != 3_300_000_000 || rows[0].GST != 300_000_000 || rows[1].Incl != 3_300_000_000 || rows[1].GST != 0 {
		t.Errorf("income allocation = %+v", rows)
	}
	bas := reports.BAS(rows, nil)
	if bas.G1 != 6_600_000_000 || bas.G3 != 3_300_000_000 || bas.A1 != 300_000_000 {
		t.Errorf("BAS = %+v", bas)
	}
	pnl := reports.PnL(rows, nil, "org")
	if len(pnl) != 1 || pnl[0].Net != 6_300_000_000 {
		t.Errorf("P&L = %+v", pnl)
	}
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

// noModules disables every toggleable module: reports are core and must
// work regardless.
func noModules() testkit.HandlerOption {
	off := map[string]bool{}
	for _, name := range config.Known {
		off[name] = false
	}
	return testkit.Modules(off)
}

// basFiguresBody mirrors the API's snake_case/"1a"/"1b" BAS figure keys
// (see reports.BASFigures.MarshalJSON), used for both figures_cents and
// figures_dollars.
type basFiguresBody struct {
	G1  int64 `json:"g1"`
	G2  int64 `json:"g2"`
	G3  int64 `json:"g3"`
	G4  int64 `json:"g4"`
	G10 int64 `json:"g10"`
	G11 int64 `json:"g11"`
	A1  int64 `json:"1a"`
	B1  int64 `json:"1b"`
}

type basResponseBody struct {
	Period         string         `json:"period"`
	FiguresCents   basFiguresBody `json:"figures_cents"`
	FiguresDollars basFiguresBody `json:"figures_dollars"`
}

type basDetailResponseBody struct {
	Field  string `json:"field"`
	Income []struct {
		InclGST int64 `json:"incl_gst"`
		GST     int64 `json:"gst"`
	} `json:"income"`
	Expenses []struct {
		InclGST int64 `json:"incl_gst"`
		GST     int64 `json:"gst"`
	} `json:"expenses"`
}

// TestBASTiesToDetail builds the exact scenario from the task brief: rent
// from a residential property, a consulting invoice paid in two halves, an
// excluded bond payment, and two expense lines (one capital). It checks
// every BAS figure against the brief's expected values, then checks that
// each field's /bas/detail rows sum to that same figure - the tax-critical
// guarantee that a BAS number always ties back to its rows exactly.
func TestBASTiesToDetail(t *testing.T) {
	e := testkit.New(t)
	pool := e.Pool
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler(noModules()))
	e.Settings(org, testkit.GSTRegistered(true))

	tenant := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant")).ID
	client := e.Contact(org, testkit.Name("Client")).ID
	resProperty := e.Property(org, testkit.PropertyName("1 Rental St"), testkit.Residential()).ID

	// Rent: $500, INP (residential), paid in full.
	rentInv := e.Invoice(org, tenant, testkit.Source("rent"), testkit.Amount(50000), testkit.InvoiceGST("INP"), testkit.LineProperty(resProperty), testkit.Issued(testkit.Date("2026-07-01")))
	testPayment(t, pool, org.ID, rentInv.ID, 50000, testkit.Date("2026-07-10"))

	// Consulting: 10000 ex-GST + 1000 GST = 11000, paid in two halves.
	consultingInv := e.Invoice(org, client, testkit.Source("timesheet"), testkit.Amount(11000), testkit.InvoiceGST("GST"), testkit.Issued(testkit.Date("2026-07-01")))
	testPayment(t, pool, org.ID, consultingInv.ID, 5500, testkit.Date("2026-07-15"))
	testPayment(t, pool, org.ID, consultingInv.ID, 5500, testkit.Date("2026-08-01"))

	// Bond invoice, paid - must be excluded entirely.
	bondInv := e.Invoice(org, tenant, testkit.Source("bond"), testkit.Amount(140000), testkit.InvoiceGST("N-T"), testkit.LineProperty(resProperty), testkit.Issued(testkit.Date("2026-07-01")))
	testPayment(t, pool, org.ID, bondInv.ID, 140000, testkit.Date("2026-07-10"))

	// Expenses: Repairs $110 GST (non-capital), Equipment $2200 GST (capital).
	repairsCat := testCategoryID(t, pool, org.ID, "Repairs & maintenance")
	equipmentCat := testCategoryID(t, pool, org.ID, "Equipment")
	testExpense(t, pool, org.ID, repairsCat, 11000, testkit.Date("2026-07-20"), nil)
	testExpense(t, pool, org.ID, equipmentCat, 220000, testkit.Date("2026-07-25"), nil)

	rr := s.Do(http.MethodGet, "/api/v1/orgs/acme/reports/bas?period=FY2027-Q1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET bas: status = %d, body=%s", rr.Code, rr.Body)
	}
	var bas basResponseBody
	if err := json.Unmarshal(rr.Body, &bas); err != nil {
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
		rr := s.Do(http.MethodGet, "/api/v1/orgs/acme/reports/bas/detail?period=FY2027-Q1&field="+field, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET bas/detail %s: status = %d, body=%s", field, rr.Code, rr.Body)
		}
		var detail basDetailResponseBody
		if err := json.Unmarshal(rr.Body, &detail); err != nil {
			t.Fatalf("unmarshaling detail for %s: %v", field, err)
		}

		var sum int64
		isGSTField := field == "1A" || field == "1B"
		for _, r := range detail.Income {
			if isGSTField {
				sum += r.GST
			} else {
				sum += r.InclGST
			}
		}
		for _, r := range detail.Expenses {
			if isGSTField {
				sum += r.GST
			} else {
				sum += r.InclGST
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
	e := testkit.New(t)
	pool := e.Pool
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler(noModules()))
	e.Settings(org, testkit.GSTRegistered(true))
	tenant := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant")).ID
	client := e.Contact(org, testkit.Name("Client")).ID
	p1 := e.Property(org, testkit.PropertyName("P1"), testkit.Commercial()).ID

	rentInv := e.Invoice(org, tenant, testkit.Source("rent"), testkit.Amount(50000), testkit.InvoiceGST("FRE"), testkit.LineProperty(p1), testkit.Issued(testkit.Date("2026-07-01")))
	testPayment(t, pool, org.ID, rentInv.ID, 50000, testkit.Date("2026-07-10"))

	// Repairs & maintenance defaults to GST; with the org GST-registered,
	// an incl-GST amount of 11000 splits into gst_amount
	// GSTInInclusive(11000, GST) = round(11000/11) = 1000, so ex-GST (what
	// PnL sums) is 11000 - 1000 = 10000.
	repairsCat := testCategoryID(t, pool, org.ID, "Repairs & maintenance")
	testExpense(t, pool, org.ID, repairsCat, 11000, testkit.Date("2026-07-15"), &p1)

	consultingInv := e.Invoice(org, client, testkit.Source("timesheet"), testkit.Amount(10000), testkit.InvoiceGST("FRE"), testkit.Issued(testkit.Date("2026-07-01")))
	testPayment(t, pool, org.ID, consultingInv.ID, 10000, testkit.Date("2026-07-20"))

	rr := s.Do(http.MethodGet, "/api/v1/orgs/acme/reports/pnl?from=2026-07-01&to=2026-09-30&group_by=property", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET pnl: status = %d, body=%s", rr.Code, rr.Body)
	}
	var groups []pnlGroupResponseBody
	if err := json.Unmarshal(rr.Body, &groups); err != nil {
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
	if p1Group.TotalExpenses != 10000 {
		t.Errorf("P1 TotalExpenses = %d, want 10000", p1Group.TotalExpenses)
	}
	if p1Group.Net != 40000 {
		t.Errorf("P1 Net = %d, want 40000", p1Group.Net)
	}

	unassigned, ok := byKey["Unassigned"]
	if !ok {
		t.Fatalf("no Unassigned group in %+v", groups)
	}
	if unassigned.TotalIncome != 10000 {
		t.Errorf("Unassigned TotalIncome = %d, want 10000", unassigned.TotalIncome)
	}
	if unassigned.TotalExpenses != 0 {
		t.Errorf("Unassigned TotalExpenses = %d, want 0", unassigned.TotalExpenses)
	}
	if unassigned.Net != 10000 {
		t.Errorf("Unassigned Net = %d, want 10000", unassigned.Net)
	}
}

// TestTransactionsCSV checks the header row, row count, and dollar
// formatting of the transactions.csv export.
func TestTransactionsCSV(t *testing.T) {
	e := testkit.New(t)
	pool := e.Pool
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler(noModules()))
	tenant := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant")).ID
	repairsCat := testCategoryID(t, pool, org.ID, "Repairs & maintenance")

	rentInv := e.Invoice(org, tenant, testkit.Source("rent"), testkit.Amount(50000), testkit.InvoiceGST("FRE"), testkit.Issued(testkit.Date("2026-07-01")))
	testPayment(t, pool, org.ID, rentInv.ID, 50000, testkit.Date("2026-07-10"))
	testExpense(t, pool, org.ID, repairsCat, 11000, testkit.Date("2026-07-15"), nil)

	rr := s.Do(http.MethodGet, "/api/v1/orgs/acme/reports/transactions.csv?from=2026-07-01&to=2026-09-30", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body)
	}
	if ct := rr.Header.Get("Content-Type"); ct != "text/csv" {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
	if cd := rr.Header.Get("Content-Disposition"); cd == "" {
		t.Errorf("Content-Disposition is empty")
	}

	records, err := csv.NewReader(bytes.NewReader(rr.Body)).ReadAll()
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

// rawInsertPayment inserts a payment row directly via SQL, bypassing
// core.RecordPayment's own draft/void guard - used to prove the report
// query's own status filter holds even against data that shouldn't exist
// in practice (RecordPayment itself refuses to record against a draft).
func rawInsertPayment(t *testing.T, pool *pgxpool.Pool, orgID, invoiceID int64, amount int64, paidOn time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO payments (org_id, invoice_id, amount, paid_on, method) VALUES ($1, $2, $3, $4, 'bank_transfer')`,
		orgID, invoiceID, amount, paidOn,
	); err != nil {
		t.Fatalf("inserting raw payment: %v", err)
	}
}

// forceVoid sets an invoice's status to 'void' directly via SQL, bypassing
// core.VoidInvoice's own payment-count guard - used to produce a voided
// invoice that still has a recorded payment (the state the reports status
// filter must exclude), which VoidInvoice itself refuses to create.
func forceVoid(t *testing.T, pool *pgxpool.Pool, invoiceID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE invoices SET status = 'void' WHERE id = $1`, invoiceID); err != nil {
		t.Fatalf("forcing void: %v", err)
	}
}

// TestReportsExcludeDraftAndVoid checks that a paid-then-voided invoice and
// a draft invoice with a payment recorded directly against it (both states
// core itself refuses to produce, but the report query's own status filter
// must still exclude) never appear in transactions.csv or count toward
// BAS G1/1A.
func TestReportsExcludeDraftAndVoid(t *testing.T) {
	e := testkit.New(t)
	pool := e.Pool
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler(noModules()))
	e.Settings(org, testkit.GSTRegistered(true))
	tenant := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant")).ID

	// Issued, paid in full (22000 = 20000 ex-GST + 2000 GST), then forced
	// void via SQL - a state VoidInvoice itself refuses (it checks for
	// existing payments), but the reports query must still exclude by
	// status alone.
	voidedInv := e.Invoice(org, tenant, testkit.Source("manual"), testkit.Amount(20000), testkit.InvoiceGST("GST"), testkit.Issued(testkit.Date("2026-07-01")))
	testPayment(t, pool, org.ID, voidedInv.ID, 22000, testkit.Date("2026-07-05"))
	forceVoid(t, pool, voidedInv.ID)

	// A draft invoice (never issued) with a payment row inserted directly -
	// core.RecordPayment refuses this itself, but the reports query must
	// still exclude it by status alone.
	draft, err := core.CreateDraftInvoice(e.Ctx, pool, org.ID, core.NewInvoice{
		ContactID: tenant, Source: "manual", DueInDays: 7,
		Lines: []core.NewLine{{Description: "line", QuantityMilli: 1000, UnitAmount: 5000, GSTCode: "GST"}},
	})
	if err != nil {
		t.Fatalf("CreateDraftInvoice: %v", err)
	}
	rawInsertPayment(t, pool, org.ID, draft.ID, 5500, testkit.Date("2026-07-06"))

	// A real, paid invoice (FRE, so it contributes to G1 but not 1A) in the
	// same window, giving G1/1A a known value the excluded rows would
	// otherwise inflate.
	paidInv := e.Invoice(org, tenant, testkit.Source("manual"), testkit.Amount(30000), testkit.InvoiceGST("FRE"), testkit.Issued(testkit.Date("2026-07-01")))
	testPayment(t, pool, org.ID, paidInv.ID, 30000, testkit.Date("2026-07-10"))

	rr := s.Do(http.MethodGet, "/api/v1/orgs/acme/reports/transactions.csv?from=2026-07-01&to=2026-09-30", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body)
	}
	records, err := csv.NewReader(bytes.NewReader(rr.Body)).ReadAll()
	if err != nil {
		t.Fatalf("parsing csv: %v", err)
	}
	if len(records) != 2 { // header + the one paid invoice's income row
		t.Fatalf("len(records) = %d, want 2 (header + paid invoice only): %+v", len(records), records)
	}

	rr = s.Do(http.MethodGet, "/api/v1/orgs/acme/reports/bas?period=FY2027-Q1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET bas: status = %d, body=%s", rr.Code, rr.Body)
	}
	var bas basResponseBody
	if err := json.Unmarshal(rr.Body, &bas); err != nil {
		t.Fatalf("unmarshaling bas response: %v", err)
	}
	if bas.FiguresCents.G1 != 30000 {
		t.Errorf("G1 = %d, want 30000 (the draft/voided payments must not count)", bas.FiguresCents.G1)
	}
	if bas.FiguresCents.A1 != 0 {
		t.Errorf("1A = %d, want 0 (the draft/voided GST must not count)", bas.FiguresCents.A1)
	}
}

// reportTotals is the subset of a quarter's figures the exactly-once test
// checks: BAS G1/1A/G11/1B plus P&L (ex-GST) income and expenses for the
// whole org, along with how many rows fed them.
type reportTotals struct {
	G1, A1, G11, B1             core.Cents
	PnLIncome, PnLExpenses      core.Cents
	IncomeRows, ExpenseRowCount int
}

func quarterTotals(t *testing.T, pool *pgxpool.Pool, orgID int64) reportTotals {
	t.Helper()
	ctx := context.Background()
	from, to := testkit.Date("2026-07-01"), testkit.Date("2026-09-30")
	income, err := reports.IncomeRows(ctx, pool, orgID, from, to)
	if err != nil {
		t.Fatalf("IncomeRows: %v", err)
	}
	exp, err := reports.ExpenseRows(ctx, pool, orgID, from, to)
	if err != nil {
		t.Fatalf("ExpenseRows: %v", err)
	}
	bas := reports.BAS(income, exp)
	got := reportTotals{G1: bas.G1, A1: bas.A1, G11: bas.G11, B1: bas.B1, IncomeRows: len(income), ExpenseRowCount: len(exp)}
	for _, g := range reports.PnL(income, exp, "org") {
		got.PnLIncome += g.TotalIncome
		got.PnLExpenses += g.TotalExpenses
	}
	return got
}

// TestReconciledMovementsCountExactlyOnce drives every reconciliation path
// in one GST-registered quarter and checks each money movement reaches BAS
// and the P&L exactly once:
//   - invoice A ($220, GST $20) paid by a bank credit via MatchInvoice;
//   - invoice B ($110, GST $10) paid by a Stripe webhook payment, then
//     reconciled via MatchPayout, which books a $1.10 (GST $0.10) fee;
//   - an ignored $50 debit (drawings) - never counted;
//   - a $33 (GST $3) expense created from a debit line, plus a standalone
//     $55 (GST $5) expense.
//
// It then unmatches the bank match and the payout: payment A (created by
// the match) disappears, payment B stays (it came from the webhook, so it's
// still income), and the fee expense disappears - so the figures move by
// exactly those amounts and nothing else.
func TestReconciledMovementsCountExactlyOnce(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	e.Settings(org, testkit.GSTRegistered(true))
	client := e.Contact(org, testkit.Name("Client")).ID
	repairs := testCategoryID(t, pool, org.ID, "Repairs & maintenance")

	invA := e.Invoice(org, client, testkit.Source("manual"), testkit.Amount(22000), testkit.InvoiceGST("GST"), testkit.Issued(testkit.Date("2026-07-01")))
	invB := e.Invoice(org, client, testkit.Source("timesheet"), testkit.Amount(11000), testkit.InvoiceGST("GST"), testkit.Issued(testkit.Date("2026-07-01")))
	if _, _, err := core.RecordPayment(ctx, pool, org.ID, core.NewPayment{
		InvoiceID: invB.ID, Amount: 11000, PaidOn: testkit.Date("2026-07-12"), Method: "stripe", ProviderRef: "pi_b",
	}); err != nil {
		t.Fatalf("RecordPayment (webhook): %v", err)
	}

	mapping, _ := importers.Preset("commbank")
	account, err := recon.CreateBankAccount(ctx, pool, org.ID, "Everyday", mapping)
	if err != nil {
		t.Fatalf("CreateBankAccount: %v", err)
	}
	statement := "10/07/2026,220.00,Transfer from client\n" +
		"15/07/2026,108.90,Stripe payout\n" +
		"20/07/2026,-50.00,Owner drawings\n" +
		"22/07/2026,-33.00,Plumber\n"
	if _, err := recon.Import(ctx, pool, org.ID, account.ID, strings.NewReader(statement)); err != nil {
		t.Fatalf("Import: %v", err)
	}
	lines, _, err := recon.ListBankLines(ctx, pool, org.ID, recon.BankLineFilter{})
	if err != nil {
		t.Fatalf("ListBankLines: %v", err)
	}
	lineID := map[string]int64{}
	for _, l := range lines {
		lineID[l.Description] = l.ID
	}

	run := func(name string, fn func(tx pgx.Tx) error) {
		t.Helper()
		if err := module.RunInTx(ctx, pool, fn); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	run("MatchInvoice", func(tx pgx.Tx) error {
		_, err := recon.MatchInvoice(ctx, tx, org.ID, lineID["Transfer from client"], invA.ID, "bank_transfer")
		return err
	})
	payoutOn := testkit.Date("2026-07-15")
	fake := &payments.Fake{
		Payouts: map[string]payments.Payout{
			"po_b": {Ref: "po_b", Amount: 10890, ArrivalOn: payoutOn, Status: "paid"},
		},
		Transactions: map[string][]payments.BalanceTxn{
			"po_b": {{Ref: "txn_b", Type: "charge", PaymentRef: "pi_b", Amount: 11000, Fee: 110}},
		},
	}
	b := e.BillingService(testkit.Provider(fake))
	if _, err := recon.MatchPayout(ctx, pool, b, org.ID, "acme", lineID["Stripe payout"], "po_b"); err != nil {
		t.Fatalf("MatchPayout: %v", err)
	}
	run("Ignore", func(tx pgx.Tx) error {
		return recon.Ignore(ctx, tx, org.ID, lineID["Owner drawings"], "drawings")
	})
	run("CreateExpenseFromLine", func(tx pgx.Tx) error {
		_, _, err := recon.CreateExpenseFromLine(ctx, tx, org.ID, lineID["Plumber"], recon.NewExpense{
			Description: "Plumber", Lines: []recon.NewExpenseLine{{CategoryID: repairs, Amount: 3300}},
		})
		return err
	})
	testExpense(t, pool, org.ID, repairs, 5500, testkit.Date("2026-07-25"), nil)

	want := reportTotals{
		G1: 22000 + 11000, A1: 2000 + 1000,
		G11: 110 + 3300 + 5500, B1: 10 + 300 + 500,
		PnLIncome: 20000 + 10000, PnLExpenses: 100 + 3000 + 5000,
		IncomeRows: 2, ExpenseRowCount: 3,
	}
	if got := quarterTotals(t, pool, org.ID); got != want {
		t.Fatalf("after reconciling:\n got  %+v\n want %+v", got, want)
	}

	run("Unmatch bank match", func(tx pgx.Tx) error {
		return recon.Unmatch(ctx, tx, org.ID, lineID["Transfer from client"])
	})
	run("Unmatch payout", func(tx pgx.Tx) error {
		return recon.Unmatch(ctx, tx, org.ID, lineID["Stripe payout"])
	})

	want.G1 -= 22000
	want.A1 -= 2000
	want.PnLIncome -= 20000
	want.IncomeRows--
	want.G11 -= 110
	want.B1 -= 10
	want.PnLExpenses -= 100
	want.ExpenseRowCount--
	if got := quarterTotals(t, pool, org.ID); got != want {
		t.Fatalf("after unmatching:\n got  %+v\n want %+v", got, want)
	}
}
