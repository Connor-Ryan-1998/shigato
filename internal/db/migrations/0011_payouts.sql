-- Stripe payouts: a payout groups the webhook payments it transferred to
-- the org's bank account into a single bank line, and books the provider's
-- fees as a "Stripe fees" expense. bank_line_id is UNIQUE: at most one
-- payout per bank line, matching expenses.bank_line_id's own uniqueness.
CREATE TABLE payouts (
    id              bigserial PRIMARY KEY,
    org_id          bigint NOT NULL REFERENCES orgs,
    provider_ref    text NOT NULL,
    amount          bigint NOT NULL,
    arrival_on      date NOT NULL,
    bank_line_id    bigint NOT NULL UNIQUE REFERENCES bank_lines,
    fee_expense_id  bigint REFERENCES expenses,
    UNIQUE (org_id, provider_ref)
);

ALTER TABLE payments ADD COLUMN payout_id bigint REFERENCES payouts;
