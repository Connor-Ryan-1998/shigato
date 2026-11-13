//go:build integration

package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"shigoto/internal/auth"
	"shigoto/internal/testkit"
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
	e := testkit.New(t)
	handler := e.Handler(testkit.OnlyModules())

	orgB := e.Org("org-b")
	orgA := e.Org("org-a")
	user := e.User("owner@example.com", testkit.WithPassword("correct-password"))
	userID := user.ID
	e.Member(user, orgA, auth.Owner)
	e.Member(user, orgB, auth.Owner)

	rr := e.Login(user, handler).Do(http.MethodGet, "/api/v1/me", nil)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body)
	}

	var body meResponse
	if err := json.Unmarshal(rr.Body, &body); err != nil {
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

	raw := string(rr.Body)
	if strings.Contains(raw, "password") || strings.Contains(raw, "oidc") {
		t.Errorf("response body exposes password hash or oidc subject: %s", raw)
	}
}
