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
