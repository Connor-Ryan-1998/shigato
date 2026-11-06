package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sessionCookieName is the cookie holding the session token.
const sessionCookieName = "shigoto_session"

// Login rate limits (global-constraints.md "Rate limits"): at most 5 failed
// logins per email, and 20 per client IP, in a trailing 15 minutes.
const (
	loginEmailLimit = 5
	loginIPLimit    = 20
	loginWindow     = 15 * time.Minute
)

// writeJSONError writes a JSON error body {"error":"<message>"}. It
// duplicates module.WriteError rather than importing it: package module
// imports package auth (for module.Deps.Auth), so auth importing module back
// would be a cycle.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// Auth holds the dependencies for the login/logout handlers and the
// RequireOrg middleware.
type Auth struct {
	pool          *pgxpool.Pool
	secureCookies bool
	trustedProxy  *net.IPNet

	limiter *Limiter

	// oidc is non-nil when OIDC sign-in is configured (WithOIDC). nil
	// means OIDCEnabled is false and both OIDC handlers 404.
	oidc *oidcProvider
}

// Option configures optional Auth behaviour beyond New's required
// parameters, without changing the signature existing callers already use.
type Option func(*Auth)

// WithClock overrides the clock Auth's login rate limiter uses. Production
// code never needs this (New defaults to time.Now); tests inject a fake
// clock to exercise the sliding window without a real sleep.
func WithClock(now func() time.Time) Option {
	return func(a *Auth) { a.limiter = NewLimiter(now) }
}

// WithTrustedProxy sets the CIDR whose X-Forwarded-For header is trusted
// when resolving a request's client IP (config.Config.TrustedProxy, from
// SHIGOTO_TRUSTED_PROXY). Unset, the client IP is always r.RemoteAddr.
func WithTrustedProxy(trusted *net.IPNet) Option {
	return func(a *Auth) { a.trustedProxy = trusted }
}

// New creates an Auth. secureCookies should be true when the app is served
// over HTTPS (BaseURL starts with "https://"), so the session cookie is
// marked Secure.
func New(pool *pgxpool.Pool, secureCookies bool, opts ...Option) *Auth {
	a := &Auth{pool: pool, secureCookies: secureCookies, limiter: NewLimiter(time.Now)}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Principal identifies the authenticated user and their role within the
// requested org.
type Principal struct {
	UserID    int64
	OrgID     int64
	Role      Role
	ContactID *int64
}

type principalKey struct{}

// From reads the Principal that RequireOrg stored in ctx.
func From(ctx context.Context) Principal {
	p, _ := ctx.Value(principalKey{}).(Principal)
	return p
}

// LoginHandler handles POST /api/v1/auth/login with body {email,password}.
// On success it sets the session cookie. It is exported as an
// http.HandlerFunc for Task 5's router to mount.
func (a *Auth) LoginHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Checked (and reserved - see Limiter.Allow) before touching the
	// password, so an attacker who's already over a limit can't use
	// further attempts to keep timing-probing it, and so concurrent
	// attempts can't all slip through before any of them is recorded.
	emailKey := "email:" + strings.ToLower(body.Email)
	ipKey := ipRateLimitKey(ClientIP(r, a.trustedProxy))
	if ok, retryAfter := a.limiter.Allow(emailKey, loginEmailLimit, loginWindow); !ok {
		writeTooManyRequests(w, retryAfter)
		return
	}
	if ok, retryAfter := a.limiter.Allow(ipKey, loginIPLimit, loginWindow); !ok {
		writeTooManyRequests(w, retryAfter)
		return
	}

	userID, err := verifyCredentials(r.Context(), a.pool, body.Email, body.Password)
	if errors.Is(err, ErrInvalidCredentials) {
		// Nothing further to do: Allow's reservation above is already the
		// record of this failure. Success never resets either count -
		// Succeed below only releases this one request's own reservation -
		// so an attacker who guesses right on attempt 4 still has only 1
		// try left in this window.
		writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		slog.Error("login failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.limiter.Succeed(emailKey)
	a.limiter.Succeed(ipKey)

	if err := a.StartSession(r.Context(), a.pool, w, userID); err != nil {
		slog.Error("starting session failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeTooManyRequests writes the shared 429 body and a whole-seconds,
// rounded-up Retry-After header.
func writeTooManyRequests(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 0 {
		seconds = 0
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeJSONError(w, http.StatusTooManyRequests, "too many attempts, try again later")
}

// LogoutHandler handles POST /api/v1/auth/logout. It deletes the session
// (if any) and clears the cookie.
func (a *Auth) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	var logoutErr error
	if c, err := r.Cookie(sessionCookieName); err == nil {
		logoutErr = Logout(r.Context(), a.pool, c.Value)
	}

	// Clear the cookie regardless of whether the server-side delete
	// succeeded: the browser should stop sending a token either way.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})

	if logoutErr != nil {
		slog.Error("logout failed", "error", logoutErr)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RequireOrg returns middleware that resolves the shigoto_session cookie to
// a user, resolves the {org} path value (slug) to a membership, and
// requires permission p on that membership's role.
//
// No session: 401. Not a member of the org: 404. Member but missing the
// permission: 403.
func (a *Auth) RequireOrg(p Perm) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(sessionCookieName)
			if err != nil {
				writeJSONError(w, http.StatusUnauthorized, "unauthorized")
				return
			}

			ctx := r.Context()
			userID, err := userIDForToken(ctx, a.pool, c.Value)
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			if err != nil {
				slog.Error("resolving session failed", "error", err)
				writeJSONError(w, http.StatusInternalServerError, "internal error")
				return
			}

			m, err := membershipFor(ctx, a.pool, userID, r.PathValue("org"))
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "not found")
				return
			}
			if err != nil {
				slog.Error("resolving membership failed", "error", err)
				writeJSONError(w, http.StatusInternalServerError, "internal error")
				return
			}

			if !Can(m.Role, p) {
				writeJSONError(w, http.StatusForbidden, "forbidden")
				return
			}

			principal := Principal{
				UserID:    userID,
				OrgID:     m.OrgID,
				Role:      m.Role,
				ContactID: m.ContactID,
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, principalKey{}, principal)))
		})
	}
}

// RequirePortal returns middleware for the contact-scoped portal API: a
// session is still required (401, as RequireOrg), but unlike RequireOrg it
// requires the membership's role to be tenant or client rather than a
// permission - any other case (no membership, or a staff role) is 404, not
// 403, so a portal route never reveals that a staff-only org or membership
// exists (global-constraints.md "Portal isolation"). The leases route's
// tenant-only restriction isn't checked here: a client gets a 404 there
// too, which that handler enforces itself from the stored Principal.
func (a *Auth) RequirePortal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		ctx := r.Context()
		userID, err := userIDForToken(ctx, a.pool, c.Value)
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err != nil {
			slog.Error("resolving session failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		m, err := membershipFor(ctx, a.pool, userID, r.PathValue("org"))
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "not found")
			return
		}
		if err != nil {
			slog.Error("resolving membership failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if m.Role != Tenant && m.Role != Client {
			writeJSONError(w, http.StatusNotFound, "not found")
			return
		}

		principal := Principal{
			UserID:    userID,
			OrgID:     m.OrgID,
			Role:      m.Role,
			ContactID: m.ContactID,
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, principalKey{}, principal)))
	})
}

type userIDKey struct{}

// UserIDFrom reads the user ID that RequireUser stored in ctx.
func UserIDFrom(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(userIDKey{}).(int64)
	return id, ok
}

// RequireUser returns middleware that resolves the shigoto_session cookie to
// a user and stores their ID in ctx, without requiring membership in any
// org. It's for routes like /api/v1/me that span every org a user belongs
// to rather than being scoped to one. No session: 401.
func (a *Auth) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		ctx := r.Context()
		userID, err := userIDForToken(ctx, a.pool, c.Value)
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err != nil {
			slog.Error("resolving session failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, userIDKey{}, userID)))
	})
}
