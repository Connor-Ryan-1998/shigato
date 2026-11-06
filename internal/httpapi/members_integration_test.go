//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// insertExpiredInvite inserts an invite row that's already past its expiry,
// bypassing auth.CreateInvite (which always sets a 7-day-out expires_at), so
// TestInviteExpired can exercise the expired-token path without waiting a
// week.
func insertExpiredInvite(t *testing.T, pool *pgxpool.Pool, orgID int64, email, role, token string) {
	t.Helper()
	sum := sha256TestHash(token)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO invites (org_id, email, role, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4, now() - interval '1 hour')`,
		orgID, email, role, sum,
	); err != nil {
		t.Fatalf("insertExpiredInvite: %v", err)
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

func TestInviteExpired(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")

	h := newMembersTestHandler(pool, nil)

	insertExpiredInvite(t, pool, org.ID, "expired@example.com", "staff", "expired-test-token")

	rr := doMembersRequest(h, membersRequestJSON(t, http.MethodGet, "/api/v1/invites/expired-test-token", "", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET expired invite status = %d, body %s", rr.Code, rr.Body.String())
	}
	var details map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &details)
	if details["expired"] != true {
		t.Errorf("expired invite details expired = %v, want true", details["expired"])
	}

	rr = doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/invites/expired-test-token/accept", "",
		map[string]string{"name": "Late", "password": "a-very-long-password"}))
	if rr.Code != http.StatusGone {
		t.Fatalf("accept expired invite status = %d, want 410, body %s", rr.Code, rr.Body.String())
	}
}

func TestInviteEmailMismatchWhenLoggedIn409(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	ownerID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	addMembership(t, pool, ownerID, org.ID, "owner")
	ownerToken := settingsTestLogin(t, pool, "owner@example.com", "correct-password")

	otherID := webhookTestUser(t, pool, "other@example.com", "another-password")
	_ = otherID
	otherToken := settingsTestLogin(t, pool, "other@example.com", "another-password")

	h := newMembersTestHandler(pool, nil)

	rr := doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/orgs/acme/invites", ownerToken,
		map[string]string{"email": "invited@example.com", "role": "staff"}))
	var created createInviteResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	token := tokenFromLink(t, created.Link)

	rr = doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/invites/"+token+"/accept", otherToken, nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("accept with mismatched logged-in email status = %d, want 409, body %s", rr.Code, rr.Body.String())
	}
}

func TestLastOwnerProtected(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	ownerID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	addMembership(t, pool, ownerID, org.ID, "owner")
	ownerToken := settingsTestLogin(t, pool, "owner@example.com", "correct-password")

	h := newMembersTestHandler(pool, nil)

	rr := doMembersRequest(h, membersRequestJSON(t, http.MethodPut,
		"/api/v1/orgs/acme/members/"+itoa(ownerID), ownerToken, map[string]string{"role": "staff"}))
	if rr.Code != http.StatusConflict {
		t.Fatalf("demote last owner status = %d, want 409, body %s", rr.Code, rr.Body.String())
	}

	rr = doMembersRequest(h, membersRequestJSON(t, http.MethodDelete,
		"/api/v1/orgs/acme/members/"+itoa(ownerID), ownerToken, nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("remove last owner status = %d, want 409, body %s", rr.Code, rr.Body.String())
	}

	// With a second owner present, demoting/removing the first succeeds.
	secondOwnerID := webhookTestUser(t, pool, "owner2@example.com", "correct-password")
	addMembership(t, pool, secondOwnerID, org.ID, "owner")

	rr = doMembersRequest(h, membersRequestJSON(t, http.MethodPut,
		"/api/v1/orgs/acme/members/"+itoa(ownerID), ownerToken, map[string]string{"role": "staff"}))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("demote with a second owner present status = %d, want 204, body %s", rr.Code, rr.Body.String())
	}
}

func TestMembersCrossOrg404(t *testing.T) {
	pool := db.TestPool(t)
	orgA := webhookTestOrg(t, pool, "acme")
	orgB := webhookTestOrg(t, pool, "beta")

	ownerID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	addMembership(t, pool, ownerID, orgA.ID, "owner")
	ownerToken := settingsTestLogin(t, pool, "owner@example.com", "correct-password")

	betaUserID := webhookTestUser(t, pool, "betastaff@example.com", "correct-password")
	addMembership(t, pool, betaUserID, orgB.ID, "staff")

	h := newMembersTestHandler(pool, nil)

	rr := doMembersRequest(h, membersRequestJSON(t, http.MethodPut,
		"/api/v1/orgs/acme/members/"+itoa(betaUserID), ownerToken, map[string]string{"role": "staff"}))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("PUT cross-org member status = %d, want 404, body %s", rr.Code, rr.Body.String())
	}

	rr = doMembersRequest(h, membersRequestJSON(t, http.MethodDelete,
		"/api/v1/orgs/acme/members/"+itoa(betaUserID), ownerToken, nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("DELETE cross-org member status = %d, want 404, body %s", rr.Code, rr.Body.String())
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

func TestInviteAcceptExistingUserNotLoggedIn409(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	ownerID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	addMembership(t, pool, ownerID, org.ID, "owner")
	ownerToken := settingsTestLogin(t, pool, "owner@example.com", "correct-password")

	// An account already exists for this email (unrelated to the org).
	webhookTestUser(t, pool, "existing@example.com", "some-other-password")

	h := newMembersTestHandler(pool, nil)

	rr := doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/orgs/acme/invites", ownerToken,
		map[string]string{"email": "existing@example.com", "role": "staff"}))
	var created createInviteResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	token := tokenFromLink(t, created.Link)

	rr = doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/invites/"+token+"/accept", "",
		map[string]string{"name": "Existing", "password": "a-very-long-password"}))
	if rr.Code != http.StatusConflict {
		t.Fatalf("accept with existing account status = %d, want 409, body %s", rr.Code, rr.Body.String())
	}

	// The token must still be usable: the failed accept rolled back the
	// token consumption along with everything else in that transaction. Now
	// sign in as the existing user and accept while logged in.
	existingToken := settingsTestLogin(t, pool, "existing@example.com", "some-other-password")
	rr = doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/invites/"+token+"/accept", existingToken, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("accept while logged in after a failed anonymous accept status = %d, want 200, body %s", rr.Code, rr.Body.String())
	}
}

func TestInviteAcceptShortPassword422(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	ownerID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	addMembership(t, pool, ownerID, org.ID, "owner")
	ownerToken := settingsTestLogin(t, pool, "owner@example.com", "correct-password")

	h := newMembersTestHandler(pool, nil)

	rr := doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/orgs/acme/invites", ownerToken,
		map[string]string{"email": "shortpw@example.com", "role": "staff"}))
	var created createInviteResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	token := tokenFromLink(t, created.Link)

	rr = doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/invites/"+token+"/accept", "",
		map[string]string{"name": "Short", "password": "too-short"}))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("accept with short password status = %d, want 422, body %s", rr.Code, rr.Body.String())
	}

	// The token is still valid afterwards (the validation failure rolled
	// back the token consumption).
	rr = doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/invites/"+token+"/accept", "",
		map[string]string{"name": "Short", "password": "a-very-long-password"}))
	if rr.Code != http.StatusOK {
		t.Fatalf("accept with a valid password after a short-password failure status = %d, want 200, body %s", rr.Code, rr.Body.String())
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

func TestInviteRevokeCrossOrg404(t *testing.T) {
	pool := db.TestPool(t)
	orgA := webhookTestOrg(t, pool, "acme")
	orgB := webhookTestOrg(t, pool, "beta")

	ownerAID := webhookTestUser(t, pool, "ownera@example.com", "correct-password")
	addMembership(t, pool, ownerAID, orgA.ID, "owner")
	ownerAToken := settingsTestLogin(t, pool, "ownera@example.com", "correct-password")

	ownerBID := webhookTestUser(t, pool, "ownerb@example.com", "correct-password")
	addMembership(t, pool, ownerBID, orgB.ID, "owner")
	ownerBToken := settingsTestLogin(t, pool, "ownerb@example.com", "correct-password")

	h := newMembersTestHandler(pool, nil)

	rr := doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/orgs/beta/invites", ownerBToken,
		map[string]string{"email": "betainvitee@example.com", "role": "staff"}))
	var created createInviteResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	if created.InviteID == 0 {
		t.Fatalf("expected an invite id, got response %s", rr.Body.String())
	}

	rr = doMembersRequest(h, membersRequestJSON(t, http.MethodDelete,
		"/api/v1/orgs/acme/invites/"+itoa(created.InviteID), ownerAToken, nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-org revoke status = %d, want 404, body %s", rr.Code, rr.Body.String())
	}
}

func TestInviteEmailedFalseWithoutMailer(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	ownerID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	addMembership(t, pool, ownerID, org.ID, "owner")
	ownerToken := settingsTestLogin(t, pool, "owner@example.com", "correct-password")

	h := newMembersTestHandler(pool, nil)

	rr := doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/orgs/acme/invites", ownerToken,
		map[string]string{"email": "nomailer@example.com", "role": "staff"}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST invites status = %d, body %s", rr.Code, rr.Body.String())
	}
	var created createInviteResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding invite response: %v", err)
	}
	if created.Emailed {
		t.Errorf("emailed = true with no mailer configured, want false")
	}
	if created.Link == "" {
		t.Errorf("link must still be returned when emailed is false")
	}
}

func TestUpdateMemberRolePortalRole409(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	ownerID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	addMembership(t, pool, ownerID, org.ID, "owner")
	ownerToken := settingsTestLogin(t, pool, "owner@example.com", "correct-password")

	tenantID := webhookTestUser(t, pool, "tenant@example.com", "correct-password")
	addMembership(t, pool, tenantID, org.ID, "tenant")

	h := newMembersTestHandler(pool, nil)

	rr := doMembersRequest(h, membersRequestJSON(t, http.MethodPut,
		"/api/v1/orgs/acme/members/"+itoa(tenantID), ownerToken, map[string]string{"role": "staff"}))
	if rr.Code != http.StatusConflict {
		t.Fatalf("PUT role on a tenant membership status = %d, want 409, body %s", rr.Code, rr.Body.String())
	}
}

func TestCreateInviteInvalidEmail422(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	ownerID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	addMembership(t, pool, ownerID, org.ID, "owner")
	ownerToken := settingsTestLogin(t, pool, "owner@example.com", "correct-password")

	h := newMembersTestHandler(pool, nil)

	rr := doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/orgs/acme/invites", ownerToken,
		map[string]string{"email": "not-an-email", "role": "staff"}))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid email status = %d, want 422, body %s", rr.Code, rr.Body.String())
	}
}

func TestCreateInviteAlreadyMember409(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	ownerID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	addMembership(t, pool, ownerID, org.ID, "owner")
	ownerToken := settingsTestLogin(t, pool, "owner@example.com", "correct-password")

	staffID := webhookTestUser(t, pool, "staffer@example.com", "correct-password")
	addMembership(t, pool, staffID, org.ID, "staff")

	h := newMembersTestHandler(pool, nil)

	rr := doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/orgs/acme/invites", ownerToken,
		map[string]string{"email": "staffer@example.com", "role": "bookkeeper"}))
	if rr.Code != http.StatusConflict {
		t.Fatalf("invite an existing member status = %d, want 409, body %s", rr.Code, rr.Body.String())
	}
}

func TestStaffCannotInvite403(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	staffID := webhookTestUser(t, pool, "staffer@example.com", "correct-password")
	addMembership(t, pool, staffID, org.ID, "staff")
	staffToken := settingsTestLogin(t, pool, "staffer@example.com", "correct-password")

	h := newMembersTestHandler(pool, nil)

	rr := doMembersRequest(h, membersRequestJSON(t, http.MethodPost, "/api/v1/orgs/acme/invites", staffToken,
		map[string]string{"email": "nope@example.com", "role": "staff"}))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("staff POST invites status = %d, want 403, body %s", rr.Code, rr.Body.String())
	}
}

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}

// sha256TestHash mirrors internal/auth's unexported hashToken, duplicated
// here only for insertExpiredInvite's direct-SQL fixture (there's no
// exported way to hash a token from outside package auth).
func sha256TestHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
