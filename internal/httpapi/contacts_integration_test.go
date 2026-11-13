//go:build integration

package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/httpapi"
	"shigoto/internal/testkit"
)

func TestContactEditing(t *testing.T) {
	e := testkit.New(t)
	ctx := e.Ctx
	pool := e.Pool
	org := e.Org("acme")
	ownerUser := e.User("owner@example.com")
	owner := ownerUser.ID
	e.Member(ownerUser, org, auth.Owner)
	handler := e.Handler(testkit.OnlyModules())
	token := e.Login(ownerUser, handler).Token

	for _, kind := range []string{"tenant", "client", "supplier"} {
		t.Run(kind, func(t *testing.T) {
			sub := *e
			sub.T = t
			session := sub.Resume(token, handler)
			providerRef := "cus_keep_" + kind
			c := sub.Contact(org, testkit.Kind(kind), testkit.Name("Original"), testkit.Email("old@example.com"),
				func(c *core.Contact) { c.ProviderCustomerRef = providerRef })
			invoice := sub.Invoice(org, c.ID, testkit.Amount(10000), testkit.InvoiceGST("N-T"), testkit.Source("manual")).ID
			var beforeInvoice string
			if err := pool.QueryRow(ctx, "SELECT to_jsonb(i)::text FROM invoices i WHERE id=$1", invoice).Scan(&beforeInvoice); err != nil {
				t.Fatal(err)
			}
			body := map[string]string{"name": "Corrected", "email": "new@example.com", "phone": "0400123456", "company_name": "New Co", "abn": "51824753556"}
			url := fmt.Sprintf("/api/v1/orgs/acme/contacts/%d", c.ID)
			rr := session.Do(http.MethodPut, url, body)
			if rr.Code != http.StatusOK {
				t.Fatalf("update status %d: %s", rr.Code, rr.Body)
			}
			var response httpapi.ContactResponse
			if err := json.Unmarshal(rr.Body, &response); err != nil {
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
			audits := sub.Count("audit_log", "org_id=$1 AND entity='contact' AND entity_id=$2 AND action='update' AND actor_user_id=$3", org.ID, c.ID, owner)
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
			rr = session.Do(http.MethodPut, url, map[string]string{"name": "Minimal"})
			if rr.Code != http.StatusOK {
				t.Fatalf("clear status %d: %s", rr.Code, rr.Body)
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
	e := testkit.New(t)
	ctx := e.Ctx
	pool := e.Pool
	handler := e.Handler(testkit.OnlyModules())
	org, owner := e.OwnerSession("acme", handler)
	c := e.Contact(org, testkit.Kind("client"), testkit.Name("Keep"))
	if _, err := pool.Exec(ctx, "ALTER TABLE audit_log RENAME TO saved_contact_audit_log"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := pool.Exec(ctx, "ALTER TABLE saved_contact_audit_log RENAME TO audit_log"); err != nil {
			t.Error(err)
		}
	}()
	rr := owner.Do(http.MethodPut, fmt.Sprintf("/api/v1/orgs/acme/contacts/%d", c.ID), map[string]string{"name": "Rollback"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d: %s", rr.Code, rr.Body)
	}
	stored, err := core.GetContact(ctx, pool, org.ID, c.ID)
	if err != nil || stored.Name != "Keep" {
		t.Fatalf("unaudited edit persisted: %+v, %v", stored, err)
	}
}
