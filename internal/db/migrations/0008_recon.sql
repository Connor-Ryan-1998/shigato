-- Reconciliation module: bank accounts, their CSV imports, and the
-- individual statement lines those imports produce. A line is deduplicated
-- within its bank account by dedupe_key (see internal/importers), so
-- re-importing the same file - or an overlapping later export - only ever
-- adds genuinely new lines. payments.bank_line_id (added in 0001, without
-- its FK since bank_lines didn't exist yet) gets that FK here.
CREATE TABLE bank_accounts (
    id              bigserial PRIMARY KEY,
    org_id          bigint NOT NULL REFERENCES orgs,
    name            text NOT NULL,
    csv_mapping     jsonb NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);

CREATE TABLE bank_imports (
    id              bigserial PRIMARY KEY,
    org_id          bigint NOT NULL REFERENCES orgs,
    bank_account_id bigint NOT NULL REFERENCES bank_accounts,
    file_sha256     bytea NOT NULL,
    rows_total      int NOT NULL,
    rows_new        int NOT NULL,
    imported_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE bank_lines (
    id              bigserial PRIMARY KEY,
    org_id          bigint NOT NULL REFERENCES orgs,
    bank_account_id bigint NOT NULL REFERENCES bank_accounts,
    import_id       bigint NOT NULL REFERENCES bank_imports,
    posted_on       date NOT NULL,
    amount          bigint NOT NULL CHECK (amount <> 0),
    description     text NOT NULL,
    dedupe_key      bytea NOT NULL,
    status          text NOT NULL DEFAULT 'unmatched' CHECK (status IN ('unmatched', 'matched', 'ignored')),
    ignore_reason   text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (bank_account_id, dedupe_key)
);

CREATE INDEX bank_lines_org_status_posted ON bank_lines (org_id, status, posted_on);

ALTER TABLE payments
    ADD CONSTRAINT payments_bank_line_fk FOREIGN KEY (bank_line_id) REFERENCES bank_lines (id);
