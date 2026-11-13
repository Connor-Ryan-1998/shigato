//go:build integration

package auth_test

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/auth/oidctest"
	"shigoto/internal/testkit"
)

// newOIDCTestAuth builds an *Auth wired to idp, with BaseURL
// "https://shigoto.example" (so RedirectURL is deterministic) and returns a
// router exposing just the two OIDC routes, the way httpapi/server.go
// mounts them.
func newOIDCTestAuth(pool *pgxpool.Pool, idp *oidctest.Server) (*auth.Auth, http.Handler) {
	a := auth.New(pool, true, auth.WithOIDC(auth.OIDCConfig{
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
		if c.Name == auth.OIDCStateCookieName {
			stateCookie = c.Value
		}
	}
	if stateCookie == "" {
		t.Fatalf("expected start to set the %s cookie", auth.OIDCStateCookieName)
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
		req.AddCookie(&http.Cookie{Name: auth.OIDCStateCookieName, Value: cookieValue})
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
		case auth.SessionCookieName:
			sessionCookie = c.Value
		case auth.OIDCStateCookieName:
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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	idp := oidctest.New(t)

	org := e.Org("acme")
	staffer := e.User("staffer@example.com", testkit.WithPassword("whatever-password"))
	e.Member(staffer, org, auth.Staff)

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
	if err := pool.QueryRow(ctx, "SELECT oidc_subject, password_hash FROM users WHERE id = $1", staffer.ID).
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
	if _, err := auth.Login(ctx, pool, "staffer@example.com", "whatever-password"); err != auth.ErrInvalidCredentials {
		t.Fatalf("Login after OIDC link: err = %v, want ErrInvalidCredentials", err)
	}

	var found bool
	if err := pool.QueryRow(ctx,
		"SELECT true FROM audit_log WHERE org_id = $1 AND entity = 'user' AND entity_id = $2",
		org.ID, staffer.ID,
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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	idp := oidctest.New(t)

	org := e.Org("acme")
	staffer := e.User("staffer@example.com", testkit.WithPassword("whatever-password"))
	e.Member(staffer, org, auth.Staff)

	oldToken, err := auth.CreateSession(ctx, pool, staffer.ID)
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	if _, err := auth.UserIDForToken(ctx, pool, oldToken); err != nil {
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

	if _, err := auth.UserIDForToken(ctx, pool, oldToken); err == nil {
		t.Fatalf("expected the pre-link session to be revoked by the OIDC link")
	}
	if _, err := auth.UserIDForToken(ctx, pool, sessionCookie); err != nil {
		t.Fatalf("the session minted by the callback itself should be valid: %v", err)
	}
}

func TestOIDCAlreadyLinkedUserUntouched(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	idp := oidctest.New(t)

	org := e.Org("acme")
	staffer := e.User("staffer@example.com", testkit.WithPassword("a-password"))
	e.Member(staffer, org, auth.Staff)
	if _, err := pool.Exec(ctx, "UPDATE users SET oidc_subject = $1 WHERE id = $2", "idp-subject-2", staffer.ID); err != nil {
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
	if _, err := auth.Login(ctx, pool, "staffer@example.com", "a-password"); err != nil {
		t.Fatalf("Login for already-linked user: err = %v, want nil", err)
	}
}

// seedStaffer gives acme a password-holding staff member,
// staffer@example.com, for a callback to sign in as.
func seedStaffer(e *testkit.Env) {
	org := e.Org("acme")
	staffer := e.User("staffer@example.com", testkit.WithPassword("whatever"))
	e.Member(staffer, org, auth.Staff)
}

// stateUnused fails t unless the oidc_states row for state is still
// unconsumed (used_at NULL); why says why it must be.
func stateUnused(t *testing.T, e *testkit.Env, state, why string) {
	t.Helper()
	var usedAt *time.Time
	if err := e.Pool.QueryRow(e.Ctx, "SELECT used_at FROM oidc_states WHERE state_hash = $1", stateHash(state)).Scan(&usedAt); err != nil {
		t.Fatalf("querying oidc_states: %v", err)
	}
	if usedAt != nil {
		t.Fatalf("used_at = %v, want nil (%s)", *usedAt, why)
	}
}

// TestOIDCCallbackRejections drives one start/callback round trip per case
// and checks the callback redirects to /login with the case's error code.
// Each case seeds its own database and fake IdP.
func TestOIDCCallbackRejections(t *testing.T) {
	cases := []struct {
		name string
		// seed creates the case's fixtures; nil seeds nothing.
		seed func(e *testkit.Env)
		// claims builds the ID token claims from Start's nonce; nil leaves
		// the IdP's claims unset.
		claims func(nonce string) oidctest.Claims
		// callback drives the callback and returns its redirect; nil sends
		// Start's state with Start's cookie.
		callback func(t *testing.T, mux http.Handler, state, stateCookie string) *url.URL
		want     string
		// check runs extra assertions after the redirect is checked.
		check func(t *testing.T, e *testkit.Env, state string)
	}{
		{
			name: "replayed state",
			seed: seedStaffer,
			claims: func(nonce string) oidctest.Claims {
				return oidctest.Claims{Sub: "sub-x", Email: "staffer@example.com", EmailVerified: true, Nonce: nonce}
			},
			callback: func(t *testing.T, mux http.Handler, state, stateCookie string) *url.URL {
				if _, _, _, err := callbackOIDC(mux, state, stateCookie); err != nil {
					t.Fatalf("first callback: %v", err)
				}
				// The real browser still has its state cookie (Start only set
				// it once); a second callback with the same state and cookie
				// is a replay of an already-consumed row, not a CSRF mismatch.
				replayLoc, _, _, err := callbackOIDC(mux, state, stateCookie)
				if err != nil {
					t.Fatalf("replayed callback: %v", err)
				}
				return replayLoc
			},
			want: "sso_expired",
		},
		{
			// Start establishes the route/IdP but its state is never used.
			name: "foreign state",
			callback: func(t *testing.T, mux http.Handler, _, _ string) *url.URL {
				buf := make([]byte, 32)
				foreignState := base64.RawURLEncoding.EncodeToString(buf)
				// The cookie must match the state query parameter for the
				// callback to get past the CSRF check at all and exercise the
				// "unknown state" rejection specifically, rather than the
				// cookie-mismatch one.
				loc, _, _, err := callbackOIDC(mux, foreignState, foreignState)
				if err != nil {
					t.Fatalf("callback: %v", err)
				}
				return loc
			},
			want: "sso_expired",
		},
		{
			name: "missing cookie",
			seed: seedStaffer,
			claims: func(nonce string) oidctest.Claims {
				return oidctest.Claims{Sub: "sub-nocookie", Email: "staffer@example.com", EmailVerified: true, Nonce: nonce}
			},
			callback: func(t *testing.T, mux http.Handler, state, _ string) *url.URL {
				loc, _, _, err := callbackOIDC(mux, state, "")
				if err != nil {
					t.Fatalf("callback: %v", err)
				}
				return loc
			},
			want: "sso_expired",
			// The state row must be untouched: a missing cookie fails the
			// CSRF check before the row is ever consumed, so the real browser
			// (which does still have its cookie) can retry.
			check: func(t *testing.T, e *testkit.Env, state string) {
				stateUnused(t, e, state, "state must not be consumed without the cookie")
			},
		},
		{
			name: "wrong cookie",
			seed: seedStaffer,
			claims: func(nonce string) oidctest.Claims {
				return oidctest.Claims{Sub: "sub-wrongcookie", Email: "staffer@example.com", EmailVerified: true, Nonce: nonce}
			},
			callback: func(t *testing.T, mux http.Handler, state, _ string) *url.URL {
				loc, _, _, err := callbackOIDC(mux, state, "not-the-right-cookie-value")
				if err != nil {
					t.Fatalf("callback: %v", err)
				}
				return loc
			},
			want: "sso_expired",
			check: func(t *testing.T, e *testkit.Env, state string) {
				stateUnused(t, e, state, "state must not be consumed with a mismatched cookie")
			},
		},
		{
			name: "nonce mismatch",
			seed: seedStaffer,
			claims: func(nonce string) oidctest.Claims {
				return oidctest.Claims{
					Sub: "sub-y", Email: "staffer@example.com", EmailVerified: true,
					Nonce: "wrong-nonce-" + nonce,
				}
			},
			want: "sso_failed",
		},
		{
			name: "email unverified",
			seed: seedStaffer,
			claims: func(nonce string) oidctest.Claims {
				return oidctest.Claims{Sub: "sub-z", Email: "staffer@example.com", EmailVerified: false, Nonce: nonce}
			},
			want: "email_unverified",
		},
		{
			name: "email_verified as string",
			seed: seedStaffer,
			claims: func(nonce string) oidctest.Claims {
				return oidctest.Claims{
					Sub: "sub-string-verified", Email: "staffer@example.com",
					Nonce: nonce,
					// A non-boolean email_verified must fail closed, not be
					// coerced into true.
					EmailVerifiedRaw: "true",
				}
			},
			want: "sso_failed",
		},
		{
			name: "wrong issuer",
			seed: seedStaffer,
			claims: func(nonce string) oidctest.Claims {
				return oidctest.Claims{
					Sub: "sub-wrong-iss", Email: "staffer@example.com", EmailVerified: true,
					Nonce: nonce,
					Iss:   "https://not-the-configured-issuer.example",
				}
			},
			want: "sso_failed",
		},
		{
			name: "wrong audience",
			seed: seedStaffer,
			claims: func(nonce string) oidctest.Claims {
				return oidctest.Claims{
					Sub: "sub-wrong-aud", Email: "staffer@example.com", EmailVerified: true,
					Nonce: nonce,
					Aud:   "someone-elses-client-id",
				}
			},
			want: "sso_failed",
		},
		{
			name: "expired ID token",
			seed: seedStaffer,
			claims: func(nonce string) oidctest.Claims {
				return oidctest.Claims{
					Sub: "sub-expired", Email: "staffer@example.com", EmailVerified: true,
					Nonce:     nonce,
					ExpiresIn: -time.Minute,
				}
			},
			want: "sso_failed",
		},
		{
			name: "unknown email",
			claims: func(nonce string) oidctest.Claims {
				return oidctest.Claims{Sub: "sub-unknown", Email: "nobody@example.com", EmailVerified: true, Nonce: nonce}
			},
			want: "not_invited",
		},
		{
			// An outstanding invite, but no account yet: OIDC never
			// auto-accepts an invite on the invitee's behalf, however it got
			// there. They must accept the invite (creating a password
			// account) first, then SSO links to it on a later sign-in.
			name: "outstanding invite only",
			seed: func(e *testkit.Env) {
				org := e.Org("acme")
				owner := e.User("owner@example.com", testkit.WithPassword("owner-password"))
				if _, _, err := auth.CreateInvite(e.Ctx, e.Pool, org.ID, "invitee@example.com", "staff", owner.ID); err != nil {
					e.T.Fatalf("CreateInvite: %v", err)
				}
			},
			claims: func(nonce string) oidctest.Claims {
				return oidctest.Claims{Sub: "sub-invitee", Email: "invitee@example.com", EmailVerified: true, Nonce: nonce}
			},
			want: "not_invited",
		},
		{
			name: "tenant-only user",
			seed: func(e *testkit.Env) {
				org := e.Org("acme")
				contact := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant Contact"))
				e.PortalUser("tenant@example.com", org, auth.Tenant, contact.ID)
			},
			claims: func(nonce string) oidctest.Claims {
				return oidctest.Claims{Sub: "sub-tenant", Email: "tenant@example.com", EmailVerified: true, Nonce: nonce}
			},
			want: "not_staff",
			check: func(t *testing.T, e *testkit.Env, _ string) {
				var subject *string
				if err := e.Pool.QueryRow(e.Ctx, "SELECT oidc_subject FROM users WHERE email = $1", "tenant@example.com").Scan(&subject); err != nil {
					t.Fatalf("querying user: %v", err)
				}
				if subject != nil {
					t.Fatalf("oidc_subject = %v, want nil (not_staff user must not be linked)", *subject)
				}
			},
		},
		{
			// userB is already linked to a different subject. A token
			// claiming userB's email with yet another subject must be
			// rejected rather than silently re-linking the account to a
			// second identity.
			name: "subject conflict",
			seed: func(e *testkit.Env) {
				org := e.Org("acme")
				for _, link := range []struct{ email, subject string }{
					{"a@example.com", "already-taken"},
					{"b@example.com", "userb-existing-subject"},
				} {
					u := e.User(link.email, testkit.WithPassword("whatever"))
					e.Member(u, org, auth.Staff)
					if _, err := e.Pool.Exec(e.Ctx, "UPDATE users SET oidc_subject = $1 WHERE id = $2", link.subject, u.ID); err != nil {
						e.T.Fatalf("linking %s: %v", link.email, err)
					}
				}
			},
			claims: func(nonce string) oidctest.Claims {
				return oidctest.Claims{Sub: "a-new-subject-claiming-userb-email", Email: "b@example.com", EmailVerified: true, Nonce: nonce}
			},
			want: "sso_conflict",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := testkit.New(t)
			idp := oidctest.New(t)
			if tc.seed != nil {
				tc.seed(e)
			}

			_, mux := newOIDCTestAuth(e.Pool, idp)
			loc, stateCookie := startOIDC(t, mux, "/acme")
			state := loc.Query().Get("state")
			if tc.claims != nil {
				idp.SetClaims(tc.claims(loc.Query().Get("nonce")))
			}

			var callbackLoc *url.URL
			if tc.callback != nil {
				callbackLoc = tc.callback(t, mux, state, stateCookie)
			} else {
				var err error
				callbackLoc, _, _, err = callbackOIDC(mux, state, stateCookie)
				if err != nil {
					t.Fatalf("callback: %v", err)
				}
			}
			assertLoginError(t, callbackLoc, tc.want)
			if tc.check != nil {
				tc.check(t, e, state)
			}
		})
	}
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
			e := testkit.New(t)
			idp := oidctest.New(t)

			_, mux := newOIDCTestAuth(e.Pool, idp)
			loc, _ := startOIDC(t, mux, tc.next)
			state := loc.Query().Get("state")

			var next string
			if err := e.Pool.QueryRow(e.Ctx,
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
// for storage, so the tests above can look its row up directly.
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

func TestMetaReportsSSO(t *testing.T) {
	e := testkit.New(t)
	idp := oidctest.New(t)
	aWithSSO, _ := newOIDCTestAuth(e.Pool, idp)
	if !aWithSSO.OIDCEnabled() {
		t.Fatalf("OIDCEnabled() = false, want true when WithOIDC is set")
	}

	aWithout := auth.New(e.Pool, true)
	if aWithout.OIDCEnabled() {
		t.Fatalf("OIDCEnabled() = true, want false when OIDC isn't configured")
	}
}

// PruneOIDCStates is exercised directly against the DB; it doesn't need the
// fake IdP.
func TestPruneOIDCStates(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx

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

	if err := auth.PruneOIDCStates(ctx, pool, now); err != nil {
		t.Fatalf("PruneOIDCStates: %v", err)
	}

	if count := e.Count("oidc_states", ""); count != 1 {
		t.Fatalf("count = %d, want 1 (only the live state)", count)
	}
}
