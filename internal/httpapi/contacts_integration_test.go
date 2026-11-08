//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/db"
)

func TestContactEditing(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := webhookTestOrg(t, pool, "acme")
	other := webhookTestOrg(t, pool, "other")
	owner := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, owner, org.ID, auth.Owner)
	token := settingsTestLogin(t, pool, "owner@example.com", "correct-password")
	handler := newSettingsTestHandler(pool)

	for _, kind := range []string{"tenant", "client", "supplier"} {
		t.Run(kind, func(t *testing.T) {
			providerRef := "cus_keep_" + kind
			c, err := core.CreateContact(ctx, pool, org.ID, core.Contact{Kind: kind, Name: "Original", Email: "old@example.com", ProviderCustomerRef: providerRef})
			if err != nil {
				t.Fatal(err)
			}
			invoice := webhookTestDraft(t, pool, org.ID, c.ID, "manual", 10000)
			var beforeInvoice string
			if err := pool.QueryRow(ctx, "SELECT to_jsonb(i)::text FROM invoices i WHERE id=$1", invoice).Scan(&beforeInvoice); err != nil {
				t.Fatal(err)
			}
			body := map[string]string{"name": "Corrected", "email": "new@example.com", "phone": "0400123456", "company_name": "New Co", "abn": "51824753556"}
			url := fmt.Sprintf("/api/v1/orgs/acme/contacts/%d", c.ID)
			rr := doSettingsRequest(handler, settingsRequestJSON(t, http.MethodPut, url, token, body))
			if rr.Code != http.StatusOK {
				t.Fatalf("update status %d: %s", rr.Code, rr.Body.String())
			}
			var response contactResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.ID != c.ID || response.Kind != kind || response.Name != "Corrected" || response.Email != "new@example.com" || response.Phone != "0400123456" || response.CompanyName != "New Co" || response.ABN != "51824753556" || response.ProviderCustomerRef != providerRef {
				t.Fatalf("bad response: %+v", response)
			}
			stored, err := core.GetContact(ctx, pool, org.ID, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Name != "Corrected" || stored.Email != "new@example.com" {
				t.Fatalf("edit not persisted: %+v", stored)
			}
			var audits int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE org_id=$1 AND entity='contact' AND entity_id=$2 AND action='update' AND actor_user_id=$3", org.ID, c.ID, owner).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if audits != 1 {
				t.Fatalf("update audits=%d", audits)
			}
			var afterInvoice string
			if err := pool.QueryRow(ctx, "SELECT to_jsonb(i)::text FROM invoices i WHERE id=$1", invoice).Scan(&afterInvoice); err != nil {
				t.Fatal(err)
			}
			if beforeInvoice != afterInvoice {
				t.Fatal("contact edit rewrote invoice")
			}
			rr = doSettingsRequest(handler, settingsRequestJSON(t, http.MethodPut, url, token, map[string]string{"name": "Minimal"}))
			if rr.Code != http.StatusOK {
				t.Fatalf("clear status %d: %s", rr.Code, rr.Body.String())
			}
			stored, err = core.GetContact(ctx, pool, org.ID, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Email != "" || stored.Phone != "" || stored.CompanyName != "" || stored.ABN != "" || stored.Kind != kind || stored.ProviderCustomerRef != providerRef {
				t.Fatalf("clearing damaged identity: %+v", stored)
			}
		})
	}
	foreign, err := core.CreateContact(ctx, pool, other.ID, core.Contact{Kind: "client", Name: "Foreign"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{fmt.Sprint(foreign.ID), "99999999", "bad"} {
		rr := doSettingsRequest(handler, settingsRequestJSON(t, http.MethodPut, "/api/v1/orgs/acme/contacts/"+id, token, map[string]string{"name": "Attack"}))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("id=%s status=%d: %s", id, rr.Code, rr.Body.String())
		}
	}
	stored, err := core.GetContact(ctx, pool, other.ID, foreign.ID)
	if err != nil || stored.Name != "Foreign" {
		t.Fatal("foreign contact changed", err)
	}
}

func TestContactEditingValidationAndPermissions(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := webhookTestOrg(t, pool, "acme")
	owner := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, owner, org.ID, auth.Owner)
	token := settingsTestLogin(t, pool, "owner@example.com", "correct-password")
	c, err := core.CreateContact(ctx, pool, org.ID, core.Contact{Kind: "tenant", Name: "Keep", Email: "old@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	portalUser := webhookTestUser(t, pool, "old@example.com", "correct-password")
	if _, err := pool.Exec(ctx, "INSERT INTO memberships(user_id,org_id,role,contact_id) VALUES($1,$2,'tenant',$3)", portalUser, org.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	handler := newSettingsTestHandler(pool)
	url := fmt.Sprintf("/api/v1/orgs/acme/contacts/%d", c.ID)
	for _, tc := range []struct {
		name   string
		body   map[string]string
		status int
	}{
		{"empty", map[string]string{"name": ""}, 422},
		{"spaces", map[string]string{"name": "   "}, 422},
		{"invalid email", map[string]string{"name": "Keep", "email": "not-email"}, 422},
		{"display email", map[string]string{"name": "Keep", "email": "Person <person@example.com>"}, 422},
		{"kind", map[string]string{"name": "Keep", "kind": "supplier"}, 400},
		{"provider", map[string]string{"name": "Keep", "provider_customer_ref": "cus_attack"}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := doSettingsRequest(handler, settingsRequestJSON(t, http.MethodPut, url, token, tc.body))
			if rr.Code != tc.status {
				t.Fatalf("status=%d want=%d: %s", rr.Code, tc.status, rr.Body.String())
			}
		})
	}
	for _, role := range []auth.Role{auth.Bookkeeper, auth.Tenant} {
		email := string(role) + "@example.com"
		user := webhookTestUser(t, pool, email, "correct-password")
		webhookTestAddMembership(t, pool, user, org.ID, role)
		rr := doSettingsRequest(handler, settingsRequestJSON(t, http.MethodPut, url, settingsTestLogin(t, pool, email, "correct-password"), map[string]string{"name": "Attack"}))
		if rr.Code != http.StatusForbidden {
			t.Fatalf("role=%s status=%d", role, rr.Code)
		}
	}
	stored, err := core.GetContact(ctx, pool, org.ID, c.ID)
	if err != nil || stored.Name != "Keep" || stored.Email != "old@example.com" {
		t.Fatalf("rejected edit persisted: %+v, %v", stored, err)
	}
	unauthenticated := doSettingsRequest(handler, settingsRequestJSON(t, http.MethodPut, url, "", map[string]string{"name": "Attack"}))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", unauthenticated.Code)
	}
	noCSRF := settingsRequestJSON(t, http.MethodPut, url, token, map[string]string{"name": "Attack"})
	noCSRF.Header.Del("X-Shigoto-CSRF")
	if rr := doSettingsRequest(handler, noCSRF); rr.Code != http.StatusForbidden {
		t.Fatalf("no CSRF status=%d", rr.Code)
	}
	rr := doSettingsRequest(handler, settingsRequestJSON(t, http.MethodPut, url, token, map[string]string{"name": "Updated", "email": "new@example.com"}))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d: %s", rr.Code, rr.Body.String())
	}
	var email string
	var linkedContact int64
	if err := pool.QueryRow(ctx, "SELECT u.email,m.contact_id FROM users u JOIN memberships m ON m.user_id=u.id WHERE u.id=$1 AND m.org_id=$2", portalUser, org.ID).Scan(&email, &linkedContact); err != nil {
		t.Fatal(err)
	}
	if email != "old@example.com" || linkedContact != c.ID {
		t.Fatal("contact edit transferred portal identity")
	}
}

func TestContactEditAuditFailureRollsBack(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := webhookTestOrg(t, pool, "acme")
	owner := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, owner, org.ID, auth.Owner)
	token := settingsTestLogin(t, pool, "owner@example.com", "correct-password")
	c, err := core.CreateContact(ctx, pool, org.ID, core.Contact{Kind: "client", Name: "Keep"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "ALTER TABLE audit_log RENAME TO saved_contact_audit_log"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := pool.Exec(ctx, "ALTER TABLE saved_contact_audit_log RENAME TO audit_log"); err != nil {
			t.Error(err)
		}
	}()
	rr := doSettingsRequest(newSettingsTestHandler(pool), settingsRequestJSON(t, http.MethodPut, fmt.Sprintf("/api/v1/orgs/acme/contacts/%d", c.ID), token, map[string]string{"name": "Rollback"}))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d: %s", rr.Code, rr.Body.String())
	}
	stored, err := core.GetContact(ctx, pool, org.ID, c.ID)
	if err != nil || stored.Name != "Keep" {
		t.Fatalf("unaudited edit persisted: %+v, %v", stored, err)
	}
}
