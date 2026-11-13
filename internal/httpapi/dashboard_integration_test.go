//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/config"
	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/importers"
	"shigoto/internal/recon"
)

// newDashboardTestHandler builds a handler with the given modules enabled
// (property/timesheets/reconciliation), GetEnv returning "" so no org ever
// resolves a Stripe provider.
func newDashboardTestHandler(pool *pgxpool.Pool, mods map[string]bool) http.Handler {
	a := auth.New(pool, false)
	return New(Deps{
		Pool:   pool,
		Auth:   a,
		Cfg:    config.Config{Modules: mods},
		GetEnv: func(string) string { return "" },
	})
}

func getDashboard(handler http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// dashboardIssueInvoice creates and issues a one-line, GST-free invoice for
// totalCents, due dueInDays after today - the same create-then-issue
// sequence webhookContactAndInvoice uses, minus the provider_ref stamp
// that test doesn't need here.
func dashboardIssueInvoice(t *testing.T, pool *pgxpool.Pool, orgID, contactID int64, totalCents int64, dueInDays int) core.Invoice {
	t.Helper()
	ctx := context.Background()
	draft, err := core.CreateDraftInvoice(ctx, pool, orgID, core.NewInvoice{
		ContactID: contactID, Source: "manual", DueInDays: dueInDays,
		Lines: []core.NewLine{{Description: "line", QuantityMilli: 1000, UnitAmount: core.Cents(totalCents), GSTCode: "N-T"}},
	})
	if err != nil {
		t.Fatalf("CreateDraftInvoice: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	inv, err := core.IssueInvoice(ctx, tx, orgID, draft.ID, time.Now())
	if err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return inv
}

// backdateDueDate pushes invoiceID's due_date days into the past, so tests
// can create an overdue invoice without a negative due_in_days (which
// CreateDraftInvoice rejects).
func backdateDueDate(t *testing.T, pool *pgxpool.Pool, orgID, invoiceID int64, days int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		"UPDATE invoices SET due_date = due_date - make_interval(days => $1) WHERE org_id = $2 AND id = $3",
		days, orgID, invoiceID,
	); err != nil {
		t.Fatalf("backdating due_date: %v", err)
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

func decodeDashboard(t *testing.T, rr *httptest.ResponseRecorder) dashboardBody {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	var body dashboardBody
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decoding dashboard response %s: %v", rr.Body.String(), err)
	}
	return body
}

// TestDashboardCore covers the dashboard's core (invoices) section: one
// draft, one open invoice not yet due, one open invoice overdue with a
// partial payment, and one fully paid invoice.
func TestDashboardCore(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)
	token := settingsTestLogin(t, pool, "owner@example.com", "correct-password")
	contactID := webhookTestContact(t, pool, org.ID)
	handler := newDashboardTestHandler(pool, map[string]bool{})
	ctx := context.Background()

	// draft
	if _, err := core.CreateDraftInvoice(ctx, pool, org.ID, core.NewInvoice{
		ContactID: contactID, Source: "manual", DueInDays: 14,
		Lines: []core.NewLine{{Description: "draft", QuantityMilli: 1000, UnitAmount: 1000, GSTCode: "N-T"}},
	}); err != nil {
		t.Fatalf("CreateDraftInvoice (draft): %v", err)
	}

	// open, not yet due
	dashboardIssueInvoice(t, pool, org.ID, contactID, 5000, 30)

	// open, overdue, with a partial payment
	overdue := dashboardIssueInvoice(t, pool, org.ID, contactID, 10000, 0)
	backdateDueDate(t, pool, org.ID, overdue.ID, 5)
	if _, _, err := core.RecordPayment(ctx, pool, org.ID, core.NewPayment{
		InvoiceID: overdue.ID, Amount: 4000, PaidOn: time.Now(), Method: "cash",
	}); err != nil {
		t.Fatalf("RecordPayment (partial): %v", err)
	}

	// paid in full
	paid := dashboardIssueInvoice(t, pool, org.ID, contactID, 2000, 14)
	if _, _, err := core.RecordPayment(ctx, pool, org.ID, core.NewPayment{
		InvoiceID: paid.ID, Amount: 2000, PaidOn: time.Now(), Method: "cash",
	}); err != nil {
		t.Fatalf("RecordPayment (paid): %v", err)
	}

	body := decodeDashboard(t, getDashboard(handler, token))
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
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)
	token := settingsTestLogin(t, pool, "owner@example.com", "correct-password")
	contactID := webhookTestContact(t, pool, org.ID)
	handler := newDashboardTestHandler(pool, map[string]bool{})
	ctx := context.Background()

	paid := dashboardIssueInvoice(t, pool, org.ID, contactID, 2000, 14)
	if _, _, err := core.RecordPayment(ctx, pool, org.ID, core.NewPayment{
		InvoiceID: paid.ID, Amount: 2000, PaidOn: time.Now(), Method: "cash",
	}); err != nil {
		t.Fatalf("RecordPayment: %v", err)
	}

	body := decodeDashboard(t, getDashboard(handler, token))
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
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)
	token := settingsTestLogin(t, pool, "owner@example.com", "correct-password")
	handler := newDashboardTestHandler(pool, map[string]bool{"timesheets": true})

	body := decodeDashboard(t, getDashboard(handler, token))
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
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)
	token := settingsTestLogin(t, pool, "owner@example.com", "correct-password")
	contactID := webhookTestContact(t, pool, org.ID)
	handler := newDashboardTestHandler(pool, map[string]bool{})
	ctx := context.Background()

	if _, err := core.CreateDraftInvoice(ctx, pool, org.ID, core.NewInvoice{
		ContactID: contactID, Source: "manual", DueInDays: 14,
		Lines: []core.NewLine{{Description: "draft", QuantityMilli: 1000, UnitAmount: 1000, GSTCode: "N-T"}},
	}); err != nil {
		t.Fatalf("CreateDraftInvoice (draft): %v", err)
	}
	dashboardIssueInvoice(t, pool, org.ID, contactID, 5000, 30) // open, not due
	overdue := dashboardIssueInvoice(t, pool, org.ID, contactID, 7000, 0)
	backdateDueDate(t, pool, org.ID, overdue.ID, 5)

	listInvoicesVia := func(query string) invoiceListResponse {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/invoices"+query, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET /invoices%s: status = %d, want 200, body=%s", query, rr.Code, rr.Body.String())
		}
		var body invoiceListResponse
		if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
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
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)
	token := settingsTestLogin(t, pool, "owner@example.com", "correct-password")
	contactID := webhookTestContact(t, pool, org.ID)
	handler := newDashboardTestHandler(pool, map[string]bool{})
	ctx := context.Background()

	inv := dashboardIssueInvoice(t, pool, org.ID, contactID, 10000, 14)
	if _, _, err := core.RecordPayment(ctx, pool, org.ID, core.NewPayment{
		InvoiceID: inv.ID, Amount: 4000, PaidOn: time.Now(), Method: "cash",
	}); err != nil {
		t.Fatalf("RecordPayment: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/invoices/"+strconv.FormatInt(inv.ID, 10), nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /invoices/%d: status = %d, want 200, body=%s", inv.ID, rr.Code, rr.Body.String())
	}
	rawBody := rr.Body.String()
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
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/invoices", nil)
	listReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	listRR := httptest.NewRecorder()
	handler.ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("GET /invoices: status = %d, want 200, body=%s", listRR.Code, listRR.Body.String())
	}
	if strings.Contains(listRR.Body.String(), "payments") || strings.Contains(listRR.Body.String(), "provider_url") {
		t.Fatalf("list response leaked detail-only fields: %s", listRR.Body.String())
	}
}

// TestBankLineMatchLabel covers the bank-line JSON's "match" field for a
// line matched to an invoice: kind "invoice", with the invoice's number as
// the label.
func TestBankLineMatchLabel(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)
	token := settingsTestLogin(t, pool, "owner@example.com", "correct-password")
	contactID := webhookTestContact(t, pool, org.ID)
	handler := newDashboardTestHandler(pool, map[string]bool{"reconciliation": true})
	ctx := context.Background()

	mapping, ok := importers.Preset("commbank")
	if !ok {
		t.Fatal("commbank preset not found")
	}
	account, err := recon.CreateBankAccount(ctx, pool, org.ID, "Everyday", mapping)
	if err != nil {
		t.Fatalf("CreateBankAccount: %v", err)
	}
	if _, err := recon.Import(ctx, pool, org.ID, account.ID, strings.NewReader("01/03/2026,100.00,Payment received\n")); err != nil {
		t.Fatalf("Import: %v", err)
	}
	lines, _, err := recon.ListBankLines(ctx, pool, org.ID, recon.BankLineFilter{})
	if err != nil || len(lines) != 1 {
		t.Fatalf("ListBankLines: lines=%+v, err=%v", lines, err)
	}
	lineID := lines[0].ID

	inv := dashboardIssueInvoice(t, pool, org.ID, contactID, 10000, 14)
	if inv.Number == nil {
		t.Fatal("issued invoice has no number")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := recon.MatchInvoice(ctx, tx, org.ID, lineID, inv.ID, "bank_transfer"); err != nil {
		t.Fatalf("MatchInvoice: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/bank-lines", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
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
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
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
