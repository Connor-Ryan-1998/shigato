//go:build integration

package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shigoto/internal/auth"
	"shigoto/internal/config"
	"shigoto/internal/core"
	"shigoto/internal/db"
)

func TestForeignFinancialWritesDenied(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := webhookTestOrg(t, pool, "acme")
	foreign := webhookTestOrg(t, pool, "foreign")
	contactID := webhookTestContact(t, pool, foreign.ID)
	draftID := webhookTestDraft(t, pool, foreign.ID, contactID, "manual", 1000)
	openID := webhookTestDraft(t, pool, foreign.ID, contactID, "manual", 1000)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := core.IssueInvoice(ctx, tx, foreign.ID, openID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := pool.QueryRow(ctx, "INSERT INTO users (email,name) VALUES ('owner@example.com','Owner') RETURNING id").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)
	a := auth.New(pool, false)
	rr := httptest.NewRecorder()
	if err := a.StartSession(ctx, pool, rr, userID); err != nil {
		t.Fatal(err)
	}
	token := rr.Result().Cookies()[0].Value
	h := newSettingsTestHandler(pool)
	snapshot := func() string {
		var s string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_array(
			(SELECT jsonb_agg(i ORDER BY id) FROM invoices i),
			(SELECT jsonb_agg(p ORDER BY id) FROM payments p),
			(SELECT jsonb_agg(c ORDER BY org_id) FROM invoice_counters c),
			(SELECT count(*) FROM audit_log))::text`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, tc := range []struct {
		path    string
		body    any
		status  int
		message string
	}{
		{"/invoices", map[string]any{"contact_id": contactID, "source": "manual", "due_in_days": 14,
			"lines": []map[string]any{{"description": "Work", "quantity_milli": 1000, "unit_amount": 1000, "gst_code": "N-T"}}}, 422, "contact_id not found"},
		{fmt.Sprintf("/invoices/%d/issue", draftID), nil, 404, "not found"},
		{fmt.Sprintf("/invoices/%d/payments", openID), map[string]any{"amount": 400, "method": "bank_transfer", "paid_on": time.Now().Format("2006-01-02")}, 404, "not found"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			before := snapshot()
			rr := doSettingsRequest(h, settingsRequestJSON(t, "POST", "/api/v1/orgs/acme"+tc.path, token, tc.body))
			if rr.Code != tc.status || !strings.Contains(rr.Body.String(), tc.message) {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if tc.status == 404 && strings.TrimSpace(rr.Body.String()) != `{"error":"not found"}` {
				t.Fatal("foreign invoice details exposed")
			}
			if before != snapshot() {
				t.Fatal("foreign write changed invoices, payments, counters or audit history")
			}
		})
	}
}

func TestFinancialRouteAccessMatrix(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := webhookTestOrg(t, pool, "acme")
	webhookTestOrg(t, pool, "foreign-private-org")
	a := auth.New(pool, false)
	h := New(Deps{Pool: pool, Auth: a, Cfg: config.Config{Modules: map[string]bool{"timesheets": true, "reconciliation": true}}, GetEnv: func(string) string { return "" }})
	for _, role := range []string{"owner", "staff", "bookkeeper", "tenant", "client", "anonymous"} {
		t.Run(role, func(t *testing.T) {
			token := ""
			if role != "anonymous" {
				var userID int64
				if err := pool.QueryRow(ctx, "INSERT INTO users (email,name) VALUES ($1,$2) RETURNING id", role+"@example.com", role).Scan(&userID); err != nil {
					t.Fatal(err)
				}
				webhookTestAddMembership(t, pool, userID, org.ID, auth.Role(role))
				rr := httptest.NewRecorder()
				if err := a.StartSession(ctx, pool, rr, userID); err != nil {
					t.Fatal(err)
				}
				token = rr.Result().Cookies()[0].Value
			}
			for _, route := range []struct {
				method, path string
				allowed      []string
				status       int
			}{
				{"PUT", "/settings", []string{"owner"}, 422},
				{"POST", "/invoices", []string{"owner", "staff"}, 422},
				{"POST", "/invoices/999999/issue", []string{"owner", "staff"}, 404},
				{"POST", "/invoices/999999/payments", []string{"owner", "staff", "bookkeeper"}, 422},
				{"GET", "/reports/transactions.csv?from=2026-07-01&to=2026-07-01", []string{"owner", "staff", "bookkeeper"}, 200},
				{"GET", "/files/999999", []string{"owner", "staff", "bookkeeper"}, 404},
				{"POST", "/time-entries", []string{"owner", "staff"}, 422},
			} {
				allowed := false
				for _, r := range route.allowed {
					allowed = allowed || r == role
				}
				for _, slug := range []string{"acme", "foreign-private-org"} {
					t.Run(route.method+route.path+"/"+slug, func(t *testing.T) {
						want := route.status
						if !allowed {
							want = http.StatusForbidden
						}
						if slug != "acme" {
							want = http.StatusNotFound
						}
						if role == "anonymous" {
							want = http.StatusUnauthorized
						}
						// All requests are read-only or invalid; any write is a defect.
						snapshot := func() string {
							var s string
							if err := pool.QueryRow(ctx, `SELECT jsonb_build_array(
								(SELECT jsonb_agg(o ORDER BY id) FROM orgs o),
								(SELECT count(*) FROM invoices), (SELECT count(*) FROM payments),
								(SELECT count(*) FROM time_entries), (SELECT count(*) FROM audit_log))::text`).Scan(&s); err != nil {
								t.Fatal(err)
							}
							return s
						}
						before := snapshot()
						rr := doSettingsRequest(h, settingsRequestJSON(t, route.method, "/api/v1/orgs/"+slug+route.path, token, map[string]any{}))
						if rr.Code != want {
							t.Fatalf("status=%d want=%d body=%s", rr.Code, want, rr.Body.String())
						}
						if snapshot() != before {
							t.Fatal("denied or invalid request changed financial data")
						}
						if want == 401 || want == 403 || want == 404 {
							msg := map[int]string{401: "unauthorized", 403: "forbidden", 404: "not found"}[want]
							if strings.TrimSpace(rr.Body.String()) != fmt.Sprintf(`{"error":%q}`, msg) {
								t.Fatalf("denial exposed unexpected data: %s", rr.Body.String())
							}
						}
					})
				}
			}
		})
	}
}
