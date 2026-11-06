package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/core"
)

// inviteTTL is how long a staff invite is valid for (global-constraints.md
// "Tokens": staff invite 7 days).
const inviteTTL = 7 * 24 * time.Hour

// minInvitePasswordLength is the minimum password length accepted when
// accepting an invite creates a new account (global-constraints.md
// resolution: 12 chars, 422 otherwise).
const minInvitePasswordLength = 12

// Invite sentinel errors. Each maps to a specific HTTP status in
// httpapi/members.go; none of their messages is safe to send to a client
// as-is except where noted.
var (
	// ErrInviteInvalid means the token is unknown, already used or expired
	// - the atomic consuming UPDATE found no matching row. The caller maps
	// this to 410 Gone (resolution: 410 for used or expired, consistently).
	ErrInviteInvalid = errors.New("invite is no longer valid")
	// ErrEmailMismatch means a logged-in user tried to accept an invite
	// addressed to a different email. 409.
	ErrEmailMismatch = errors.New("invite email does not match the signed-in account")
	// ErrAccountExists means an unauthenticated accept found an existing
	// account for the invite's email. 409 "sign in first, then open the
	// link again".
	ErrAccountExists = errors.New("sign in first, then open the link again")
	// ErrPasswordTooShort means an unauthenticated accept supplied a
	// password shorter than minInvitePasswordLength. 422.
	ErrPasswordTooShort = errors.New("password must be at least 12 characters")
)

// Invite is one outstanding invite, as listed to an org admin.
type Invite struct {
	ID        int64
	Email     string
	Role      string
	ExpiresAt time.Time
}

// InviteDetails is what the public GET /api/v1/invites/{token} route shows
// before the invitee decides to accept.
type InviteDetails struct {
	OrgName string
	Email   string
	Role    string
	// Expired is true once the invite can no longer be accepted - already
	// used, or past its expiry - so the invite page can say so without
	// needing a separate error response.
	Expired bool
}

// newInviteToken generates a 32-random-byte token, base64url encoded (per
// global-constraints.md), and returns it along with the SHA-256 hash that's
// actually stored.
func newInviteToken() (token string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, hashToken(token), nil
}

// CreateInvite creates an invite for email to join orgID with role,
// replacing any outstanding (unused) invite already sitting for that email
// (the partial unique index on (org_id, email) WHERE used_at IS NULL is the
// conflict target). It returns the invite's id and the plaintext token -
// callers build the link and send it; only the token's hash is stored.
func CreateInvite(ctx context.Context, q core.Querier, orgID int64, email, role string, invitedBy int64) (id int64, token string, err error) {
	token, hash, err := newInviteToken()
	if err != nil {
		return 0, "", err
	}

	err = q.QueryRow(ctx, `
		INSERT INTO invites (org_id, email, role, token_hash, invited_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (org_id, email) WHERE used_at IS NULL
		DO UPDATE SET
			role        = EXCLUDED.role,
			token_hash  = EXCLUDED.token_hash,
			invited_by  = EXCLUDED.invited_by,
			expires_at  = EXCLUDED.expires_at,
			created_at  = now()
		RETURNING id`,
		orgID, email, role, hash, invitedBy, time.Now().Add(inviteTTL),
	).Scan(&id)
	if err != nil {
		return 0, "", err
	}
	return id, token, nil
}

// ListInvites returns orgID's outstanding (unused, unexpired) invites,
// newest first.
func ListInvites(ctx context.Context, q core.Querier, orgID int64) ([]Invite, error) {
	rows, err := q.Query(ctx, `
		SELECT id, email, role, expires_at
		FROM invites
		WHERE org_id = $1 AND used_at IS NULL AND expires_at > now()
		ORDER BY created_at DESC`,
		orgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invites []Invite
	for rows.Next() {
		var inv Invite
		if err := rows.Scan(&inv.ID, &inv.Email, &inv.Role, &inv.ExpiresAt); err != nil {
			return nil, err
		}
		invites = append(invites, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if invites == nil {
		invites = []Invite{}
	}
	return invites, nil
}

// RevokeInvite deletes orgID's invite id. It returns core.ErrNotFound if no
// such invite exists for that org (including a cross-org id, so the caller
// can render that as a 404 without leaking whether the id exists elsewhere).
func RevokeInvite(ctx context.Context, q core.Querier, orgID, id int64) error {
	tag, err := q.Exec(ctx, "DELETE FROM invites WHERE id = $1 AND org_id = $2", id, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return core.ErrNotFound
	}
	return nil
}

// InviteByToken looks up the invite identified by the plaintext token,
// regardless of whether it's already used or expired - the caller (GET
// /api/v1/invites/{token}) renders Expired rather than erroring, so the
// invite page can tell the invitee their invite expired instead of just
// 404ing. An unrecognised token returns core.ErrNotFound.
func InviteByToken(ctx context.Context, q core.Querier, token string) (InviteDetails, error) {
	hash := hashToken(token)

	var d InviteDetails
	var expiresAt time.Time
	var usedAt *time.Time
	err := q.QueryRow(ctx, `
		SELECT o.name, i.email, i.role, i.expires_at, i.used_at
		FROM invites i
		JOIN orgs o ON o.id = i.org_id
		WHERE i.token_hash = $1`,
		hash,
	).Scan(&d.OrgName, &d.Email, &d.Role, &expiresAt, &usedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return InviteDetails{}, core.ErrNotFound
	}
	if err != nil {
		return InviteDetails{}, err
	}

	d.Expired = usedAt != nil || time.Now().After(expiresAt)
	return d, nil
}

// AcceptInvite accepts the invite identified by the plaintext token, in a
// single transaction that atomically consumes the token (so a reused or
// racing accept always fails rather than ever granting membership twice).
//
// When loggedInUserID is non-nil, the invite's email must match that
// user's own (ErrEmailMismatch otherwise) and only a membership is added.
// When nil, name and password must be supplied: password must be at least
// minInvitePasswordLength characters (ErrPasswordTooShort otherwise), and
// an existing account for the invite's email is rejected with
// ErrAccountExists ("sign in first, then open the link again") rather than
// silently added to - this flow is for people who don't have an account
// yet. On success for this path, a new session is started and its cookie
// set on w, exactly as LoginHandler would.
func (a *Auth) AcceptInvite(ctx context.Context, w http.ResponseWriter, token string, loggedInUserID *int64, name, password string) error {
	if loggedInUserID == nil && len(password) < minInvitePasswordLength {
		return ErrPasswordTooShort
	}

	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var orgID int64
	var email, role string
	err = tx.QueryRow(ctx, `
		UPDATE invites SET used_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING org_id, email, role`,
		hashToken(token),
	).Scan(&orgID, &email, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInviteInvalid
	}
	if err != nil {
		return err
	}

	var userID int64
	if loggedInUserID != nil {
		var loggedInEmail string
		if err := tx.QueryRow(ctx, "SELECT email FROM users WHERE id = $1", *loggedInUserID).Scan(&loggedInEmail); err != nil {
			return err
		}
		if !strings.EqualFold(loggedInEmail, email) {
			return ErrEmailMismatch
		}
		userID = *loggedInUserID
	} else {
		err := tx.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", email).Scan(&userID)
		if err == nil {
			return ErrAccountExists
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		hash, err := HashPassword(password)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			"INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id",
			email, name, hash,
		).Scan(&userID); err != nil {
			return err
		}
	}

	// ON CONFLICT DO NOTHING: a user who somehow already holds a
	// membership in this org keeps their existing role rather than having
	// an invite silently change it.
	if _, err := tx.Exec(ctx,
		"INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, $3) ON CONFLICT (user_id, org_id) DO NOTHING",
		userID, orgID, role,
	); err != nil {
		return err
	}

	if loggedInUserID == nil {
		if err := a.StartSession(ctx, tx, w, userID); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// PruneInvites deletes every invite that's already used or has expired
// (Ruling 49: Task 3 prunes its own table). It's called by the hourly
// scheduler alongside PruneSessions.
func PruneInvites(ctx context.Context, q core.Querier, now time.Time) error {
	_, err := q.Exec(ctx, "DELETE FROM invites WHERE used_at IS NOT NULL OR expires_at < $1", now)
	return err
}

// UserFromRequest resolves r's session cookie to a user ID, if any. ok is
// false when there's no cookie or the session is unknown/expired - the
// caller treats the request as anonymous rather than erroring, since routes
// like invite acceptance work both signed in and signed out. It exists so
// such routes can check for an optional session without duplicating
// RequireOrg/RequireUser's cookie-reading code.
func (a *Auth) UserFromRequest(r *http.Request) (userID int64, ok bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return 0, false
	}
	id, err := userIDForToken(r.Context(), a.pool, c.Value)
	if err != nil {
		return 0, false
	}
	return id, true
}
