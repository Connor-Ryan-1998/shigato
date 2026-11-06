package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sessionCookieName is the cookie holding the session token.
const sessionCookieName = "shigoto_session"

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
}

// New creates an Auth. secureCookies should be true when the app is served
// over HTTPS (BaseURL starts with "https://"), so the session cookie is
// marked Secure.
func New(pool *pgxpool.Pool, secureCookies bool) *Auth {
	return &Auth{pool: pool, secureCookies: secureCookies}
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

	token, err := Login(r.Context(), a.pool, body.Email, body.Password)
	if errors.Is(err, ErrInvalidCredentials) {
		writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		slog.Error("login failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(sessionTTL),
		HttpOnly: true,
		Secure:   a.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
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
