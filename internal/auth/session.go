package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/core"
)

// sessionTTL is how long a session lasts after login.
const sessionTTL = 30 * 24 * time.Hour

// ErrInvalidCredentials is returned by Login for both an unknown email and
// a wrong password, so callers can't distinguish the two.
var ErrInvalidCredentials = errors.New("invalid credentials")

// Login verifies email and password against stored staff credentials and,
// if they match, creates a new 30-day session. It returns
// ErrInvalidCredentials for both an unknown email and a wrong password, and
// checks a dummy hash in the unknown-email case so the two take about the
// same time.
func Login(ctx context.Context, q core.Querier, email, password string) (string, error) {
	userID, err := verifyCredentials(ctx, q, email, password)
	if err != nil {
		return "", err
	}
	return createSession(ctx, q, userID)
}

// verifyCredentials checks email and password against stored staff
// credentials and returns the matching user's ID. It returns
// ErrInvalidCredentials for both an unknown email and a wrong password, and
// checks a dummy hash in the unknown-email case so the two take about the
// same time. It creates no session; callers that need one call
// createSession or StartSession.
func verifyCredentials(ctx context.Context, q core.Querier, email, password string) (int64, error) {
	var userID int64
	var hash *string
	err := q.QueryRow(ctx,
		"SELECT id, password_hash FROM users WHERE email = $1",
		email,
	).Scan(&userID, &hash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}

	known := err == nil && hash != nil
	check := dummyHash
	if known {
		check = *hash
	}
	valid := checkPassword(check, password)

	if !known || !valid {
		return 0, ErrInvalidCredentials
	}
	return userID, nil
}

// createSession inserts a new 30-day session row for userID and returns its
// token.
func createSession(ctx context.Context, q core.Querier, userID int64) (string, error) {
	token, tokenHash, err := newToken()
	if err != nil {
		return "", err
	}
	_, err = q.Exec(ctx,
		"INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)",
		tokenHash, userID, time.Now().Add(sessionTTL),
	)
	if err != nil {
		return "", err
	}
	return token, nil
}

// StartSession creates a new session for userID and sets the session cookie
// on w with exactly the flags LoginHandler has always used (including
// a.secureCookies). It's exported so other flows that authenticate a user
// outside password Login - invite acceptance, OIDC callbacks, magic-link
// exchange - can start a session the same way, without duplicating the
// cookie plumbing. q is separate from a's own pool so a caller already
// inside a transaction can pass that instead.
func (a *Auth) StartSession(ctx context.Context, q core.Querier, w http.ResponseWriter, userID int64) error {
	token, err := createSession(ctx, q, userID)
	if err != nil {
		return err
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
	return nil
}

// Logout deletes the session for token, if one exists.
func Logout(ctx context.Context, q core.Querier, token string) error {
	_, err := q.Exec(ctx, "DELETE FROM sessions WHERE token_hash = $1", hashToken(token))
	return err
}

// PruneSessions deletes every session whose expires_at is before now. It's
// called by the hourly scheduler so expired sessions don't accumulate.
func PruneSessions(ctx context.Context, q core.Querier, now time.Time) error {
	_, err := q.Exec(ctx, "DELETE FROM sessions WHERE expires_at < $1", now)
	return err
}

// newToken generates a random 32-byte session token and returns it along
// with the hash stored in the database.
func newToken() (token string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, err
	}
	token = hex.EncodeToString(buf)
	return token, hashToken(token), nil
}

// newRandomToken generates a 32-random-byte token, base64url encoded (per
// global-constraints.md "Tokens": every invite, magic-link and OIDC-state
// token), and returns it along with the SHA-256 hash that's actually stored.
// Shared by newInviteToken, newMagicToken and newOIDCStateToken - the three
// are kept as distinct names at their call sites for readability, but their
// bodies were identical.
func newRandomToken() (token string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, hashToken(token), nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// membership is a user's role within one org, as resolved from the session
// and path by RequireOrg.
type membership struct {
	OrgID     int64
	Role      Role
	ContactID *int64
}

// userIDForToken resolves a session token to a user ID. It returns
// pgx.ErrNoRows if the token is unknown or its session has expired.
func userIDForToken(ctx context.Context, q core.Querier, token string) (int64, error) {
	var userID int64
	err := q.QueryRow(ctx,
		"SELECT user_id FROM sessions WHERE token_hash = $1 AND expires_at > now()",
		hashToken(token),
	).Scan(&userID)
	return userID, err
}

// membershipFor resolves a user's membership in the org with the given
// slug. It returns pgx.ErrNoRows if the org doesn't exist or the user isn't
// a member.
func membershipFor(ctx context.Context, q core.Querier, userID int64, slug string) (membership, error) {
	var m membership
	var role string
	err := q.QueryRow(ctx,
		`SELECT m.org_id, m.role, m.contact_id
		 FROM memberships m
		 JOIN orgs o ON o.id = m.org_id
		 WHERE m.user_id = $1 AND o.slug = $2`,
		userID, slug,
	).Scan(&m.OrgID, &role, &m.ContactID)
	m.Role = Role(role)
	return m, err
}
