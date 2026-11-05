-- Core schema: orgs, users, contacts, invoicing and payments.
--
-- citext is used for case-insensitive email matching. The extension is
-- database-wide, so it is installed once into the public schema; it must
-- stay resolvable from any schema that puts public later in its
-- search_path (see internal/db/testdb.go).
CREATE EXTENSION IF NOT EXISTS citext SCHEMA public;

CREATE TABLE orgs (
    id              bigserial PRIMARY KEY,
    slug            text NOT NULL UNIQUE,
    name            text NOT NULL,
    abn             text,
    gst_registered  boolean NOT NULL DEFAULT false,
    bas_frequency   text CHECK (bas_frequency IN ('quarterly', 'monthly')),
    fy_start_month  smallint NOT NULL DEFAULT 7,
    currency        char(3) NOT NULL DEFAULT 'AUD',
    bank_bsb        text,
    bank_account    text,
    payid           text
);

CREATE TABLE users (
    id              bigserial PRIMARY KEY,
    email           public.citext NOT NULL UNIQUE,
    name            text NOT NULL,
    password_hash   text,
    oidc_subject    text UNIQUE,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE contacts (
    id                      bigserial PRIMARY KEY,
    org_id                  bigint NOT NULL REFERENCES orgs,
    kind                    text CHECK (kind IN ('tenant', 'client', 'supplier')),
    name                    text NOT NULL,
    email                   text,
    phone                   text,
    company_name            text,
    abn                     text,
    provider_customer_ref   text,
    UNIQUE (org_id, provider_customer_ref)
);

CREATE TABLE categories (
    id                  bigserial PRIMARY KEY,
    org_id              bigint NOT NULL REFERENCES orgs,
    name                text NOT NULL,
    type                text CHECK (type IN ('income', 'expense')),
    default_gst_code    text,
    UNIQUE (org_id, name)
);

CREATE TABLE memberships (
    user_id     bigint NOT NULL REFERENCES users,
    org_id      bigint NOT NULL REFERENCES orgs,
    role        text CHECK (role IN ('owner', 'staff', 'bookkeeper', 'tenant', 'client')),
    contact_id  bigint REFERENCES contacts,
    PRIMARY KEY (user_id, org_id)
);

CREATE TABLE sessions (
    token_hash  bytea PRIMARY KEY,
    user_id     bigint NOT NULL REFERENCES users,
    expires_at  timestamptz NOT NULL
);

CREATE TABLE invoices (
    id                  bigserial PRIMARY KEY,
    org_id              bigint NOT NULL REFERENCES orgs,
    contact_id          bigint NOT NULL REFERENCES contacts,
    number              int,
    source              text CHECK (source IN ('rent', 'bond', 'timesheet', 'manual')),
    status              text NOT NULL DEFAULT 'draft'
                            CHECK (status IN ('draft', 'open', 'paid', 'void', 'uncollectible')),
    issue_date          date,
    due_date            date,
    currency            char(3) NOT NULL DEFAULT 'AUD',
    subtotal            bigint NOT NULL DEFAULT 0,
    gst_total           bigint NOT NULL DEFAULT 0,
    total               bigint NOT NULL DEFAULT 0,
    payment_reference   text,
    provider_ref        text,
    provider_url        text,
    UNIQUE (org_id, number),
    UNIQUE (org_id, payment_reference)
);

CREATE TABLE invoice_counters (
    org_id      bigint PRIMARY KEY REFERENCES orgs,
    last_number int NOT NULL DEFAULT 0
);

CREATE TABLE invoice_lines (
    id              bigserial PRIMARY KEY,
    invoice_id      bigint NOT NULL REFERENCES invoices,
    org_id          bigint NOT NULL REFERENCES orgs,
    description     text NOT NULL,
    quantity_milli  bigint NOT NULL,
    unit_amount     bigint NOT NULL,
    gst_code        text,
    gst_amount      bigint NOT NULL DEFAULT 0,
    amount          bigint NOT NULL,
    -- No FKs yet: property and project tables arrive in later plans.
    property_id     bigint,
    project_id      bigint
);

CREATE TABLE payments (
    id              bigserial PRIMARY KEY,
    org_id          bigint NOT NULL REFERENCES orgs,
    invoice_id      bigint NOT NULL REFERENCES invoices,
    amount          bigint NOT NULL CHECK (amount > 0),
    paid_on         date NOT NULL,
    method          text CHECK (method IN ('stripe', 'bank_transfer', 'payid', 'cash', 'other')),
    provider_ref    text,
    -- No FK yet: bank reconciliation lines arrive in a later plan.
    bank_line_id    bigint,
    UNIQUE (org_id, provider_ref)
);

CREATE TABLE webhook_events (
    provider    text NOT NULL,
    event_id    text NOT NULL,
    org_id      bigint NOT NULL REFERENCES orgs,
    received_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, event_id)
);

CREATE TABLE audit_log (
    id              bigserial PRIMARY KEY,
    org_id          bigint NOT NULL REFERENCES orgs,
    actor_user_id   bigint REFERENCES users,
    entity          text NOT NULL,
    entity_id       bigint NOT NULL,
    action          text NOT NULL,
    diff            jsonb,
    at              timestamptz NOT NULL DEFAULT now()
);
