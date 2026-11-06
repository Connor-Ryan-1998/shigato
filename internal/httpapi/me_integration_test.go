//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shigoto/internal/auth"
	"shigoto/internal/db"
)

type meResponse struct {
	User struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"user"`
	Orgs []struct {
		Slug  string   `json:"slug"`
		Name  string   `json:"name"`
		Role  string   `json:"role"`
		Perms []string `json:"perms"`
	} `json:"orgs"`
}

func TestMeListsOrgsAndPerms(t *testing.T) {
	pool := db.TestPool(t)
	handler := newWebhookHandler(pool, nil)

	orgB := webhookTestOrg(t, pool, "org-b")
	orgA := webhookTestOrg(t, pool, "org-a")
	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, orgA.ID, auth.Owner)
	webhookTestAddMembership(t, pool, userID, orgB.ID, auth.Owner)

	token, err := auth.Login(context.Background(), pool, "owner@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	var body meResponse
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if body.User.ID != userID || body.User.Email != "owner@example.com" || body.User.Name != "owner@example.com" {
		t.Errorf("user = %+v, want id=%d email=name=owner@example.com", body.User, userID)
	}

	if len(body.Orgs) != 2 {
		t.Fatalf("orgs = %+v, want 2 orgs", body.Orgs)
	}
	// Sorted by name: "org-a org" < "org-b org".
	if body.Orgs[0].Slug != "org-a" || body.Orgs[1].Slug != "org-b" {
		t.Errorf("orgs = %+v, want org-a then org-b (sorted by name)", body.Orgs)
	}
	for _, o := range body.Orgs {
		if o.Role != string(auth.Owner) {
			t.Errorf("org %s role = %q, want %q", o.Slug, o.Role, auth.Owner)
		}
		found := false
		for _, p := range o.Perms {
			if p == string(auth.PermOrgAdmin) {
				found = true
			}
		}
		if !found {
			t.Errorf("org %s perms = %v, want it to include %q", o.Slug, o.Perms, auth.PermOrgAdmin)
		}
	}

	raw := rr.Body.String()
	if strings.Contains(raw, "password") || strings.Contains(raw, "oidc") {
		t.Errorf("response body exposes password hash or oidc subject: %s", raw)
	}
}

func TestMeUnauthenticated401(t *testing.T) {
	pool := db.TestPool(t)
	handler := newWebhookHandler(pool, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Error == "" {
		t.Error("error body missing \"error\" key value")
	}
}
