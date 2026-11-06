-- OIDC login state: one row per in-flight authorization-code-with-PKCE
-- round trip, from GET /api/v1/auth/oidc/start to the callback. state_hash
-- is the SHA-256 of the random state token sent as the "state" query
-- parameter, so a leaked oidc_states row (e.g. from a log) can't be replayed
-- - the same convention as invites.token_hash and sessions.token_hash.
-- Consumed atomically by UPDATE ... SET used_at = now() WHERE state_hash =
-- $1 AND used_at IS NULL AND expires_at > now() RETURNING ..., so a missing,
-- already-used or expired state all fail the same way (/login?error=sso_expired).
-- Rows are pruned by the hourly scheduler (internal/auth.PruneOIDCStates)
-- once expired or used; global-constraints.md sets the 10-minute lifetime
-- as a Go constant, enforced here only via expires_at.
CREATE TABLE oidc_states (
    state_hash      bytea PRIMARY KEY,
    nonce           text NOT NULL,
    code_verifier   text NOT NULL,
    next            text NOT NULL,
    expires_at      timestamptz NOT NULL,
    used_at         timestamptz NULL
);
