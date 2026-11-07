package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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
	// ErrNameRequired means an unauthenticated accept supplied a blank (or
	// all-whitespace) name. 422.
	ErrNameRequired = errors.New("name is required")
	// ErrAlreadyMember means CreateInvite was asked to invite an email that
	// already belongs to a member of the org. 409.
	ErrAlreadyMember = errors.New("this person is already a member of this organisation")
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

// NeedsPasswordForInvite reports whether the currently signed-in userID must
// supply a password to accept a staff invite (Ruling 63): true only when the
// account has no password set (password_hash IS NULL - e.g. a portal-only
// user created via magic link) and OIDC is disabled, so there's no other way
// for them to ever sign back in as staff. When OIDC is enabled, they sign in
// via SSO instead and no password is required.
func (a *Auth) NeedsPasswordForInvite(ctx context.Context, userID int64) (bool, error) {
	if a.OIDCEnabled() {
		return false, nil
	}
	var hash *string
	if err := a.pool.QueryRow(ctx, "SELECT password_hash FROM users WHERE id = $1", userID).Scan(&hash); err != nil {
		return false, err
	}
	return hash == nil, nil
}

// newInviteToken generates a 32-random-byte token, base64url encoded (per
// global-constraints.md), and returns it along with the SHA-256 hash that's
// actually stored.
func newInviteToken() (token string, hash []byte, err error) {
	return newRandomToken()
}

// CreateInvite creates an invite for email to join orgID with role,
// replacing any outstanding (unused) invite already sitting for that email
// (the partial unique index on (org_id, email) WHERE used_at IS NULL is the
// conflict target). It returns the invite's id and the plaintext token -
// callers build the link and send it; only the token's hash is stored. It
// returns ErrAlreadyMember if email already belongs to a member of orgID -
// inviting them again would be confusing (and AcceptInvite would just 409
// its own accept with ErrAccountExists for the no-session path).
func CreateInvite(ctx context.Context, q core.Querier, orgID int64, email, role string, invitedBy int64) (id int64, token string, err error) {
	var exists int
	err = q.QueryRow(ctx, `
		SELECT 1 FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.org_id = $1 AND u.email = $2`,
		orgID, email,
	).Scan(&exists)
	if err == nil {
		return 0, "", ErrAlreadyMember
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, "", err
	}

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
// On success it returns the org's slug, so the caller can send the SPA
// straight there.
//
// Token validity is checked first: an invalid/used/expired token
// (ErrInviteInvalid) always wins over a validation error on the request
// body, even when both are true, because every later step runs inside the
// same not-yet-committed transaction - a validation failure after this
// point rolls back the token consumption too, leaving the token usable for
// a retry with a corrected body.
//
// When loggedInUserID is non-nil, the invite's email must match that
// user's own (ErrEmailMismatch otherwise) and only a membership is added.
// Ruling 63: if that account has no password (password_hash IS NULL - a
// portal-only account from a magic link) and OIDC is disabled, password must
// also be supplied (ErrPasswordTooShort if under minInvitePasswordLength)
// and is set on the account in this same tx; with OIDC enabled no password
// is required, since the user signs in via SSO afterwards.
// When nil, name and password must be supplied: name must be non-blank
// (ErrNameRequired otherwise) and password must be at least
// minInvitePasswordLength characters (ErrPasswordTooShort otherwise), and
// an existing account for the invite's email - including one created by a
// concurrent accept racing this one - is rejected with ErrAccountExists
// ("sign in first, then open the link again") rather than silently added
// to - this flow is for people who don't have an account yet.
//
// Ruling 64 (security): whenever this call actually grants a new
// membership (not a no-op re-accept of one the user already held), every
// session the user held before this call is deleted in the same tx, and a
// fresh session is started and its cookie set on w, exactly as LoginHandler
// would - a staff role must never be reachable through a session that
// predates the grant.
func (a *Auth) AcceptInvite(ctx context.Context, w http.ResponseWriter, token string, loggedInUserID *int64, name, password string) (orgSlug string, err error) {
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return "", err
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
		return "", ErrInviteInvalid
	}
	if err != nil {
		return "", err
	}

	var userID int64
	var newAccount bool
	if loggedInUserID != nil {
		var loggedInEmail string
		var passwordHash *string
		if err := tx.QueryRow(ctx, "SELECT email, password_hash FROM users WHERE id = $1", *loggedInUserID).
			Scan(&loggedInEmail, &passwordHash); err != nil {
			return "", err
		}
		if !strings.EqualFold(loggedInEmail, email) {
			return "", ErrEmailMismatch
		}
		userID = *loggedInUserID

		// Ruling 63: a signed-in user with no password (e.g. a portal-only
		// account created via magic link) must set one to accept a staff
		// invite, unless OIDC is enabled and they'll sign in via SSO
		// instead.
		if passwordHash == nil && !a.OIDCEnabled() {
			if len(password) < minInvitePasswordLength {
				return "", ErrPasswordTooShort
			}
			hash, err := HashPassword(password)
			if err != nil {
				return "", err
			}
			if _, err := tx.Exec(ctx, "UPDATE users SET password_hash = $1 WHERE id = $2", hash, userID); err != nil {
				return "", err
			}
		}
	} else {
		if strings.TrimSpace(name) == "" {
			return "", ErrNameRequired
		}
		if len(password) < minInvitePasswordLength {
			return "", ErrPasswordTooShort
		}

		err := tx.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", email).Scan(&userID)
		if err == nil {
			return "", ErrAccountExists
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}

		hash, err := HashPassword(password)
		if err != nil {
			return "", err
		}
		if err := tx.QueryRow(ctx,
			"INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id",
			email, name, hash,
		).Scan(&userID); err != nil {
			// A concurrent accept for the same email (e.g. two outstanding
			// invites to different orgs) can race this exact check-then-
			// insert; the unique constraint on users.email is the real
			// guard, and a violation here means the same thing the earlier
			// SELECT would have found had it run a moment later.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return "", ErrAccountExists
			}
			return "", err
		}
		newAccount = true
	}

	// ON CONFLICT DO NOTHING: a user who somehow already holds a
	// membership in this org keeps their existing role rather than having
	// an invite silently change it. RowsAffected tells gainedMembership
	// apart from that no-op case.
	tag, err := tx.Exec(ctx,
		"INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, $3) ON CONFLICT (user_id, org_id) DO NOTHING",
		userID, orgID, role,
	)
	if err != nil {
		return "", err
	}
	gainedMembership := tag.RowsAffected() == 1

	if newAccount {
		if err := core.Audit(ctx, tx, orgID, &userID, "user", userID, "create", map[string]string{"email": email}); err != nil {
			return "", err
		}
	}
	if err := core.Audit(ctx, tx, orgID, &userID, "membership", userID, "accept_invite", map[string]string{"role": role}); err != nil {
		return "", err
	}

	// Ruling 64 (security): gaining a staff membership here invalidates
	// every session the user already held (e.g. one obtained earlier via a
	// portal magic link, on this device or another) before starting the
	// fresh one this request gets - a staff role must never be reachable
	// through a session that predates the grant.
	if gainedMembership {
		if _, err := tx.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1", userID); err != nil {
			return "", err
		}
	}
	if loggedInUserID == nil || gainedMembership {
		if err := a.StartSession(ctx, tx, w, userID); err != nil {
			return "", err
		}
	}

	if err := tx.QueryRow(ctx, "SELECT slug FROM orgs WHERE id = $1", orgID).Scan(&orgSlug); err != nil {
		return "", err
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return orgSlug, nil
}

// inviteRetention is how long a used or expired invite row is kept after it
// stopped being acceptable, before PruneInvites deletes it. An invitee who
// waits past expiry to open their link still sees "this invite has expired"
// (InviteByToken finds the row and reports Expired) rather than "not found",
// which reads as if the link were simply wrong, for this long.
const inviteRetention = 30 * 24 * time.Hour

// PruneInvites deletes every invite that's been used or expired for more
// than inviteRetention (Ruling 49: Task 3 prunes its own table). It's called
// by the hourly scheduler alongside PruneSessions. Keeping used/expired rows
// around briefly (rather than deleting them the moment they stop being
// acceptable) is so InviteByToken can still tell an invitee their invite
// expired instead of just 404ing.
func PruneInvites(ctx context.Context, q core.Querier, now time.Time) error {
	_, err := q.Exec(ctx, "DELETE FROM invites WHERE expires_at < $1", now.Add(-inviteRetention))
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
