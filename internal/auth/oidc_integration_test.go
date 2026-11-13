//go:build integration

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth/oidctest"
	"shigoto/internal/db"
)

// newOIDCTestAuth builds an *Auth wired to idp, with BaseURL
// "https://shigoto.example" (so RedirectURL is deterministic) and returns a
// router exposing just the two OIDC routes, the way httpapi/server.go
// mounts them.
func newOIDCTestAuth(pool *pgxpool.Pool, idp *oidctest.Server) (*Auth, http.Handler) {
	a := New(pool, true, WithOIDC(OIDCConfig{
		Issuer:       idp.URL,
		ClientID:     "shigoto-client",
		ClientSecret: "shigoto-secret",
		Scopes:       []string{"openid", "email", "profile"},
		RedirectURL:  "https://shigoto.example/api/v1/auth/oidc/callback",
	}))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/auth/oidc/start", a.OIDCStartHandler)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", a.OIDCCallbackHandler)
	return a, mux
}

// startOIDC drives GET /api/v1/auth/oidc/start?next=next and returns the
// redirect's Location URL (state and nonce are query parameters on it) and
// the value of the state-binding cookie Start set, which callbackOIDC needs
// to carry for a callback to pass the CSRF cookie check.
func startOIDC(t *testing.T, mux http.Handler, next string) (loc *url.URL, stateCookie string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/start?next="+url.QueryEscape(next), nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusFound {
		t.Fatalf("start: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	loc, err := url.Parse(rr.Result().Header.Get("Location"))
	if err != nil {
		t.Fatalf("parsing Location: %v", err)
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == oidcStateCookieName {
			stateCookie = c.Value
		}
	}
	if stateCookie == "" {
		t.Fatalf("expected start to set the %s cookie", oidcStateCookieName)
	}
	return loc, stateCookie
}

// callbackOIDC drives GET /api/v1/auth/oidc/callback with the given state, a
// fixed fake code, and (unless empty) the state-binding cookie, and returns
// the resulting redirect's Location header, any Set-Cookie session token,
// and whether the response cleared the state-binding cookie (which the
// callback must do on every return path).
func callbackOIDC(mux http.Handler, state, cookieValue string) (loc *url.URL, sessionCookie string, stateCookieCleared bool, err error) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=fake-code&state="+url.QueryEscape(state), nil)
	if cookieValue != "" {
		req.AddCookie(&http.Cookie{Name: oidcStateCookieName, Value: cookieValue})
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusFound {
		return nil, "", false, errStatus(rr.Code, rr.Body.String())
	}
	loc, err = url.Parse(rr.Result().Header.Get("Location"))
	if err != nil {
		return nil, "", false, err
	}
	for _, c := range rr.Result().Cookies() {
		switch c.Name {
		case SessionCookieName:
			sessionCookie = c.Value
		case oidcStateCookieName:
			stateCookieCleared = c.Value == "" && c.MaxAge < 0
		}
	}
	return loc, sessionCookie, stateCookieCleared, nil
}

type statusErr struct {
	code int
	body string
}

func (e statusErr) Error() string { return e.body }

func errStatus(code int, body string) error { return statusErr{code, body} }

func TestOIDCHappyPathLinksAndSignsIn(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)
	ctx := context.Background()

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "staffer@example.com", "whatever-password")
	addMembership(t, pool, userID, org.ID, Staff)

	_, mux := newOIDCTestAuth(pool, idp)

	loc, stateCookie := startOIDC(t, mux, "/acme")
	state := loc.Query().Get("state")
	nonce := loc.Query().Get("nonce")
	if state == "" || nonce == "" {
		t.Fatalf("expected state and nonce in redirect, got %s", loc)
	}

	// PKCE: the verifier the fake IdP actually receives at /token must
	// match the one Start stored alongside this state.
	var storedVerifier string
	if err := pool.QueryRow(ctx, "SELECT code_verifier FROM oidc_states WHERE state_hash = $1", stateHash(state)).
		Scan(&storedVerifier); err != nil {
		t.Fatalf("querying stored code_verifier: %v", err)
	}

	idp.SetClaims(oidctest.Claims{
		Sub:           "idp-subject-1",
		Email:         "staffer@example.com",
		EmailVerified: true,
		Nonce:         nonce,
	})

	callbackLoc, sessionCookie, stateCookieCleared, err := callbackOIDC(mux, state, stateCookie)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if callbackLoc.Path != "/acme" {
		t.Fatalf("redirect = %s, want /acme", callbackLoc)
	}
	if sessionCookie == "" {
		t.Fatalf("expected a session cookie to be set")
	}
	if !stateCookieCleared {
		t.Fatalf("expected the OIDC state cookie to be cleared on callback")
	}
	if idp.LastCodeVerifier() == "" || idp.LastCodeVerifier() != storedVerifier {
		t.Fatalf("code_verifier sent = %q, want the stored verifier %q", idp.LastCodeVerifier(), storedVerifier)
	}

	var subject *string
	var passwordHash *string
	if err := pool.QueryRow(ctx, "SELECT oidc_subject, password_hash FROM users WHERE id = $1", userID).
		Scan(&subject, &passwordHash); err != nil {
		t.Fatalf("querying user: %v", err)
	}
	if subject == nil || *subject != "idp-subject-1" {
		t.Fatalf("oidc_subject = %v, want idp-subject-1", subject)
	}
	// Ruling 54: linking by verified email clears any existing password.
	if passwordHash != nil {
		t.Fatalf("password_hash = %v, want cleared", *passwordHash)
	}

	// Password login must now fail: SSO proved email ownership, so a
	// password set by someone else must stop working.
	if _, err := Login(ctx, pool, "staffer@example.com", "whatever-password"); err != ErrInvalidCredentials {
		t.Fatalf("Login after OIDC link: err = %v, want ErrInvalidCredentials", err)
	}

	var found bool
	if err := pool.QueryRow(ctx,
		"SELECT true FROM audit_log WHERE org_id = $1 AND entity = 'user' AND entity_id = $2",
		org.ID, userID,
	).Scan(&found); err != nil {
		t.Fatalf("querying audit_log: %v", err)
	}
	if !found {
		t.Fatalf("expected an audit_log entry for the oidc_subject link")
	}
}

// TestOIDCLinkRevokesExistingSessions covers Ruling 64 (security): linking
// an existing account to an OIDC subject must kill every session that
// account already held (e.g. one from a password login before the user ever
// went through SSO), not just clear the password.
func TestOIDCLinkRevokesExistingSessions(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)
	ctx := context.Background()

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "staffer@example.com", "whatever-password")
	addMembership(t, pool, userID, org.ID, Staff)

	oldToken, err := createSession(ctx, pool, userID)
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	if _, err := userIDForToken(ctx, pool, oldToken); err != nil {
		t.Fatalf("pre-link session should be valid before the link: %v", err)
	}

	_, mux := newOIDCTestAuth(pool, idp)
	loc, stateCookie := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "idp-subject-revoke", Email: "staffer@example.com", EmailVerified: true,
		Nonce: loc.Query().Get("nonce"),
	})

	_, sessionCookie, _, err := callbackOIDC(mux, loc.Query().Get("state"), stateCookie)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}

	if _, err := userIDForToken(ctx, pool, oldToken); err == nil {
		t.Fatalf("expected the pre-link session to be revoked by the OIDC link")
	}
	if _, err := userIDForToken(ctx, pool, sessionCookie); err != nil {
		t.Fatalf("the session minted by the callback itself should be valid: %v", err)
	}
}

func TestOIDCAlreadyLinkedUserUntouched(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)
	ctx := context.Background()

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "staffer@example.com", "a-password")
	addMembership(t, pool, userID, org.ID, Staff)
	if _, err := pool.Exec(ctx, "UPDATE users SET oidc_subject = $1 WHERE id = $2", "idp-subject-2", userID); err != nil {
		t.Fatalf("pre-linking subject: %v", err)
	}

	_, mux := newOIDCTestAuth(pool, idp)
	loc, stateCookie := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "idp-subject-2", Email: "staffer@example.com", EmailVerified: true,
		Nonce: loc.Query().Get("nonce"),
	})

	if _, _, _, err := callbackOIDC(mux, loc.Query().Get("state"), stateCookie); err != nil {
		t.Fatalf("callback: %v", err)
	}

	// Already linked by subject: password must be untouched.
	if _, err := Login(ctx, pool, "staffer@example.com", "a-password"); err != nil {
		t.Fatalf("Login for already-linked user: err = %v, want nil", err)
	}
}

func TestOIDCReplayedStateRejected(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "staffer@example.com", "whatever")
	addMembership(t, pool, userID, org.ID, Staff)

	_, mux := newOIDCTestAuth(pool, idp)
	loc, stateCookie := startOIDC(t, mux, "/acme")
	state := loc.Query().Get("state")
	idp.SetClaims(oidctest.Claims{Sub: "sub-x", Email: "staffer@example.com", EmailVerified: true, Nonce: loc.Query().Get("nonce")})

	if _, _, _, err := callbackOIDC(mux, state, stateCookie); err != nil {
		t.Fatalf("first callback: %v", err)
	}

	// The real browser still has its state cookie (Start only set it once);
	// a second callback with the same state and cookie is a replay of an
	// already-consumed row, not a CSRF mismatch.
	replayLoc, _, _, err := callbackOIDC(mux, state, stateCookie)
	if err != nil {
		t.Fatalf("replayed callback: %v", err)
	}
	assertLoginError(t, replayLoc, "sso_expired")
}

func TestOIDCForeignStateRejected(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)

	_, mux := newOIDCTestAuth(pool, idp)
	startOIDC(t, mux, "/acme") // establishes the route/IdP but we never use its state

	buf := make([]byte, 32)
	foreignState := base64.RawURLEncoding.EncodeToString(buf)

	// The cookie must match the state query parameter for the callback to
	// get past the CSRF check at all and exercise the "unknown state"
	// rejection specifically, rather than the cookie-mismatch one.
	loc, _, _, err := callbackOIDC(mux, foreignState, foreignState)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	assertLoginError(t, loc, "sso_expired")
}

func TestOIDCCallbackMissingCookieRejected(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)
	ctx := context.Background()

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "staffer@example.com", "whatever")
	addMembership(t, pool, userID, org.ID, Staff)

	_, mux := newOIDCTestAuth(pool, idp)
	loc, _ := startOIDC(t, mux, "/acme")
	state := loc.Query().Get("state")
	idp.SetClaims(oidctest.Claims{Sub: "sub-nocookie", Email: "staffer@example.com", EmailVerified: true, Nonce: loc.Query().Get("nonce")})

	callbackLoc, _, _, err := callbackOIDC(mux, state, "")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	assertLoginError(t, callbackLoc, "sso_expired")

	// The state row must be untouched: a missing cookie fails the CSRF
	// check before the row is ever consumed, so the real browser (which
	// does still have its cookie) can retry.
	var usedAt *time.Time
	if err := pool.QueryRow(ctx, "SELECT used_at FROM oidc_states WHERE state_hash = $1", stateHash(state)).Scan(&usedAt); err != nil {
		t.Fatalf("querying oidc_states: %v", err)
	}
	if usedAt != nil {
		t.Fatalf("used_at = %v, want nil (state must not be consumed without the cookie)", *usedAt)
	}
}

func TestOIDCCallbackWrongCookieRejected(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)
	ctx := context.Background()

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "staffer@example.com", "whatever")
	addMembership(t, pool, userID, org.ID, Staff)

	_, mux := newOIDCTestAuth(pool, idp)
	loc, _ := startOIDC(t, mux, "/acme")
	state := loc.Query().Get("state")
	idp.SetClaims(oidctest.Claims{Sub: "sub-wrongcookie", Email: "staffer@example.com", EmailVerified: true, Nonce: loc.Query().Get("nonce")})

	callbackLoc, _, _, err := callbackOIDC(mux, state, "not-the-right-cookie-value")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	assertLoginError(t, callbackLoc, "sso_expired")

	var usedAt *time.Time
	if err := pool.QueryRow(ctx, "SELECT used_at FROM oidc_states WHERE state_hash = $1", stateHash(state)).Scan(&usedAt); err != nil {
		t.Fatalf("querying oidc_states: %v", err)
	}
	if usedAt != nil {
		t.Fatalf("used_at = %v, want nil (state must not be consumed with a mismatched cookie)", *usedAt)
	}
}

func TestOIDCNonceMismatchRejected(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "staffer@example.com", "whatever")
	addMembership(t, pool, userID, org.ID, Staff)

	_, mux := newOIDCTestAuth(pool, idp)
	loc, stateCookie := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "sub-y", Email: "staffer@example.com", EmailVerified: true,
		Nonce: "wrong-nonce-" + loc.Query().Get("nonce"),
	})

	callbackLoc, _, _, err := callbackOIDC(mux, loc.Query().Get("state"), stateCookie)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	assertLoginError(t, callbackLoc, "sso_failed")
}

func TestOIDCEmailUnverifiedRejected(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "staffer@example.com", "whatever")
	addMembership(t, pool, userID, org.ID, Staff)

	_, mux := newOIDCTestAuth(pool, idp)
	loc, stateCookie := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "sub-z", Email: "staffer@example.com", EmailVerified: false,
		Nonce: loc.Query().Get("nonce"),
	})

	callbackLoc, _, _, err := callbackOIDC(mux, loc.Query().Get("state"), stateCookie)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	assertLoginError(t, callbackLoc, "email_unverified")
}

func TestOIDCEmailVerifiedAsStringRejected(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "staffer@example.com", "whatever")
	addMembership(t, pool, userID, org.ID, Staff)

	_, mux := newOIDCTestAuth(pool, idp)
	loc, stateCookie := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "sub-string-verified", Email: "staffer@example.com",
		Nonce: loc.Query().Get("nonce"),
		// A non-boolean email_verified must fail closed, not be coerced
		// into true.
		EmailVerifiedRaw: "true",
	})

	callbackLoc, _, _, err := callbackOIDC(mux, loc.Query().Get("state"), stateCookie)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	assertLoginError(t, callbackLoc, "sso_failed")
}

func TestOIDCWrongIssuerRejected(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "staffer@example.com", "whatever")
	addMembership(t, pool, userID, org.ID, Staff)

	_, mux := newOIDCTestAuth(pool, idp)
	loc, stateCookie := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "sub-wrong-iss", Email: "staffer@example.com", EmailVerified: true,
		Nonce: loc.Query().Get("nonce"),
		Iss:   "https://not-the-configured-issuer.example",
	})

	callbackLoc, _, _, err := callbackOIDC(mux, loc.Query().Get("state"), stateCookie)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	assertLoginError(t, callbackLoc, "sso_failed")
}

func TestOIDCWrongAudienceRejected(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "staffer@example.com", "whatever")
	addMembership(t, pool, userID, org.ID, Staff)

	_, mux := newOIDCTestAuth(pool, idp)
	loc, stateCookie := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "sub-wrong-aud", Email: "staffer@example.com", EmailVerified: true,
		Nonce: loc.Query().Get("nonce"),
		Aud:   "someone-elses-client-id",
	})

	callbackLoc, _, _, err := callbackOIDC(mux, loc.Query().Get("state"), stateCookie)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	assertLoginError(t, callbackLoc, "sso_failed")
}

func TestOIDCExpiredIDTokenRejected(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "staffer@example.com", "whatever")
	addMembership(t, pool, userID, org.ID, Staff)

	_, mux := newOIDCTestAuth(pool, idp)
	loc, stateCookie := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "sub-expired", Email: "staffer@example.com", EmailVerified: true,
		Nonce:     loc.Query().Get("nonce"),
		ExpiresIn: -time.Minute,
	})

	callbackLoc, _, _, err := callbackOIDC(mux, loc.Query().Get("state"), stateCookie)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	assertLoginError(t, callbackLoc, "sso_failed")
}

func TestOIDCUnknownEmailNotInvited(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)

	_, mux := newOIDCTestAuth(pool, idp)
	loc, stateCookie := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "sub-unknown", Email: "nobody@example.com", EmailVerified: true,
		Nonce: loc.Query().Get("nonce"),
	})

	callbackLoc, _, _, err := callbackOIDC(mux, loc.Query().Get("state"), stateCookie)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	assertLoginError(t, callbackLoc, "not_invited")
}

func TestOIDCOutstandingInviteOnlyEmailNotInvited(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)
	ctx := context.Background()

	org := createTestOrg(t, pool, "acme")
	// An outstanding invite, but no account yet: OIDC never auto-accepts
	// an invite on the invitee's behalf, however it got there. They must
	// accept the invite (creating a password account) first, then SSO
	// links to it on a later sign-in.
	ownerID := createTestUser(t, pool, "owner@example.com", "owner-password")
	if _, _, err := CreateInvite(ctx, pool, org.ID, "invitee@example.com", "staff", ownerID); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	_, mux := newOIDCTestAuth(pool, idp)
	loc, stateCookie := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "sub-invitee", Email: "invitee@example.com", EmailVerified: true,
		Nonce: loc.Query().Get("nonce"),
	})

	callbackLoc, _, _, err := callbackOIDC(mux, loc.Query().Get("state"), stateCookie)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	assertLoginError(t, callbackLoc, "not_invited")
}

func TestOIDCTenantOnlyUserNotStaff(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)
	ctx := context.Background()

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "tenant@example.com", "whatever")
	contactID := createTestContact(t, pool, org.ID)
	if _, err := pool.Exec(ctx,
		"INSERT INTO memberships (user_id, org_id, role, contact_id) VALUES ($1, $2, 'tenant', $3)",
		userID, org.ID, contactID,
	); err != nil {
		t.Fatalf("adding tenant membership: %v", err)
	}

	_, mux := newOIDCTestAuth(pool, idp)
	loc, stateCookie := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "sub-tenant", Email: "tenant@example.com", EmailVerified: true,
		Nonce: loc.Query().Get("nonce"),
	})

	callbackLoc, _, _, err := callbackOIDC(mux, loc.Query().Get("state"), stateCookie)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	assertLoginError(t, callbackLoc, "not_staff")

	var subject *string
	if err := pool.QueryRow(ctx, "SELECT oidc_subject FROM users WHERE id = $1", userID).Scan(&subject); err != nil {
		t.Fatalf("querying user: %v", err)
	}
	if subject != nil {
		t.Fatalf("oidc_subject = %v, want nil (not_staff user must not be linked)", *subject)
	}
}

func TestOIDCSubjectConflictRejected(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)
	ctx := context.Background()

	org := createTestOrg(t, pool, "acme")
	userA := createTestUser(t, pool, "a@example.com", "whatever")
	addMembership(t, pool, userA, org.ID, Staff)
	if _, err := pool.Exec(ctx, "UPDATE users SET oidc_subject = $1 WHERE id = $2", "already-taken", userA); err != nil {
		t.Fatalf("linking userA: %v", err)
	}
	userB := createTestUser(t, pool, "b@example.com", "whatever")
	addMembership(t, pool, userB, org.ID, Staff)

	// userB is already linked to a different subject. A token claiming
	// userB's email with yet another subject must be rejected rather than
	// silently re-linking the account to a second identity.
	if _, err := pool.Exec(ctx, "UPDATE users SET oidc_subject = $1 WHERE id = $2", "userb-existing-subject", userB); err != nil {
		t.Fatalf("pre-linking userB: %v", err)
	}

	_, mux := newOIDCTestAuth(pool, idp)
	loc, stateCookie := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "a-new-subject-claiming-userb-email", Email: "b@example.com", EmailVerified: true,
		Nonce: loc.Query().Get("nonce"),
	})

	callbackLoc, _, _, err := callbackOIDC(mux, loc.Query().Get("state"), stateCookie)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	assertLoginError(t, callbackLoc, "sso_conflict")
}

func TestOIDCStartNextOpenRedirectFallsBackToRoot(t *testing.T) {
	cases := []struct {
		name string
		next string
	}{
		{"protocol-relative", "//evil.com"},
		{"backslash-escape", "/\\evil.com"},
		// A tab: some browsers strip control characters before resolving a
		// URL, so "/\t/evil.com" would otherwise normalize to the
		// protocol-relative "//evil.com" once followed.
		{"tab-smuggled-protocol-relative", "/\t/evil.com"},
		{"newline-smuggled", "/\n/evil.com"},
		{"absolute-url", "https://evil.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := db.TestPool(t)
			idp := oidctest.New(t)

			_, mux := newOIDCTestAuth(pool, idp)
			loc, _ := startOIDC(t, mux, tc.next)
			state := loc.Query().Get("state")

			var next string
			if err := pool.QueryRow(context.Background(),
				"SELECT next FROM oidc_states WHERE state_hash = $1", stateHash(state),
			).Scan(&next); err != nil {
				t.Fatalf("querying oidc_states: %v", err)
			}
			if next != "/" {
				t.Fatalf("next = %q, want \"/\"", next)
			}
		})
	}
}

// stateHash mirrors how the implementation hashes the plaintext state token
// for storage, so the test above can look its row up directly.
func stateHash(state string) []byte {
	sum := sha256.Sum256([]byte(state))
	return sum[:]
}

func assertLoginError(t *testing.T, loc *url.URL, code string) {
	t.Helper()
	if loc.Path != "/login" {
		t.Fatalf("redirect path = %s, want /login", loc.Path)
	}
	if got := loc.Query().Get("error"); got != code {
		t.Fatalf("error = %q, want %q", got, code)
	}
}

func createTestContact(t *testing.T, pool *pgxpool.Pool, orgID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		"INSERT INTO contacts (org_id, kind, name) VALUES ($1, 'tenant', 'Tenant Contact') RETURNING id",
		orgID,
	).Scan(&id); err != nil {
		t.Fatalf("creating contact: %v", err)
	}
	return id
}

func TestMetaReportsSSO(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)
	aWithSSO, _ := newOIDCTestAuth(pool, idp)
	if !aWithSSO.OIDCEnabled() {
		t.Fatalf("OIDCEnabled() = false, want true when WithOIDC is set")
	}

	aWithout := New(pool, true)
	if aWithout.OIDCEnabled() {
		t.Fatalf("OIDCEnabled() = true, want false when OIDC isn't configured")
	}
}

// PruneOIDCStates is exercised directly against the DB; it doesn't need the
// fake IdP.
func TestPruneOIDCStates(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()

	insertState := func(hash string, usedAt *time.Time, expiresAt time.Time) {
		sum := sha256.Sum256([]byte(hash))
		if _, err := pool.Exec(ctx,
			"INSERT INTO oidc_states (state_hash, nonce, code_verifier, next, expires_at, used_at) VALUES ($1, 'n', 'v', '/', $2, $3)",
			sum[:], expiresAt, usedAt,
		); err != nil {
			t.Fatalf("inserting state: %v", err)
		}
	}

	now := time.Now()
	used := now
	insertState("used", &used, now.Add(time.Hour))
	insertState("expired", nil, now.Add(-time.Minute))
	insertState("live", nil, now.Add(time.Hour))

	if err := PruneOIDCStates(ctx, pool, now); err != nil {
		t.Fatalf("PruneOIDCStates: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM oidc_states").Scan(&count); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1 (only the live state)", count)
	}
}
