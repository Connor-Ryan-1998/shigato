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

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/config"
	"shigoto/internal/db"
	"shigoto/internal/mail"
)

// newMembersTestHandler builds a handler with GetEnv returning "" (no
// Stripe provider resolves) and the given mailer, so TestInviteEmailSent can
// inject a capture mailer while every other test leaves mail disabled (nil
// Mailer, link-only).
func newMembersTestHandler(pool *pgxpool.Pool, mailer *mail.Mailer) http.Handler {
	a := auth.New(pool, false)
	return New(Deps{
		Pool:   pool,
		Auth:   a,
		Cfg:    config.Config{Modules: map[string]bool{}, BaseURL: "https://shigoto.example"},
		GetEnv: func(string) string { return "" },
		Mailer: mailer,
	})
}

// addMembership inserts a membership row directly, for setting up test
// fixtures that already have a member rather than going through the invite
// flow.
func addMembership(t *testing.T, pool *pgxpool.Pool, userID, orgID int64, role string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		"INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, $3)",
		userID, orgID, role,
	); err != nil {
		t.Fatalf("addMembership: %v", err)
	}
}

func membersRequestJSON(t *testing.T, method, url, token string, body any) *http.Request {
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

func doMembersRequest(handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

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
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	ownerID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	addMembership(t, pool, ownerID, org.ID, "owner")
	ownerToken := settingsTestLogin(t, pool, "owner@example.com", "correct-password")

	h := newMembersTestHandler(pool, nil)

	// Create the invite.
	rr := doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/orgs/acme/invites", ownerToken,
		map[string]string{"email": "newstaff@example.com", "role": "staff"}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST invites status = %d, body %s", rr.Code, rr.Body.String())
	}
	var created createInviteResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding invite response: %v", err)
	}
	if created.Link == "" {
		t.Fatalf("invite response has no link")
	}
	token := tokenFromLink(t, created.Link)

	// GET the invite details (public, no session).
	rr = doMembersRequest(h, membersRequestJSON(t, http.MethodGet, "/api/v1/invites/"+token, "", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET invite details status = %d, body %s", rr.Code, rr.Body.String())
	}
	var details map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &details); err != nil {
		t.Fatalf("decoding invite details: %v", err)
	}
	if details["expired"] != false {
		t.Errorf("invite details expired = %v, want false", details["expired"])
	}
	if details["email"] != "newstaff@example.com" {
		t.Errorf("invite details email = %v", details["email"])
	}

	// Accept, not logged in: creates the account.
	rr = doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/invites/"+token+"/accept", "",
		map[string]string{"name": "New Staff", "password": "a-very-long-password"}))
	if rr.Code != http.StatusOK {
		t.Fatalf("accept status = %d, body %s", rr.Code, rr.Body.String())
	}
	var accepted acceptInviteResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decoding accept response: %v", err)
	}
	if accepted.OrgSlug != "acme" {
		t.Errorf("accept org_slug = %q, want acme", accepted.OrgSlug)
	}
	if got := rr.Result().Cookies(); len(got) == 0 {
		t.Errorf("accept response set no session cookie")
	}

	var role string
	var newUserID int64
	err := pool.QueryRow(context.Background(), `
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
	if err := pool.QueryRow(context.Background(), `
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
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	ownerID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	addMembership(t, pool, ownerID, org.ID, "owner")
	ownerToken := settingsTestLogin(t, pool, "owner@example.com", "correct-password")

	h := newMembersTestHandler(pool, nil)

	rr := doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/orgs/acme/invites", ownerToken,
		map[string]string{"email": "second@example.com", "role": "staff"}))
	var created createInviteResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	token := tokenFromLink(t, created.Link)

	rr = doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/invites/"+token+"/accept", "",
		map[string]string{"name": "Second", "password": "a-very-long-password"}))
	if rr.Code != http.StatusOK {
		t.Fatalf("first accept status = %d, body %s", rr.Code, rr.Body.String())
	}

	rr = doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/invites/"+token+"/accept", "",
		map[string]string{"name": "Second", "password": "a-very-long-password"}))
	if rr.Code != http.StatusGone {
		t.Fatalf("second accept status = %d, want 410, body %s", rr.Code, rr.Body.String())
	}
}

func TestInviteEmailSent(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	ownerID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	addMembership(t, pool, ownerID, org.ID, "owner")
	ownerToken := settingsTestLogin(t, pool, "owner@example.com", "correct-password")

	var sent mail.Message
	captured := false
	capture := &mail.Mailer{From: "shigoto@example.com", Send: func(_ context.Context, m mail.Message) error {
		sent = m
		captured = true
		return nil
	}}

	h := newMembersTestHandler(pool, capture)

	rr := doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/orgs/acme/invites", ownerToken,
		map[string]string{"email": "mailed@example.com", "role": "staff"}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST invites status = %d, body %s", rr.Code, rr.Body.String())
	}
	var created createInviteResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

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
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	ownerID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	addMembership(t, pool, ownerID, org.ID, "owner")
	ownerToken := settingsTestLogin(t, pool, "owner@example.com", "correct-password")

	invitedID := webhookTestUser(t, pool, "invited@example.com", "another-password")
	invitedToken := settingsTestLogin(t, pool, "invited@example.com", "another-password")

	h := newMembersTestHandler(pool, nil)

	rr := doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/orgs/acme/invites", ownerToken,
		map[string]string{"email": "invited@example.com", "role": "bookkeeper"}))
	var created createInviteResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	token := tokenFromLink(t, created.Link)

	rr = doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/invites/"+token+"/accept", invitedToken, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("accept while logged in with a matching email status = %d, want 200, body %s", rr.Code, rr.Body.String())
	}
	var accepted acceptInviteResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &accepted)
	if accepted.OrgSlug != "acme" {
		t.Errorf("accept org_slug = %q, want acme", accepted.OrgSlug)
	}

	var role string
	if err := pool.QueryRow(context.Background(),
		"SELECT role FROM memberships WHERE org_id = $1 AND user_id = $2", org.ID, invitedID,
	).Scan(&role); err != nil {
		t.Fatalf("querying membership: %v", err)
	}
	if role != "bookkeeper" {
		t.Errorf("accepted member role = %q, want bookkeeper", role)
	}
}
