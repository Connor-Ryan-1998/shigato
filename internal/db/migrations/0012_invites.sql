-- Staff invites: an owner invites someone by email to join their org with a
-- given role. The invite is single-use (token_hash, consumed atomically by
-- UPDATE ... RETURNING) and expires after 7 days (enforced in Go,
-- internal/auth/invites.go). A partial unique index on (org_id, email)
-- WHERE used_at IS NULL means creating a second invite for the same email
-- replaces the outstanding one instead of duplicating it.
CREATE TABLE invites (
    id          bigserial PRIMARY KEY,
    org_id      bigint NOT NULL REFERENCES orgs,
    email       public.citext NOT NULL,
    role        text NOT NULL CHECK (role IN ('owner', 'staff', 'bookkeeper')),
    token_hash  bytea NOT NULL UNIQUE,
    invited_by  bigint REFERENCES users,
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX invites_org_email_outstanding ON invites (org_id, email) WHERE used_at IS NULL;
