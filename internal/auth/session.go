package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	var userID int64
	var hash *string
	err := q.QueryRow(ctx,
		"SELECT id, password_hash FROM users WHERE email = $1",
		email,
	).Scan(&userID, &hash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}

	known := err == nil && hash != nil
	check := dummyHash
	if known {
		check = *hash
	}
	valid := checkPassword(check, password)

	if !known || !valid {
		return "", ErrInvalidCredentials
	}

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

// Logout deletes the session for token, if one exists.
func Logout(ctx context.Context, q core.Querier, token string) error {
	_, err := q.Exec(ctx, "DELETE FROM sessions WHERE token_hash = $1", hashToken(token))
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
