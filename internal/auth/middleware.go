package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sessionCookieName is the cookie holding the session token.
const sessionCookieName = "shigoto_session"

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
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	token, err := Login(r.Context(), a.pool, body.Email, body.Password)
	if err != nil {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
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
	if c, err := r.Cookie(sessionCookieName); err == nil {
		_ = Logout(r.Context(), a.pool, c.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
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
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			ctx := r.Context()
			userID, err := userIDForToken(ctx, a.pool, c.Value)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			m, err := membershipFor(ctx, a.pool, userID, r.PathValue("org"))
			if errors.Is(err, pgx.ErrNoRows) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			if !Can(m.Role, p) {
				http.Error(w, "forbidden", http.StatusForbidden)
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
