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

// magicLinkTTL is how long a magic link is valid for (global-constraints.md
// "Tokens": magic link 15 minutes).
const magicLinkTTL = 15 * time.Minute

// Magic-request rate limit (global-constraints.md "Rate limits"): at most 5
// requests per email per hour. Every request counts, including ones for an
// unknown or ineligible email - RequestMagicLinkHandler never calls
// Succeed, matching global-constraints.md's "every request counts".
const (
	magicRequestLimit  = 5
	magicRequestWindow = time.Hour
)

// ErrMagicLinkInvalid means the token is unknown, already used, expired, or
// belongs to a user who no longer qualifies for portal sign-in (Ruling 59:
// a user who's gained a staff membership since the link was issued). The
// caller maps this to 410 Gone with a fixed message - the same message in
// every case, so a probed token never reveals which of these it was.
var ErrMagicLinkInvalid = errors.New("This link has expired — request a new one")

// newMagicToken generates a 32-random-byte token, base64url encoded (per
// global-constraints.md), and returns it along with the SHA-256 hash that's
// actually stored.
func newMagicToken() (token string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, hashToken(token), nil
}

// userEligibleForPortal reports whether userID holds at least one tenant or
// client membership and no staff-role (owner/staff/bookkeeper) membership
// in any org (Ruling 59: a magic link must never yield a staff session).
func userEligibleForPortal(ctx context.Context, q core.Querier, userID int64) (bool, error) {
	var hasStaff, hasPortal bool
	err := q.QueryRow(ctx, `
		SELECT
			EXISTS(SELECT 1 FROM memberships WHERE user_id = $1 AND role = ANY($2)),
			EXISTS(SELECT 1 FROM memberships WHERE user_id = $1 AND role IN ('tenant', 'client'))`,
		userID, staffRoles,
	).Scan(&hasStaff, &hasPortal)
	if err != nil {
		return false, err
	}
	return hasPortal && !hasStaff, nil
}

// issueMagicLink creates and stores a fresh magic-link token for userID,
// unconditionally - callers that need the eligibility check (RequestMagicLink)
// run it first; IssueMagicLinkForUser's caller (portal-invite) has just
// created or confirmed a tenant/client membership, which already guarantees
// eligibility.
func issueMagicLink(ctx context.Context, q core.Querier, userID int64) (token string, err error) {
	token, hash, err := newMagicToken()
	if err != nil {
		return "", err
	}
	if _, err := q.Exec(ctx,
		"INSERT INTO magic_links (token_hash, user_id, expires_at) VALUES ($1, $2, $3)",
		hash, userID, time.Now().Add(magicLinkTTL),
	); err != nil {
		return "", err
	}
	return token, nil
}

// IssueMagicLinkForUser issues a token for userID unconditionally, for the
// portal-invite endpoint: it has just found or created userID's account and
// given it a tenant/client membership, so eligibility is already guaranteed
// by construction.
func IssueMagicLinkForUser(ctx context.Context, q core.Querier, userID int64) (string, error) {
	return issueMagicLink(ctx, q, userID)
}

// RequestMagicLink looks up email and, if it belongs to a user eligible for
// portal sign-in (userEligibleForPortal), issues a token. An unknown email
// or one that isn't eligible (staff-only, or no tenant/client membership at
// all) returns an empty token and a nil error - never an error the caller
// could use to distinguish the cases - so the handler can return the exact
// same 202 {} either way (no account enumeration). The DB lookup runs on
// every call, known email or not, so the two paths take roughly the same
// time.
func RequestMagicLink(ctx context.Context, q core.Querier, email string) (token string, err error) {
	var userID int64
	err = q.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	eligible, err := userEligibleForPortal(ctx, q, userID)
	if err != nil {
		return "", err
	}
	if !eligible {
		return "", nil
	}

	return issueMagicLink(ctx, q, userID)
}

// AllowMagicRequest reserves a slot in the per-email magic-request rate
// limit (5/hour), keyed the same way LoginHandler keys its own per-email
// limit. Every call that returns ok counts towards the limit; there's no
// Succeed for a magic request, since global-constraints.md requires every
// request - eligible or not - to count.
func (a *Auth) AllowMagicRequest(email string) (ok bool, retryAfter time.Duration) {
	return a.limiter.Allow("magic:"+strings.ToLower(email), magicRequestLimit, magicRequestWindow)
}

// VerifyMagicLink atomically consumes the magic link identified by the
// plaintext token and starts a session for its user, in a single
// transaction: the token is consumed first (an invalid/used/expired token
// fails with ErrMagicLinkInvalid before anything else runs), then
// userEligibleForPortal is re-checked (Ruling 59) in case the user gained a
// staff membership since the link was issued - if so, the whole transaction
// is rolled back (the token is left unconsumed, though still useless: it'll
// keep failing this same check until it expires) and ErrMagicLinkInvalid is
// returned, exactly as for an already-used token.
func (a *Auth) VerifyMagicLink(ctx context.Context, w http.ResponseWriter, token string) error {
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID int64
	err = tx.QueryRow(ctx, `
		UPDATE magic_links SET used_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING user_id`,
		hashToken(token),
	).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrMagicLinkInvalid
	}
	if err != nil {
		return err
	}

	eligible, err := userEligibleForPortal(ctx, tx, userID)
	if err != nil {
		return err
	}
	if !eligible {
		return ErrMagicLinkInvalid
	}

	if err := a.StartSession(ctx, tx, w, userID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// PruneMagicLinks deletes every magic_links row that's already used or has
// expired (Ruling 49: this task prunes only its own table). It's called by
// the hourly scheduler alongside PruneSessions, PruneInvites and
// PruneOIDCStates.
func PruneMagicLinks(ctx context.Context, q core.Querier, now time.Time) error {
	_, err := q.Exec(ctx, "DELETE FROM magic_links WHERE used_at IS NOT NULL OR expires_at < $1", now)
	return err
}
