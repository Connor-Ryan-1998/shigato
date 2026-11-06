//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/config"
	"shigoto/internal/db"
)

// settingsTestLogin logs email/password in and returns the session token,
// so the caller can attach it as the shigoto_session cookie.
func settingsTestLogin(t *testing.T, pool *pgxpool.Pool, email, password string) string {
	t.Helper()
	token, err := auth.Login(context.Background(), pool, email, password)
	if err != nil {
		t.Fatalf("Login(%s): %v", email, err)
	}
	return token
}

// newSettingsTestHandler builds a handler with GetEnv returning "" so no
// org ever resolves a Stripe provider: tests that depend on the QLD
// pay-options count can rely on bank transfer and PayID being the only two
// options in play.
func newSettingsTestHandler(pool *pgxpool.Pool) http.Handler {
	a := auth.New(pool, false)
	return New(Deps{
		Pool:   pool,
		Auth:   a,
		Cfg:    config.Config{Modules: map[string]bool{}},
		GetEnv: func(string) string { return "" },
	})
}

func settingsRequestJSON(t *testing.T, method, url, token string, body any) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshaling body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, url, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shigoto-CSRF", "1")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	}
	return req
}

func doSettingsRequest(handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// TestSettingsPutThenIssueRent checks that a rent invoice can't be issued
// until the org has at least 2 payment options, and that PUTting bank and
// PayID details (2 options, since GetEnv is stubbed to never produce a
// Stripe option) unblocks issuing the same invoice.
func TestSettingsPutThenIssueRent(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)
	token := settingsTestLogin(t, pool, "owner@example.com", "correct-password")

	contactID := webhookTestContact(t, pool, org.ID)
	invoiceID := webhookTestDraft(t, pool, org.ID, contactID, "rent", 50000)

	handler := newSettingsTestHandler(pool)

	issueReq := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/acme/invoices/"+strconv.FormatInt(invoiceID, 10)+"/issue", nil)
	issueReq.Header.Set("X-Shigoto-CSRF", "1")
	issueReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := doSettingsRequest(handler, issueReq)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("issue before settings PUT: status = %d, want 422, body=%s", rr.Code, rr.Body.String())
	}

	putBody := settingsRequest{
		Name:            "Acme Org",
		BankBSB:         "123-456",
		BankAccount:     "12345678",
		PayID:           "owner@example.com",
		InvoiceLeadDays: 7,
	}
	putReq := settingsRequestJSON(t, http.MethodPut, "/api/v1/orgs/acme/settings", token, putBody)
	rr = doSettingsRequest(handler, putReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT settings: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	issueReq2 := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/acme/invoices/"+strconv.FormatInt(invoiceID, 10)+"/issue", nil)
	issueReq2.Header.Set("X-Shigoto-CSRF", "1")
	issueReq2.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr = doSettingsRequest(handler, issueReq2)
	if rr.Code != http.StatusOK {
		t.Fatalf("issue after settings PUT: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
}

// TestSettingsPutNeedsOrgAdmin checks that a staff member (who holds
// invoices_write/read but not org_admin) is forbidden from changing org
// settings.
func TestSettingsPutNeedsOrgAdmin(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	userID := webhookTestUser(t, pool, "staff@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Staff)
	token := settingsTestLogin(t, pool, "staff@example.com", "correct-password")

	handler := newSettingsTestHandler(pool)

	putBody := settingsRequest{Name: "Acme Org", InvoiceLeadDays: 7}
	putReq := settingsRequestJSON(t, http.MethodPut, "/api/v1/orgs/acme/settings", token, putBody)
	rr := doSettingsRequest(handler, putReq)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rr.Code, rr.Body.String())
	}
}
