//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/config"
	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/property"
)

func newPropertyTestHandler(pool *pgxpool.Pool) http.Handler {
	a := auth.New(pool, false)
	return New(Deps{
		Pool:   pool,
		Auth:   a,
		Cfg:    config.Config{Modules: map[string]bool{"property": true}},
		GetEnv: func(string) string { return "" },
	})
}

func propertyTestContact(t *testing.T, pool *pgxpool.Pool, orgID int64, kind, name string) int64 {
	t.Helper()
	c, err := core.CreateContact(context.Background(), pool, orgID, core.Contact{Kind: kind, Name: name})
	if err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	return c.ID
}

func propertyTestProperty(t *testing.T, pool *pgxpool.Pool, orgID int64) int64 {
	t.Helper()
	p, err := property.CreateProperty(context.Background(), pool, orgID, property.Property{Name: "1 Test St", Residential: true})
	if err != nil {
		t.Fatalf("CreateProperty: %v", err)
	}
	return p.ID
}

func propertyTestSetup(t *testing.T) (pool *pgxpool.Pool, handler http.Handler, token string, orgID, propertyID int64) {
	t.Helper()
	pool = db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)
	token = settingsTestLogin(t, pool, "owner@example.com", "correct-password")
	handler = newPropertyTestHandler(pool)
	propertyID = propertyTestProperty(t, pool, org.ID)
	return pool, handler, token, org.ID, propertyID
}

func postLease(handler http.Handler, token string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/acme/leases", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shigoto-CSRF", "1")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// TestRentRunAuditsActor covers finding 7: invoices auto-issued by a
// user-triggered POST /rent-run are audited against that user.
func TestRentRunAuditsActor(t *testing.T) {
	pool, handler, token, orgID, propertyID := propertyTestSetup(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		"UPDATE orgs SET invoice_lead_days = 0, auto_send_rent = true, bank_bsb = '062-000', bank_account = '12345678', payid = 'a@example.com' WHERE id = $1",
		orgID,
	); err != nil {
		t.Fatalf("configuring org: %v", err)
	}
	tenantID := propertyTestContact(t, pool, orgID, "tenant", "Tenant")
	if rr := postLease(handler, token, map[string]any{
		"property_id": propertyID, "start_date": time.Now().UTC().Format("2006-01-02"),
		"frequency": "weekly", "rent_amount": 50000,
		"tenants": []map[string]any{{"contact_id": tenantID, "primary": true}},
	}); rr.Code != http.StatusCreated {
		t.Fatalf("create lease: status = %d, body=%s", rr.Code, rr.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/acme/rent-run", nil)
	req.Header.Set("X-Shigoto-CSRF", "1")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"issued":1`) {
		t.Fatalf("rent-run: status = %d body=%s, want 200 with issued 1", rr.Code, rr.Body.String())
	}

	var withActor int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM audit_log WHERE org_id = $1 AND action = 'issue' AND actor_user_id IS NOT NULL", orgID,
	).Scan(&withActor); err != nil {
		t.Fatalf("counting audit_log: %v", err)
	}
	if withActor != 1 {
		t.Fatalf("issue audit rows with an actor = %d, want 1", withActor)
	}
}
