package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/oauth2"

	"shigoto/internal/core"
)

// oidcStateTTL is how long an OIDC login's state/nonce/PKCE row (and the
// browser-side state cookie that binds a callback to the browser that
// started it) is valid for (global-constraints.md "Tokens": OIDC state 10
// minutes).
const oidcStateTTL = 10 * time.Minute

// oidcDiscoveryTimeout bounds how long provider discovery (fetching
// /.well-known/openid-configuration) may take, so a slow or unreachable IdP
// fails an OIDC attempt rather than hanging the request indefinitely.
const oidcDiscoveryTimeout = 10 * time.Second

// oidcRequestTimeout bounds the token exchange and ID token verification
// (which fetches JWKS), for the same reason as oidcDiscoveryTimeout.
const oidcRequestTimeout = 10 * time.Second

// oidcStateCookieName is a short-lived, HttpOnly cookie set on the browser
// at /api/v1/auth/oidc/start and checked (then always cleared) at
// /api/v1/auth/oidc/callback. It binds a callback to the browser that
// actually started this particular attempt: the "state" query parameter
// alone only proves the callback carries a value this server once issued,
// not that it's being redeemed by the same browser it was issued to - a
// CSRF login attack forges a callback request (from the attacker's own,
// completed OIDC flow) onto the victim's browser, using the victim's
// session. Requiring the state to also match a cookie only that browser
// holds closes that.
const oidcStateCookieName = "shigoto_oidc_state"

// OIDC error codes. The frontend (web/src/routes/login/+page.svelte) maps
// each to a fixed message; the backend never sends IdP-originated text to
// the browser, only one of these codes as /login?error=<code>.
const (
	oidcErrExpired     = "sso_expired"
	oidcErrUnavailable = "sso_unavailable"
	oidcErrFailed      = "sso_failed"
	oidcErrUnverified  = "email_unverified"
	oidcErrNotInvited  = "not_invited"
	oidcErrNotStaff    = "not_staff"
	oidcErrConflict    = "sso_conflict"
)

// OIDCConfig configures generic OIDC staff sign-in (Authentik in
// production). RedirectURL must be BaseURL + "/api/v1/auth/oidc/callback".
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       []string
	RedirectURL  string
}

// oidcProvider lazily discovers cfg.Issuer's OIDC configuration and caches
// the result only on success, guarded by mu: a discovery failure (the IdP
// is briefly unreachable, say) is retried on the next attempt rather than
// being cached or crashing startup.
type oidcProvider struct {
	cfg OIDCConfig

	mu       sync.Mutex
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauthCfg *oauth2.Config
}

func (p *oidcProvider) ready(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.provider != nil {
		return p.oauthCfg, p.verifier, nil
	}

	dctx, cancel := context.WithTimeout(ctx, oidcDiscoveryTimeout)
	defer cancel()
	provider, err := oidc.NewProvider(dctx, p.cfg.Issuer)
	if err != nil {
		return nil, nil, err
	}

	p.provider = provider
	p.oauthCfg = &oauth2.Config{
		ClientID:     p.cfg.ClientID,
		ClientSecret: p.cfg.ClientSecret,
		RedirectURL:  p.cfg.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       p.cfg.Scopes,
	}
	p.verifier = provider.Verifier(&oidc.Config{ClientID: p.cfg.ClientID})
	return p.oauthCfg, p.verifier, nil
}

// WithOIDC enables generic OIDC staff sign-in with cfg. Without this
// option, OIDCEnabled is false and both OIDC handlers 404.
func WithOIDC(cfg OIDCConfig) Option {
	return func(a *Auth) { a.oidc = &oidcProvider{cfg: cfg} }
}

// OIDCEnabled reports whether OIDC sign-in is configured, for GET
// /api/v1/meta's "sso" field. Safe on a nil *Auth, since some tests build
// httpapi.Deps without one.
func (a *Auth) OIDCEnabled() bool {
	return a != nil && a.oidc != nil
}

// safeOIDCNext validates the next redirect target: an absolute path that
// isn't protocol-relative ("//", which a browser treats as a different
// origin) and carries no backslash or control character. A backslash is
// rejected outright (not just a leading "/\\") because some browsers
// normalize an interior backslash to "/" before resolving the URL, so
// "/\t/evil.com" - "next=/%09/evil.com", a tab - is stripped down to
// "//evil.com" (protocol-relative) by the time it's followed; filtering
// control characters (including the tab itself) closes that and similar
// smuggling via \n, \r or other C0 bytes. Anything that fails falls back to
// "/".
func safeOIDCNext(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "/"
	}
	for _, r := range raw {
		if r == '\\' || r < 0x20 || r == 0x7f {
			return "/"
		}
	}
	return raw
}

// newOIDCStateToken generates a 32-random-byte token, base64url encoded
// (global-constraints.md "Tokens"), and its SHA-256 hash for storage.
func newOIDCStateToken() (token string, hash []byte, err error) {
	return newRandomToken()
}

// newOIDCNonce generates a random nonce to bind the ID token back to this
// authorization attempt. It doesn't need to be secret, only unpredictable.
func newOIDCNonce() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// redirectLoginError redirects the browser to /login?error=code. It's the
// only way an OIDC handler ever reports a problem: nothing from the IdP, or
// anything more specific than the fixed code, is ever sent to the browser.
func redirectLoginError(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/login?error="+code, http.StatusFound)
}

// setOIDCStateCookie sets the state-binding cookie at the start of an
// attempt; its lifetime matches the oidc_states row it binds to.
func setOIDCStateCookie(w http.ResponseWriter, secure bool, state string) {
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookieName,
		Value:    state,
		Path:     "/api/v1/auth/oidc",
		MaxAge:   int(oidcStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearOIDCStateCookie removes the state-binding cookie. The callback
// clears it unconditionally, on every return path, whether the attempt
// succeeded or failed: it's single-use either way.
func clearOIDCStateCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookieName,
		Value:    "",
		Path:     "/api/v1/auth/oidc",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// failOIDCCallback clears the state cookie and redirects to /login?error=code.
// Every error return from OIDCCallbackHandler goes through this (rather than
// the bare redirectLoginError Start uses), since by the time the callback
// runs there's always a cookie from Start that needs clearing either way.
func (a *Auth) failOIDCCallback(w http.ResponseWriter, r *http.Request, code string) {
	clearOIDCStateCookie(w, a.secureCookies)
	redirectLoginError(w, r, code)
}

// OIDCStartHandler handles GET /api/v1/auth/oidc/start?next=/path. It
// generates the state, nonce and PKCE verifier, stores the hashed state,
// and redirects to the IdP.
func (a *Auth) OIDCStartHandler(w http.ResponseWriter, r *http.Request) {
	if !a.OIDCEnabled() {
		http.NotFound(w, r)
		return
	}

	oauthCfg, _, err := a.oidc.ready(r.Context())
	if err != nil {
		// Ruling 56: log only a fixed message and the configured issuer, not
		// the go-oidc error text (which can embed the IdP's own response
		// body).
		slog.Error("OIDC discovery failed", "code", oidcErrUnavailable, "issuer", a.oidc.cfg.Issuer)
		redirectLoginError(w, r, oidcErrUnavailable)
		return
	}

	next := safeOIDCNext(r.URL.Query().Get("next"))

	state, stateHash, err := newOIDCStateToken()
	if err != nil {
		slog.Error("generating OIDC state failed", "error", err)
		redirectLoginError(w, r, oidcErrFailed)
		return
	}
	nonce, err := newOIDCNonce()
	if err != nil {
		slog.Error("generating OIDC nonce failed", "error", err)
		redirectLoginError(w, r, oidcErrFailed)
		return
	}
	verifier := oauth2.GenerateVerifier()

	if _, err := a.pool.Exec(r.Context(),
		`INSERT INTO oidc_states (state_hash, nonce, code_verifier, next, expires_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		stateHash, nonce, verifier, next, time.Now().Add(oidcStateTTL),
	); err != nil {
		slog.Error("storing OIDC state failed", "error", err)
		redirectLoginError(w, r, oidcErrFailed)
		return
	}

	setOIDCStateCookie(w, a.secureCookies, state)
	url := oauthCfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce))
	http.Redirect(w, r, url, http.StatusFound)
}

// oidcState is one consumed oidc_states row.
type oidcState struct {
	nonce    string
	verifier string
	next     string
}

// consumeOIDCState atomically consumes the state identified by the
// plaintext token: a missing, already-used or expired row all fail the same
// way (pgx.ErrNoRows), since the caller treats them identically
// (/login?error=sso_expired).
func consumeOIDCState(ctx context.Context, q core.Querier, token string) (oidcState, error) {
	var s oidcState
	err := q.QueryRow(ctx,
		`UPDATE oidc_states SET used_at = now()
		 WHERE state_hash = $1 AND used_at IS NULL AND expires_at > now()
		 RETURNING nonce, code_verifier, next`,
		hashToken(token),
	).Scan(&s.nonce, &s.verifier, &s.next)
	return s, err
}

// PruneOIDCStates deletes every oidc_states row that's already used or has
// expired (Ruling 49: this task prunes only its own table). It's called by
// the hourly scheduler alongside PruneSessions and PruneInvites.
func PruneOIDCStates(ctx context.Context, q core.Querier, now time.Time) error {
	_, err := q.Exec(ctx, "DELETE FROM oidc_states WHERE used_at IS NOT NULL OR expires_at < $1", now)
	return err
}

// OIDCCallbackHandler handles GET /api/v1/auth/oidc/callback?code&state. On
// success it starts a session exactly as local login does and redirects to
// the next path stored with the state. Every failure redirects to
// /login?error=<code>; nothing from the IdP (including its own "error"
// query parameter) is ever reflected back, and nothing is logged beyond the
// code.
func (a *Auth) OIDCCallbackHandler(w http.ResponseWriter, r *http.Request) {
	if !a.OIDCEnabled() {
		http.NotFound(w, r)
		return
	}

	if r.URL.Query().Get("error") != "" {
		a.failOIDCCallback(w, r, oidcErrFailed)
		return
	}

	ctx := r.Context()
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		a.failOIDCCallback(w, r, oidcErrFailed)
		return
	}

	// CSRF: the state query parameter alone only proves this server issued
	// it at some point, not that this callback is being redeemed by the
	// browser it was issued to. Require the state-binding cookie Start set
	// to match, in constant time, *before* even attempting to consume the
	// state row - a missing or wrong cookie must leave the row alone so a
	// legitimate retry (if the real browser still has it) can still
	// succeed.
	cookie, err := r.Cookie(oidcStateCookieName)
	if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		a.failOIDCCallback(w, r, oidcErrExpired)
		return
	}

	consumed, err := consumeOIDCState(ctx, a.pool, state)
	if errors.Is(err, pgx.ErrNoRows) {
		a.failOIDCCallback(w, r, oidcErrExpired)
		return
	}
	if err != nil {
		slog.Error("consuming OIDC state failed", "error", err)
		a.failOIDCCallback(w, r, oidcErrFailed)
		return
	}

	oauthCfg, verifier, err := a.oidc.ready(ctx)
	if err != nil {
		// Ruling 56: log only a fixed message and the configured issuer,
		// not the go-oidc error text.
		slog.Error("OIDC discovery failed", "code", oidcErrUnavailable, "issuer", a.oidc.cfg.Issuer)
		a.failOIDCCallback(w, r, oidcErrUnavailable)
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, oidcRequestTimeout)
	defer cancel()

	token, err := oauthCfg.Exchange(reqCtx, code, oauth2.VerifierOption(consumed.verifier))
	if err != nil {
		a.failOIDCCallback(w, r, oidcErrFailed)
		return
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		a.failOIDCCallback(w, r, oidcErrFailed)
		return
	}
	idToken, err := verifier.Verify(reqCtx, rawIDToken)
	if err != nil {
		a.failOIDCCallback(w, r, oidcErrFailed)
		return
	}
	if idToken.Nonce != consumed.nonce {
		a.failOIDCCallback(w, r, oidcErrFailed)
		return
	}

	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	// idToken.Claims unmarshals email_verified strictly into a bool: an IdP
	// sending it as the string "true" fails this decode (fails closed as
	// oidcErrFailed) rather than being coerced into a pass.
	if err := idToken.Claims(&claims); err != nil {
		a.failOIDCCallback(w, r, oidcErrFailed)
		return
	}
	if !claims.EmailVerified {
		a.failOIDCCallback(w, r, oidcErrUnverified)
		return
	}

	userID, err := a.resolveOIDCUser(ctx, idToken.Subject, claims.Email)
	switch {
	case errors.Is(err, errOIDCNotInvited):
		a.failOIDCCallback(w, r, oidcErrNotInvited)
		return
	case errors.Is(err, errOIDCNotStaff):
		a.failOIDCCallback(w, r, oidcErrNotStaff)
		return
	case errors.Is(err, errOIDCConflict):
		a.failOIDCCallback(w, r, oidcErrConflict)
		return
	case err != nil:
		slog.Error("resolving OIDC user failed", "error", err)
		a.failOIDCCallback(w, r, oidcErrFailed)
		return
	}

	if err := a.StartSession(ctx, a.pool, w, userID); err != nil {
		slog.Error("starting OIDC session failed", "error", err)
		a.failOIDCCallback(w, r, oidcErrFailed)
		return
	}

	clearOIDCStateCookie(w, a.secureCookies)
	http.Redirect(w, r, consumed.next, http.StatusFound)
}

// Sentinel errors for resolveOIDCUser. None of their messages is sent to
// the browser; OIDCCallbackHandler maps each to a fixed error code.
var (
	errOIDCNotInvited = errors.New("no shigoto account for this email")
	errOIDCNotStaff   = errors.New("user has no staff membership")
	errOIDCConflict   = errors.New("subject already linked to a different user")
)

// staffRoles are the roles OIDC sign-in is available to (global-constraints.md
// "OIDC sign-in is for staff roles only").
var staffRoles = []string{string(Owner), string(Staff), string(Bookkeeper)}

// resolveOIDCUser implements global-constraints.md's "OIDC never
// auto-provisions" and Ruling 54, in one transaction:
//
//  1. A user is found by oidc_subject. Found: already linked, untouched,
//     skip straight to the staff check.
//  2. Failing that, by email. Not found at all: errOIDCNotInvited (an
//     unknown email never auto-provisions an account). Found but already
//     linked to a *different* subject: errOIDCConflict.
//  3. The resolved user must hold a staff-role (owner/staff/bookkeeper)
//     membership in at least one org, checked before any mutation so a
//     rejected tenant/client-only user is never linked or touched:
//     errOIDCNotStaff otherwise.
//  4. Only now, if step 2 found the user by email (oidc_subject was NULL),
//     is oidc_subject set - and, per Ruling 54, the user's password_hash is
//     cleared in the same transaction (SSO just proved email ownership, so
//     a password set by someone else, e.g. an owner accepting an invite for
//     that email, must stop working) - and the link is audited against one
//     of the user's staff orgs.
func (a *Auth) resolveOIDCUser(ctx context.Context, sub, email string) (int64, error) {
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID int64
	var needsLink bool

	err = tx.QueryRow(ctx, "SELECT id FROM users WHERE oidc_subject = $1", sub).Scan(&userID)
	switch {
	case err == nil:
		// Already linked by subject (Ruling 54: untouched).
	case errors.Is(err, pgx.ErrNoRows):
		var existingSubject *string
		err = tx.QueryRow(ctx, "SELECT id, oidc_subject FROM users WHERE email = $1", email).
			Scan(&userID, &existingSubject)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, errOIDCNotInvited
		}
		if err != nil {
			return 0, err
		}
		if existingSubject != nil {
			// Already linked to some other subject (it can't be sub: that
			// would have matched the lookup above).
			return 0, errOIDCConflict
		}
		needsLink = true
	default:
		return 0, err
	}

	var staffOrgID int64
	err = tx.QueryRow(ctx,
		"SELECT org_id FROM memberships WHERE user_id = $1 AND role = ANY($2) ORDER BY org_id LIMIT 1",
		userID, staffRoles,
	).Scan(&staffOrgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errOIDCNotStaff
	}
	if err != nil {
		return 0, err
	}

	if needsLink {
		tag, err := tx.Exec(ctx,
			"UPDATE users SET oidc_subject = $1, password_hash = NULL WHERE id = $2 AND oidc_subject IS NULL",
			sub, userID,
		)
		if err != nil {
			// A unique-violation here means a concurrent request linked this
			// same subject to a different user between our SELECT above and
			// this UPDATE: treat it the same as the conflict that SELECT
			// would have found had it run a moment later. No audit, no
			// session - nothing to undo.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return 0, errOIDCConflict
			}
			return 0, err
		}
		// 0 rows means another concurrent request already set this user's
		// oidc_subject (to some other value) between our SELECT and this
		// UPDATE's WHERE oidc_subject IS NULL - the same race, caught by
		// the guard instead of the unique index. Either way: no audit, no
		// session.
		if tag.RowsAffected() != 1 {
			return 0, errOIDCConflict
		}
		if err := core.Audit(ctx, tx, staffOrgID, &userID, "user", userID, "oidc_link",
			map[string]any{"email": email, "password_cleared": true},
		); err != nil {
			return 0, err
		}

		// Ruling 64 (security): linking an existing account to an OIDC
		// subject deletes every session that account already held, in this
		// same tx - the caller starts a fresh one for this callback right
		// after. Without this, a session obtained before the link (e.g. a
		// portal magic link, or a password session later invalidated by the
		// password_hash clear above) would keep working as a now-privileged
		// staff session.
		if _, err := tx.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1", userID); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return userID, nil
}
