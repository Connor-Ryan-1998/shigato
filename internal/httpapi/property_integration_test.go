//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func newPropertyTestHandler(pool *pgxpool.Pool, enabled bool) http.Handler {
	a := auth.New(pool, false)
	return New(Deps{
		Pool:   pool,
		Auth:   a,
		Cfg:    config.Config{Modules: map[string]bool{"property": enabled}},
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
	handler = newPropertyTestHandler(pool, true)
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

func TestCreateLeaseRequiresOnePrimary(t *testing.T) {
	pool, handler, token, orgID, propertyID := propertyTestSetup(t)
	tenant1 := propertyTestContact(t, pool, orgID, "tenant", "Tenant One")
	tenant2 := propertyTestContact(t, pool, orgID, "tenant", "Tenant Two")

	base := map[string]any{
		"property_id": propertyID, "start_date": "2026-11-02",
		"frequency": "weekly", "rent_amount": 50000,
	}

	// No primary.
	noPrimary := map[string]any{}
	for k, v := range base {
		noPrimary[k] = v
	}
	noPrimary["tenants"] = []map[string]any{{"contact_id": tenant1, "primary": false}}
	if rr := postLease(handler, token, noPrimary); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("no primary: status = %d, want 422, body=%s", rr.Code, rr.Body.String())
	}

	// Two primaries.
	twoPrimary := map[string]any{}
	for k, v := range base {
		twoPrimary[k] = v
	}
	twoPrimary["tenants"] = []map[string]any{
		{"contact_id": tenant1, "primary": true},
		{"contact_id": tenant2, "primary": true},
	}
	if rr := postLease(handler, token, twoPrimary); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("two primaries: status = %d, want 422, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateLeaseRejectsCrossOrgTenant(t *testing.T) {
	pool, handler, token, _, propertyID := propertyTestSetup(t)
	other := webhookTestOrg(t, pool, "other")
	otherTenant := propertyTestContact(t, pool, other.ID, "tenant", "Other Org Tenant")

	body := map[string]any{
		"property_id": propertyID, "start_date": "2026-11-02",
		"frequency": "weekly", "rent_amount": 50000,
		"tenants": []map[string]any{{"contact_id": otherTenant, "primary": true}},
	}
	if rr := postLease(handler, token, body); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cross-org tenant: status = %d, want 422, body=%s", rr.Code, rr.Body.String())
	}
}

func TestRentChangeWarningRoute(t *testing.T) {
	pool, handler, token, orgID, propertyID := propertyTestSetup(t)
	tenant := propertyTestContact(t, pool, orgID, "tenant", "Tenant One")

	body := map[string]any{
		"property_id": propertyID, "start_date": "2025-01-01",
		"frequency": "weekly", "rent_amount": 50000,
		"tenants": []map[string]any{{"contact_id": tenant, "primary": true}},
	}
	rr := postLease(handler, token, body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create lease: status = %d, want 201, body=%s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding created lease: %v", err)
	}

	postRentChange := func(effective string, amount int64) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"effective_date": effective, "amount": amount})
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/leases/%d/rent-changes", created.ID), bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Shigoto-CSRF", "1")
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	rr = postRentChange("2025-06-01", 55000)
	if rr.Code != http.StatusCreated {
		t.Fatalf("first increase: status = %d, want 201, body=%s", rr.Code, rr.Body.String())
	}
	var first struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &first); err != nil {
		t.Fatalf("decoding first increase: %v", err)
	}
	if len(first.Warnings) != 0 {
		t.Fatalf("first increase warnings = %v, want none", first.Warnings)
	}

	rr = postRentChange("2025-12-01", 60000)
	if rr.Code != http.StatusCreated {
		t.Fatalf("second increase: status = %d, want 201, body=%s", rr.Code, rr.Body.String())
	}
	var second struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &second); err != nil {
		t.Fatalf("decoding second increase: %v", err)
	}
	if len(second.Warnings) != 1 {
		t.Fatalf("second increase warnings = %v, want exactly 1", second.Warnings)
	}

	l, err := property.GetLease(context.Background(), pool, orgID, created.ID)
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if len(l.RentChanges) != 3 {
		t.Fatalf("RentChanges = %d rows, want 3 (initial + 2 increases, both stored)", len(l.RentChanges))
	}
}

func TestPropertyModuleDisabled404(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)
	token := settingsTestLogin(t, pool, "owner@example.com", "correct-password")

	handler := newPropertyTestHandler(pool, false)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/properties", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
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
