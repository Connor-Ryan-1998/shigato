//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"shigoto/internal/mail"
	"shigoto/internal/testkit"
)

type createInviteResponse struct {
	InviteID int64  `json:"invite_id"`
	Link     string `json:"link"`
	Emailed  bool   `json:"emailed"`
}

type acceptInviteResponse struct {
	OrgSlug string `json:"org_slug"`
}

// tokenFromLink extracts the plaintext token from an invite link, the way
// the SPA's own invite page URL does: <base>/invite/<token>.
func tokenFromLink(t *testing.T, link string) string {
	t.Helper()
	const marker = "/invite/"
	i := strings.Index(link, marker)
	if i < 0 {
		t.Fatalf("link %q has no /invite/ segment", link)
	}
	return link[i+len(marker):]
}

func TestInviteAcceptNewUser(t *testing.T) {
	e := testkit.New(t)
	h := e.Handler(testkit.OnlyModules())
	org, owner := e.OwnerSession("acme", h)
	anon := e.Anonymous(h)

	// Create the invite.
	rr := owner.Do(http.MethodPost, "/api/v1/orgs/acme/invites",
		map[string]string{"email": "newstaff@example.com", "role": "staff"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST invites status = %d, body %s", rr.Code, rr.Body)
	}
	var created createInviteResponse
	if err := json.Unmarshal(rr.Body, &created); err != nil {
		t.Fatalf("decoding invite response: %v", err)
	}
	if created.Link == "" {
		t.Fatalf("invite response has no link")
	}
	token := tokenFromLink(t, created.Link)

	// GET the invite details (public, no session).
	rr = anon.Do(http.MethodGet, "/api/v1/invites/"+token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET invite details status = %d, body %s", rr.Code, rr.Body)
	}
	var details map[string]any
	if err := json.Unmarshal(rr.Body, &details); err != nil {
		t.Fatalf("decoding invite details: %v", err)
	}
	if details["expired"] != false {
		t.Errorf("invite details expired = %v, want false", details["expired"])
	}
	if details["email"] != "newstaff@example.com" {
		t.Errorf("invite details email = %v", details["email"])
	}

	// Accept, not logged in: creates the account.
	rr = anon.Do(http.MethodPost, "/api/v1/invites/"+token+"/accept",
		map[string]string{"name": "New Staff", "password": "a-very-long-password"})
	if rr.Code != http.StatusOK {
		t.Fatalf("accept status = %d, body %s", rr.Code, rr.Body)
	}
	var accepted acceptInviteResponse
	if err := json.Unmarshal(rr.Body, &accepted); err != nil {
		t.Fatalf("decoding accept response: %v", err)
	}
	if accepted.OrgSlug != "acme" {
		t.Errorf("accept org_slug = %q, want acme", accepted.OrgSlug)
	}
	if got := rr.Cookies(); len(got) == 0 {
		t.Errorf("accept response set no session cookie")
	}

	var role string
	var newUserID int64
	err := e.Pool.QueryRow(e.Ctx, `
		SELECT u.id, m.role FROM memberships m
		JOIN users u ON u.id = m.user_id
		WHERE u.email = 'newstaff@example.com' AND m.org_id = $1`, org.ID,
	).Scan(&newUserID, &role)
	if err != nil {
		t.Fatalf("querying new membership: %v", err)
	}
	if role != "staff" {
		t.Errorf("new member role = %q, want staff", role)
	}

	// Accepting writes an audit row for both the new user and the
	// membership.
	var auditCount int
	if err := e.Pool.QueryRow(e.Ctx, `
		SELECT count(*) FROM audit_log
		WHERE org_id = $1 AND actor_user_id = $2 AND action IN ('create', 'accept_invite')`,
		org.ID, newUserID,
	).Scan(&auditCount); err != nil {
		t.Fatalf("counting audit rows: %v", err)
	}
	if auditCount < 2 {
		t.Errorf("audit rows for accept = %d, want at least 2 (user create + membership accept_invite)", auditCount)
	}
}

func TestInviteSingleUse(t *testing.T) {
	e := testkit.New(t)
	h := e.Handler(testkit.OnlyModules())
	_, owner := e.OwnerSession("acme", h)
	anon := e.Anonymous(h)

	rr := owner.Do(http.MethodPost, "/api/v1/orgs/acme/invites",
		map[string]string{"email": "second@example.com", "role": "staff"})
	var created createInviteResponse
	_ = json.Unmarshal(rr.Body, &created)
	token := tokenFromLink(t, created.Link)

	rr = anon.Do(http.MethodPost, "/api/v1/invites/"+token+"/accept",
		map[string]string{"name": "Second", "password": "a-very-long-password"})
	if rr.Code != http.StatusOK {
		t.Fatalf("first accept status = %d, body %s", rr.Code, rr.Body)
	}

	rr = anon.Do(http.MethodPost, "/api/v1/invites/"+token+"/accept",
		map[string]string{"name": "Second", "password": "a-very-long-password"})
	if rr.Code != http.StatusGone {
		t.Fatalf("second accept status = %d, want 410, body %s", rr.Code, rr.Body)
	}
}

func TestInviteEmailSent(t *testing.T) {
	e := testkit.New(t)

	var sent mail.Message
	captured := false
	capture := &mail.Mailer{From: "shigoto@example.com", Send: func(_ context.Context, m mail.Message) error {
		sent = m
		captured = true
		return nil
	}}

	h := e.Handler(testkit.OnlyModules(), testkit.Mailer(capture))
	_, owner := e.OwnerSession("acme", h)

	rr := owner.Do(http.MethodPost, "/api/v1/orgs/acme/invites",
		map[string]string{"email": "mailed@example.com", "role": "staff"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST invites status = %d, body %s", rr.Code, rr.Body)
	}
	var created createInviteResponse
	_ = json.Unmarshal(rr.Body, &created)

	if !captured {
		t.Fatalf("invite email was never sent")
	}
	if !created.Emailed {
		t.Errorf("emailed = false, want true when the mailer succeeds")
	}
	if sent.To != "mailed@example.com" {
		t.Errorf("sent.To = %q, want mailed@example.com", sent.To)
	}
	token := tokenFromLink(t, created.Link)
	if !strings.Contains(sent.Text, token) && !strings.Contains(sent.HTML, token) {
		t.Errorf("sent message doesn't contain the invite link/token: text=%q html=%q", sent.Text, sent.HTML)
	}
}

func TestInviteAcceptLoggedInMatchingEmail(t *testing.T) {
	e := testkit.New(t)
	h := e.Handler(testkit.OnlyModules())
	org, owner := e.OwnerSession("acme", h)

	invitedUser := e.User("invited@example.com", testkit.WithPassword("another-password"))
	invited := e.Login(invitedUser, h)

	rr := owner.Do(http.MethodPost, "/api/v1/orgs/acme/invites",
		map[string]string{"email": "invited@example.com", "role": "bookkeeper"})
	var created createInviteResponse
	_ = json.Unmarshal(rr.Body, &created)
	token := tokenFromLink(t, created.Link)

	rr = invited.Do(http.MethodPost, "/api/v1/invites/"+token+"/accept", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("accept while logged in with a matching email status = %d, want 200, body %s", rr.Code, rr.Body)
	}
	var accepted acceptInviteResponse
	_ = json.Unmarshal(rr.Body, &accepted)
	if accepted.OrgSlug != "acme" {
		t.Errorf("accept org_slug = %q, want acme", accepted.OrgSlug)
	}

	var role string
	if err := e.Pool.QueryRow(e.Ctx,
		"SELECT role FROM memberships WHERE org_id = $1 AND user_id = $2", org.ID, invitedUser.ID,
	).Scan(&role); err != nil {
		t.Fatalf("querying membership: %v", err)
	}
	if role != "bookkeeper" {
		t.Errorf("accepted member role = %q, want bookkeeper", role)
	}
}
