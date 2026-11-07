//go:build integration

package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shigoto/internal/auth"
	"shigoto/internal/config"
	"shigoto/internal/db"
)

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
