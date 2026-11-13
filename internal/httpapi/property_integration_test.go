//go:build integration

package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"shigoto/internal/testkit"
)

// TestRentRunAuditsActor covers finding 7: invoices auto-issued by a
// user-triggered POST /rent-run are audited against that user.
func TestRentRunAuditsActor(t *testing.T) {
	e := testkit.New(t)
	handler := e.Handler(testkit.OnlyModules("property"))
	org, owner := e.OwnerSession("acme", handler)
	prop := e.Property(org, testkit.PropertyName("1 Test St"), testkit.Residential())
	e.Settings(org, testkit.RentRun(0, true), testkit.PayOptions())
	tenant := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant"))
	if rr := owner.Do(http.MethodPost, "/api/v1/orgs/acme/leases", map[string]any{
		"property_id": prop.ID, "start_date": time.Now().UTC().Format("2006-01-02"),
		"frequency": "weekly", "rent_amount": 50000,
		"tenants": []map[string]any{{"contact_id": tenant.ID, "primary": true}},
	}); rr.Code != http.StatusCreated {
		t.Fatalf("create lease: status = %d, body=%s", rr.Code, rr.Body)
	}

	rr := owner.Do(http.MethodPost, "/api/v1/orgs/acme/rent-run", nil)
	if rr.Code != http.StatusOK || !strings.Contains(string(rr.Body), `"issued":1`) {
		t.Fatalf("rent-run: status = %d body=%s, want 200 with issued 1", rr.Code, rr.Body)
	}

	if withActor := e.Count("audit_log", "org_id = $1 AND action = 'issue' AND actor_user_id IS NOT NULL", org.ID); withActor != 1 {
		t.Fatalf("issue audit rows with an actor = %d, want 1", withActor)
	}
}
