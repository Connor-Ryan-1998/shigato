//go:build integration

package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"shigoto/internal/core"
	"shigoto/internal/httpapi"
	"shigoto/internal/importers"
	"shigoto/internal/recon"
	"shigoto/internal/testkit"
)

// backdateDueDate pushes invoiceID's due_date days into the past, so tests
// can create an overdue invoice without a negative due_in_days (which
// CreateDraftInvoice rejects).
func backdateDueDate(e *testkit.Env, org core.Org, invoiceID int64, days int) {
	e.T.Helper()
	if _, err := e.Pool.Exec(e.Ctx,
		"UPDATE invoices SET due_date = due_date - make_interval(days => $1) WHERE org_id = $2 AND id = $3",
		days, org.ID, invoiceID,
	); err != nil {
		e.T.Fatalf("backdating due_date: %v", err)
	}
}

// issuedDueIn creates and issues today a one-line, GST-free manual invoice
// for totalCents, then moves its due date to dueInDays after the issue
// date (testkit drafts use a 14-day term).
func issuedDueIn(e *testkit.Env, org core.Org, contactID, totalCents int64, dueInDays int) core.Invoice {
	e.T.Helper()
	inv := e.Invoice(org, contactID, testkit.Amount(totalCents), testkit.InvoiceGST("N-T"), testkit.Source("manual"), testkit.Issued(time.Now()))
	if dueInDays != 14 {
		backdateDueDate(e, org, inv.ID, 14-dueInDays)
	}
	return inv
}

// recordPayment records a cash payment of amount on invoiceID, paid today.
func recordPayment(e *testkit.Env, org core.Org, invoiceID int64, amount core.Cents) {
	e.T.Helper()
	if _, _, err := core.RecordPayment(e.Ctx, e.Pool, org.ID, core.NewPayment{
		InvoiceID: invoiceID, Amount: amount, PaidOn: time.Now(), Method: "cash",
	}); err != nil {
		e.T.Fatalf("RecordPayment: %v", err)
	}
}

type dashboardCoreBody struct {
	OpenCount          int   `json:"open_count"`
	OpenOutstanding    int64 `json:"open_outstanding"`
	OverdueCount       int   `json:"overdue_count"`
	OverdueOutstanding int64 `json:"overdue_outstanding"`
	DraftCount         int   `json:"draft_count"`
	InvoiceCount       int   `json:"invoice_count"`
}

type dashboardBody struct {
	Core    dashboardCoreBody         `json:"core"`
	Modules map[string]map[string]any `json:"modules"`
}

func decodeDashboard(t *testing.T, rr testkit.Response) dashboardBody {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body)
	}
	var body dashboardBody
	if err := json.Unmarshal(rr.Body, &body); err != nil {
		t.Fatalf("decoding dashboard response %s: %v", rr.Body, err)
	}
	return body
}

// TestDashboardCore covers the dashboard's core (invoices) section: one
// draft, one open invoice not yet due, one open invoice overdue with a
// partial payment, and one fully paid invoice.
func TestDashboardCore(t *testing.T) {
	e := testkit.New(t)
	handler := e.Handler(testkit.OnlyModules())
	org, owner := e.OwnerSession("acme", handler)
	contact := e.Contact(org)

	// draft
	e.Invoice(org, contact.ID, testkit.Amount(1000), testkit.InvoiceGST("N-T"), testkit.Source("manual"))

	// open, not yet due
	issuedDueIn(e, org, contact.ID, 5000, 30)

	// open, overdue, with a partial payment
	overdue := issuedDueIn(e, org, contact.ID, 10000, 0)
	backdateDueDate(e, org, overdue.ID, 5)
	recordPayment(e, org, overdue.ID, 4000)

	// paid in full
	paid := issuedDueIn(e, org, contact.ID, 2000, 14)
	recordPayment(e, org, paid.ID, 2000)

	body := decodeDashboard(t, owner.Do(http.MethodGet, "/api/v1/orgs/acme/dashboard", nil))
	if body.Core.DraftCount != 1 {
		t.Fatalf("draft_count = %d, want 1", body.Core.DraftCount)
	}
	if body.Core.OpenCount != 2 {
		t.Fatalf("open_count = %d, want 2", body.Core.OpenCount)
	}
	if body.Core.OverdueCount != 1 {
		t.Fatalf("overdue_count = %d, want 1", body.Core.OverdueCount)
	}
	if body.Core.OverdueOutstanding != 6000 {
		t.Fatalf("overdue_outstanding = %d, want 6000 (10000 total - 4000 partial)", body.Core.OverdueOutstanding)
	}
	if body.Core.InvoiceCount != 4 {
		t.Fatalf("invoice_count = %d, want 4 (draft, open, overdue, paid)", body.Core.InvoiceCount)
	}
	if len(body.Modules) != 0 {
		t.Fatalf("modules = %v, want none (no modules enabled)", body.Modules)
	}
}

// TestDashboardInvoiceCountIncludesSettledInvoices proves invoice_count
// counts every invoice regardless of status, not just open/overdue/draft -
// an org whose only invoice is fully paid has open_count/overdue_count/
// draft_count all 0, but invoice_count must still be 1, so the web
// dashboard can tell "no invoices at all" apart from "fully settled"
// (see internal/httpapi/dashboard.go's coreDashboard.InvoiceCount doc).
func TestDashboardInvoiceCountIncludesSettledInvoices(t *testing.T) {
	e := testkit.New(t)
	handler := e.Handler(testkit.OnlyModules())
	org, owner := e.OwnerSession("acme", handler)
	contact := e.Contact(org)

	paid := issuedDueIn(e, org, contact.ID, 2000, 14)
	recordPayment(e, org, paid.ID, 2000)

	body := decodeDashboard(t, owner.Do(http.MethodGet, "/api/v1/orgs/acme/dashboard", nil))
	if body.Core.OpenCount != 0 || body.Core.OverdueCount != 0 || body.Core.DraftCount != 0 {
		t.Fatalf("core = %+v, want open/overdue/draft all 0 (invoice is paid)", body.Core)
	}
	if body.Core.InvoiceCount != 1 {
		t.Fatalf("invoice_count = %d, want 1 (the paid invoice still counts)", body.Core.InvoiceCount)
	}
}

// TestDashboardOmitsDisabledModule checks that only enabled modules appear
// under "modules", even for a role (owner) that holds every permission.
func TestDashboardOmitsDisabledModule(t *testing.T) {
	e := testkit.New(t)
	handler := e.Handler(testkit.OnlyModules("timesheets"))
	_, owner := e.OwnerSession("acme", handler)

	body := decodeDashboard(t, owner.Do(http.MethodGet, "/api/v1/orgs/acme/dashboard", nil))
	if len(body.Modules) != 1 {
		t.Fatalf("modules = %v, want exactly one key", body.Modules)
	}
	if _, ok := body.Modules["timesheets"]; !ok {
		t.Fatalf("modules = %v, want a timesheets key", body.Modules)
	}
}

// TestInvoiceListFiltersAndTotal covers GET /invoices' status, limit and
// overdue filters, and that "total" always reflects the full matching
// count regardless of limit.
func TestInvoiceListFiltersAndTotal(t *testing.T) {
	e := testkit.New(t)
	handler := e.Handler(testkit.OnlyModules())
	org, owner := e.OwnerSession("acme", handler)
	contact := e.Contact(org)

	e.Invoice(org, contact.ID, testkit.Amount(1000), testkit.InvoiceGST("N-T"), testkit.Source("manual"))
	issuedDueIn(e, org, contact.ID, 5000, 30) // open, not due
	overdue := issuedDueIn(e, org, contact.ID, 7000, 0)
	backdateDueDate(e, org, overdue.ID, 5)

	listInvoicesVia := func(query string) httpapi.InvoiceListResponse {
		rr := owner.Do(http.MethodGet, "/api/v1/orgs/acme/invoices"+query, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET /invoices%s: status = %d, want 200, body=%s", query, rr.Code, rr.Body)
		}
		var body httpapi.InvoiceListResponse
		if err := json.Unmarshal(rr.Body, &body); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		return body
	}

	open := listInvoicesVia("?status=open")
	if open.Total != 2 || len(open.Invoices) != 2 {
		t.Fatalf("status=open: invoices=%+v, total=%d, want 2 and 2", open.Invoices, open.Total)
	}

	limited := listInvoicesVia("?status=open&limit=1")
	if limited.Total != 2 || len(limited.Invoices) != 1 {
		t.Fatalf("limit=1: invoices=%+v, total=%d, want 1 invoice and total 2", limited.Invoices, limited.Total)
	}

	overdueOnly := listInvoicesVia("?overdue=true")
	if overdueOnly.Total != 1 || len(overdueOnly.Invoices) != 1 || overdueOnly.Invoices[0].ID != overdue.ID {
		t.Fatalf("overdue=true: invoices=%+v, total=%d, want only invoice %d", overdueOnly.Invoices, overdueOnly.Total, overdue.ID)
	}
}

// TestInvoiceDetailIncludesPaymentsAndProviderURL covers GET
// /invoices/{id}'s detail-only fields: the payments list (with no
// provider_ref leaked) and provider_url, present as "" when the invoice
// never reached a payment provider (GetEnv returns "" so no org ever
// resolves one). GET /invoices (the list) must not carry either field.
func TestInvoiceDetailIncludesPaymentsAndProviderURL(t *testing.T) {
	e := testkit.New(t)
	handler := e.Handler(testkit.OnlyModules())
	org, owner := e.OwnerSession("acme", handler)
	contact := e.Contact(org)

	inv := issuedDueIn(e, org, contact.ID, 10000, 14)
	recordPayment(e, org, inv.ID, 4000)

	rr := owner.Do(http.MethodGet, "/api/v1/orgs/acme/invoices/"+strconv.FormatInt(inv.ID, 10), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /invoices/%d: status = %d, want 200, body=%s", inv.ID, rr.Code, rr.Body)
	}
	rawBody := string(rr.Body)
	if strings.Contains(rawBody, "provider_ref") {
		t.Fatalf("detail response leaked provider_ref: %s", rawBody)
	}

	var body struct {
		Payments []struct {
			ID     int64  `json:"id"`
			Amount int64  `json:"amount"`
			PaidOn string `json:"paid_on"`
			Method string `json:"method"`
		} `json:"payments"`
		ProviderURL string `json:"provider_url"`
	}
	if err := json.Unmarshal([]byte(rawBody), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(body.Payments) != 1 {
		t.Fatalf("payments = %+v, want exactly 1", body.Payments)
	}
	payment := body.Payments[0]
	if payment.Amount != 4000 || payment.Method != "cash" {
		t.Fatalf("payment = %+v, want amount=4000 method=cash", payment)
	}
	if body.ProviderURL != "" {
		t.Fatalf("provider_url = %q, want empty (no provider configured in this test)", body.ProviderURL)
	}

	// GET /invoices (the list) must not carry payments/provider_url at all.
	listRR := owner.Do(http.MethodGet, "/api/v1/orgs/acme/invoices", nil)
	if listRR.Code != http.StatusOK {
		t.Fatalf("GET /invoices: status = %d, want 200, body=%s", listRR.Code, listRR.Body)
	}
	if strings.Contains(string(listRR.Body), "payments") || strings.Contains(string(listRR.Body), "provider_url") {
		t.Fatalf("list response leaked detail-only fields: %s", listRR.Body)
	}
}

// TestBankLineMatchLabel covers the bank-line JSON's "match" field for a
// line matched to an invoice: kind "invoice", with the invoice's number as
// the label.
func TestBankLineMatchLabel(t *testing.T) {
	e := testkit.New(t)
	handler := e.Handler(testkit.OnlyModules("reconciliation"))
	org, owner := e.OwnerSession("acme", handler)
	contact := e.Contact(org)
	ctx := e.Ctx

	mapping, ok := importers.Preset("commbank")
	if !ok {
		t.Fatal("commbank preset not found")
	}
	account, err := recon.CreateBankAccount(ctx, e.Pool, org.ID, "Everyday", mapping)
	if err != nil {
		t.Fatalf("CreateBankAccount: %v", err)
	}
	if _, err := recon.Import(ctx, e.Pool, org.ID, account.ID, strings.NewReader("01/03/2026,100.00,Payment received\n")); err != nil {
		t.Fatalf("Import: %v", err)
	}
	lines, _, err := recon.ListBankLines(ctx, e.Pool, org.ID, recon.BankLineFilter{})
	if err != nil || len(lines) != 1 {
		t.Fatalf("ListBankLines: lines=%+v, err=%v", lines, err)
	}
	lineID := lines[0].ID

	inv := issuedDueIn(e, org, contact.ID, 10000, 14)
	if inv.Number == nil {
		t.Fatal("issued invoice has no number")
	}

	tx, err := e.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := recon.MatchInvoice(ctx, tx, org.ID, lineID, inv.ID, "bank_transfer"); err != nil {
		t.Fatalf("MatchInvoice: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	rr := owner.Do(http.MethodGet, "/api/v1/orgs/acme/bank-lines", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body)
	}

	var body struct {
		Lines []struct {
			ID    int64 `json:"id"`
			Match *struct {
				Kind  string `json:"kind"`
				ID    int64  `json:"id"`
				Label string `json:"label"`
			} `json:"match"`
		} `json:"lines"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rr.Body, &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if body.Total != 1 || len(body.Lines) != 1 {
		t.Fatalf("lines = %+v, total = %d, want 1 and 1", body.Lines, body.Total)
	}
	match := body.Lines[0].Match
	if match == nil || match.Kind != "invoice" {
		t.Fatalf("match = %+v, want kind invoice", match)
	}
	if match.ID != inv.ID {
		t.Fatalf("match.id = %d, want invoice id %d", match.ID, inv.ID)
	}
	wantLabel := strconv.Itoa(*inv.Number)
	if match.Label != wantLabel {
		t.Fatalf("match.label = %q, want %q", match.Label, wantLabel)
	}
}
