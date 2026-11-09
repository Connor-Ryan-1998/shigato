//go:build integration

package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shigoto/internal/auth"
	"shigoto/internal/config"
	"shigoto/internal/db"
	"shigoto/internal/httpapi"
)

func TestPopulateScenarios(t *testing.T) {
	pool := db.TestPool(t)
	result := populateTest(t, pool)
	main := result.Orgs[0].ID
	for name, query := range map[string]string{
		"five roles":             `SELECT count(DISTINCT role) FROM memberships`,
		"invoice pagination":     `SELECT count(*) FROM invoices WHERE org_id=$1`,
		"residential properties": `SELECT count(*) FROM properties WHERE residential AND org_id=$1`,
		"commercial properties":  `SELECT count(*) FROM properties WHERE NOT residential AND org_id=$1`,
		"active leases":          `SELECT count(*) FROM leases WHERE termination_date IS NULL AND org_id=$1`,
		"ended leases":           `SELECT count(*) FROM leases WHERE termination_date IS NOT NULL AND org_id=$1`,
		"rent history":           `SELECT count(*) FROM lease_rent_changes c JOIN leases l ON l.id=c.lease_id WHERE c.effective_date>l.start_date AND l.org_id=$1`,
		"charges":                `SELECT count(*) FROM lease_charges c JOIN leases l ON l.id=c.lease_id WHERE l.org_id=$1`,
		"rent invoices":          `SELECT count(*) FROM rent_periods WHERE invoice_id IS NOT NULL AND org_id=$1`,
		"billed time":            `SELECT count(*) FROM time_entries WHERE invoice_line_id IS NOT NULL AND org_id=$1`,
		"unbilled time":          `SELECT count(*) FROM time_entries WHERE invoice_line_id IS NULL AND org_id=$1`,
		"expenses":               `SELECT count(*) FROM expenses WHERE org_id=$1`,
		"matched bank lines":     `SELECT count(*) FROM bank_lines WHERE status='matched' AND org_id=$1`,
		"unmatched bank lines":   `SELECT count(*) FROM bank_lines WHERE status='unmatched' AND org_id=$1`,
		"ignored bank lines":     `SELECT count(*) FROM bank_lines WHERE status='ignored' AND org_id=$1`,
		"draft":                  `SELECT count(*) FROM invoices WHERE status='draft' AND org_id=$1`,
		"open":                   `SELECT count(*) FROM invoices WHERE status='open' AND org_id=$1`,
		"paid":                   `SELECT count(*) FROM invoices WHERE status='paid' AND org_id=$1`,
		"void":                   `SELECT count(*) FROM invoices WHERE status='void' AND org_id=$1`,
		"partial":                `SELECT count(*) FROM invoices i WHERE status='open' AND org_id=$1 AND (SELECT coalesce(sum(amount),0) FROM payments WHERE invoice_id=i.id) BETWEEN 1 AND i.total-1`,
	} {
		t.Run(name, func(t *testing.T) {
			args := []any{main}
			min := 1
			if name == "five roles" {
				args, min = nil, 5
			}
			if name == "invoice pagination" {
				min = 60
			}
			if got := count(t, pool, query, args...); got < min {
				t.Fatalf("got %d, want at least %d", got, min)
			}
		})
	}
	if got := count(t, pool, `SELECT count(*) FROM invoices WHERE status='open' AND due_date<$1`, testDay); got == 0 {
		t.Fatal("no overdue invoices")
	}
	if got := count(t, pool, `SELECT count(*) FROM invoices WHERE status='open' AND due_date>$1`, testDay); got == 0 {
		t.Fatal("no upcoming invoices")
	}
	if got := count(t, pool, `SELECT count(*) FROM payments p JOIN invoices i ON i.id=p.invoice_id WHERE p.paid_on>$1 OR p.paid_on<i.issue_date`, testDay); got != 0 {
		t.Errorf("%d payments are in the future or precede their invoice", got)
	}
	for name, query := range map[string]string{
		"invoice totals":     `SELECT count(*) FROM invoices i WHERE total<>subtotal+gst_total OR subtotal<>(SELECT coalesce(sum(amount),0) FROM invoice_lines WHERE invoice_id=i.id) OR gst_total<>(SELECT coalesce(sum(gst_amount),0) FROM invoice_lines WHERE invoice_id=i.id)`,
		"paid balances":      `SELECT count(*) FROM invoices i WHERE status='paid' AND total<>(SELECT coalesce(sum(amount),0) FROM payments WHERE invoice_id=i.id)`,
		"rent totals":        `SELECT count(*) FROM rent_periods r JOIN invoices i ON i.id=r.invoice_id WHERE r.amount_due<>i.total`,
		"portal passwords":   `SELECT count(*) FROM users u JOIN memberships m ON m.user_id=u.id WHERE m.role IN ('tenant','client') AND password_hash IS NOT NULL`,
		"external providers": `SELECT count(*) FROM invoices WHERE provider_ref IS NOT NULL OR provider_url IS NOT NULL`,
		"auto send":          `SELECT count(*) FROM orgs WHERE auto_send_rent`,
	} {
		if got := count(t, pool, query); got != 0 {
			t.Errorf("%s: %d invalid rows", name, got)
		}
	}
}

func TestPopulatePortalVisibility(t *testing.T) {
	pool := db.TestPool(t)
	populateTest(t, pool)
	a := auth.New(pool, false)
	h := httpapi.New(httpapi.Deps{Pool: pool, Auth: a, Cfg: config.Config{
		BaseURL: "http://localhost:8080", Location: time.UTC,
		Modules: map[string]bool{"property": true, "timesheets": true, "reconciliation": true},
	}})
	for _, name := range []string{"tenant", "tenant-other", "tenant-empty", "client", "client-empty", "tenant-beta", "client-beta"} {
		t.Run(name, func(t *testing.T) {
			token, err := auth.RequestMagicLink(context.Background(), pool, name+"@example.test")
			if err != nil || token == "" {
				t.Fatalf("normal magic-link request: %v", err)
			}
			login := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/api/v1/auth/magic/verify", strings.NewReader(fmt.Sprintf(`{"token":%q}`, token)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Shigoto-CSRF", "1")
			h.ServeHTTP(login, req)
			if login.Code != 200 || len(login.Result().Cookies()) == 0 {
				t.Fatalf("normal magic-link login: %d %s", login.Code, login.Body.String())
			}
			cookie := login.Result().Cookies()[0]
			get := func(path string, want int) *httptest.ResponseRecorder {
				t.Helper()
				rr := httptest.NewRecorder()
				r := httptest.NewRequest("GET", path, nil)
				r.AddCookie(cookie)
				h.ServeHTTP(rr, r)
				if rr.Code != want {
					t.Fatalf("%s: %d want %d: %s", path, rr.Code, want, rr.Body.String())
				}
				return rr
			}
			org, other := "sbx-property", "sbx-consulting"
			if strings.HasSuffix(name, "-beta") {
				org, other = other, org
			}
			rr := get("/api/v1/portal/"+org+"/invoices", http.StatusOK)
			var invoices []struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &invoices); err != nil {
				t.Fatal(err)
			}
			empty := strings.HasSuffix(name, "-empty")
			if (len(invoices) == 0) != empty {
				t.Fatalf("invoice count=%d, empty=%t", len(invoices), empty)
			}
			for _, inv := range invoices {
				if got := count(t, pool, `SELECT count(*) FROM invoices i JOIN contacts c ON c.id=i.contact_id WHERE i.id=$1 AND c.email=$2 AND i.status NOT IN ('draft','void')`, inv.ID, name+"@example.test"); got != 1 {
					t.Fatal("portal exposed another contact or hidden invoice")
				}
				get(fmt.Sprintf("/api/v1/portal/%s/invoices/%d", org, inv.ID), 200)
			}
			var foreignID int64
			if err := pool.QueryRow(context.Background(), `SELECT i.id FROM invoices i JOIN contacts c ON c.id=i.contact_id JOIN orgs o ON o.id=i.org_id WHERE o.slug=$1 AND c.email<>$2 LIMIT 1`, org, name+"@example.test").Scan(&foreignID); err != nil {
				t.Fatal(err)
			}
			get(fmt.Sprintf("/api/v1/portal/%s/invoices/%d", org, foreignID), 404)
			get("/api/v1/portal/"+other+"/invoices", 404)
			get("/api/v1/orgs/"+org+"/contacts", 403)
			if strings.HasPrefix(name, "tenant") {
				rr = get("/api/v1/portal/"+org+"/leases", 200)
				var leases []struct {
					ID int64 `json:"id"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &leases); err != nil {
					t.Fatal(err)
				}
				if (len(leases) == 0) != empty {
					t.Fatalf("lease count=%d, empty=%t", len(leases), empty)
				}
				for _, lease := range leases {
					if got := count(t, pool, `SELECT count(*) FROM lease_tenants lt JOIN contacts c ON c.id=lt.contact_id WHERE lt.lease_id=$1 AND c.email=$2`, lease.ID, name+"@example.test"); got != 1 {
						t.Fatal("portal exposed another tenant's lease")
					}
				}
			} else {
				get("/api/v1/portal/"+org+"/leases", 404)
			}
		})
	}
}

func TestPopulateReplacesTesterEdits(t *testing.T) {
	pool := db.TestPool(t)
	populateTest(t, pool)
	mustExec(t, pool, `INSERT INTO contacts (org_id,kind,name) SELECT org_id,'supplier','Tester supplier' FROM sandbox_orgs LIMIT 1`)
	mustExec(t, pool, `INSERT INTO sessions (token_hash,user_id,expires_at) SELECT decode('01','hex'),user_id,now()+interval '1 day' FROM sandbox_users LIMIT 1`)
	populateTest(t, pool)
	if n := count(t, pool, `SELECT count(*) FROM contacts WHERE name='Tester supplier'`); n != 0 {
		t.Fatal("tester record survived reset")
	}
	if n := count(t, pool, `SELECT count(*) FROM sessions`); n != 0 {
		t.Fatal("old sandbox session survived reset")
	}
}

func TestPopulateRefusesExternalRecord(t *testing.T) {
	pool := db.TestPool(t)
	populateTest(t, pool)
	mustExec(t, pool, `INSERT INTO orgs (slug,name) VALUES ('unrelated','Unrelated')`)
	// An invalid cross-org relationship can exist in SQL even though domain APIs reject it.
	mustExec(t, pool, `INSERT INTO invoices (org_id,contact_id,source) SELECT o.id,c.id,'manual' FROM orgs o CROSS JOIN contacts c WHERE o.slug='unrelated' LIMIT 1`)
	before := count(t, pool, `SELECT count(*) FROM invoices`)
	if _, err := Populate(context.Background(), pool, testDay); err == nil {
		t.Fatal("reset accepted an external dependency")
	}
	if n := count(t, pool, `SELECT count(*) FROM invoices`); n != before {
		t.Fatal("failed reset changed invoices")
	}
}
