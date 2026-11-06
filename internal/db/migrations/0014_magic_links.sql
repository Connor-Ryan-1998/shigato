-- Magic links: a one-time sign-in token emailed to a tenant/client portal
-- user (global-constraints.md "Tokens": 15-minute lifetime). Single-use,
-- consumed atomically by UPDATE ... SET used_at = now() WHERE token_hash =
-- $1 AND used_at IS NULL AND expires_at > now() RETURNING user_id, the same
-- convention as invites.token_hash and oidc_states.state_hash. Rows are
-- pruned by the hourly scheduler (internal/auth.PruneMagicLinks) once
-- expired or used.
CREATE TABLE magic_links (
    token_hash  bytea PRIMARY KEY,
    user_id     bigint NOT NULL REFERENCES users,
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
