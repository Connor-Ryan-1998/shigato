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
// state and nonce the IdP redirect carries, plus the Location URL itself.
func startOIDC(t *testing.T, mux http.Handler, next string) (loc *url.URL) {
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
	return loc
}

// callbackOIDC drives GET /api/v1/auth/oidc/callback with the given state
// and a fixed fake code, and returns the resulting redirect's Location
// header and any Set-Cookie session token.
func callbackOIDC(mux http.Handler, state string) (loc *url.URL, sessionCookie string, err error) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=fake-code&state="+url.QueryEscape(state), nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusFound {
		return nil, "", errStatus(rr.Code, rr.Body.String())
	}
	loc, err = url.Parse(rr.Result().Header.Get("Location"))
	if err != nil {
		return nil, "", err
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c.Value
		}
	}
	return loc, sessionCookie, nil
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

	loc := startOIDC(t, mux, "/acme")
	state := loc.Query().Get("state")
	nonce := loc.Query().Get("nonce")
	if state == "" || nonce == "" {
		t.Fatalf("expected state and nonce in redirect, got %s", loc)
	}

	idp.SetClaims(oidctest.Claims{
		Sub:           "idp-subject-1",
		Email:         "staffer@example.com",
		EmailVerified: true,
		Nonce:         nonce,
	})

	callbackLoc, sessionCookie, err := callbackOIDC(mux, state)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if callbackLoc.Path != "/acme" {
		t.Fatalf("redirect = %s, want /acme", callbackLoc)
	}
	if sessionCookie == "" {
		t.Fatalf("expected a session cookie to be set")
	}
	if idp.LastCodeVerifier() == "" {
		t.Fatalf("expected PKCE code_verifier to be sent to the token endpoint")
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
	loc := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "idp-subject-2", Email: "staffer@example.com", EmailVerified: true,
		Nonce: loc.Query().Get("nonce"),
	})

	if _, _, err := callbackOIDC(mux, loc.Query().Get("state")); err != nil {
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
	loc := startOIDC(t, mux, "/acme")
	state := loc.Query().Get("state")
	idp.SetClaims(oidctest.Claims{Sub: "sub-x", Email: "staffer@example.com", EmailVerified: true, Nonce: loc.Query().Get("nonce")})

	if _, _, err := callbackOIDC(mux, state); err != nil {
		t.Fatalf("first callback: %v", err)
	}

	replayLoc, _, err := callbackOIDC(mux, state)
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

	loc, _, err := callbackOIDC(mux, foreignState)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	assertLoginError(t, loc, "sso_expired")
}

func TestOIDCNonceMismatchRejected(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "staffer@example.com", "whatever")
	addMembership(t, pool, userID, org.ID, Staff)

	_, mux := newOIDCTestAuth(pool, idp)
	loc := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "sub-y", Email: "staffer@example.com", EmailVerified: true,
		Nonce: "wrong-nonce-" + loc.Query().Get("nonce"),
	})

	callbackLoc, _, err := callbackOIDC(mux, loc.Query().Get("state"))
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
	loc := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "sub-z", Email: "staffer@example.com", EmailVerified: false,
		Nonce: loc.Query().Get("nonce"),
	})

	callbackLoc, _, err := callbackOIDC(mux, loc.Query().Get("state"))
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	assertLoginError(t, callbackLoc, "email_unverified")
}

func TestOIDCUnknownEmailNotInvited(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)

	_, mux := newOIDCTestAuth(pool, idp)
	loc := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "sub-unknown", Email: "nobody@example.com", EmailVerified: true,
		Nonce: loc.Query().Get("nonce"),
	})

	callbackLoc, _, err := callbackOIDC(mux, loc.Query().Get("state"))
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
	loc := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "sub-tenant", Email: "tenant@example.com", EmailVerified: true,
		Nonce: loc.Query().Get("nonce"),
	})

	callbackLoc, _, err := callbackOIDC(mux, loc.Query().Get("state"))
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
	loc := startOIDC(t, mux, "/acme")
	idp.SetClaims(oidctest.Claims{
		Sub: "a-new-subject-claiming-userb-email", Email: "b@example.com", EmailVerified: true,
		Nonce: loc.Query().Get("nonce"),
	})

	callbackLoc, _, err := callbackOIDC(mux, loc.Query().Get("state"))
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	assertLoginError(t, callbackLoc, "sso_conflict")
}

func TestOIDCStartNextOpenRedirectFallsBackToRoot(t *testing.T) {
	pool := db.TestPool(t)
	idp := oidctest.New(t)

	_, mux := newOIDCTestAuth(pool, idp)
	loc := startOIDC(t, mux, "//evil.com")
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
