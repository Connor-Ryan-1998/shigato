-- Property module: properties, leases, tenants, rent changes, recurring
-- charges and (for a later task's auto-invoicing) rent periods.
CREATE TABLE properties (
    id              bigserial PRIMARY KEY,
    org_id          bigint NOT NULL REFERENCES orgs,
    name            text NOT NULL,
    address         text,
    kind            text,
    residential     boolean NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE leases (
    id                  bigserial PRIMARY KEY,
    org_id              bigint NOT NULL REFERENCES orgs,
    property_id         bigint NOT NULL REFERENCES properties,
    start_date          date NOT NULL,
    end_date            date,
    termination_date    date,
    frequency           text CHECK (frequency IN ('weekly', 'fortnightly', 'monthly')),
    paid_to_date        date,
    opening_arrears     bigint NOT NULL DEFAULT 0,
    bond_amount         bigint NOT NULL DEFAULT 0,
    bond_invoice_id     bigint REFERENCES invoices,
    rta_lodgement_ref   text,
    bond_lodged_on      date,
    created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE lease_tenants (
    lease_id    bigint NOT NULL REFERENCES leases,
    contact_id  bigint NOT NULL REFERENCES contacts,
    is_primary  boolean NOT NULL DEFAULT false,
    PRIMARY KEY (lease_id, contact_id)
);

CREATE UNIQUE INDEX lease_tenants_one_primary ON lease_tenants (lease_id) WHERE is_primary;

CREATE TABLE lease_rent_changes (
    id              bigserial PRIMARY KEY,
    lease_id        bigint NOT NULL REFERENCES leases,
    effective_date  date NOT NULL,
    amount          bigint NOT NULL CHECK (amount > 0),
    UNIQUE (lease_id, effective_date)
);

CREATE TABLE lease_charges (
    id              bigserial PRIMARY KEY,
    lease_id        bigint NOT NULL REFERENCES leases,
    description     text NOT NULL,
    amount          bigint NOT NULL CHECK (amount > 0),
    start_date      date NOT NULL,
    end_date        date
);

CREATE TABLE rent_periods (
    id              bigserial PRIMARY KEY,
    org_id          bigint NOT NULL REFERENCES orgs,
    lease_id        bigint NOT NULL REFERENCES leases,
    period_start    date NOT NULL,
    period_end      date NOT NULL,
    amount_due      bigint NOT NULL,
    invoice_id      bigint REFERENCES invoices,
    UNIQUE (lease_id, period_start)
);

ALTER TABLE invoice_lines
    ADD CONSTRAINT invoice_lines_property_id_fkey FOREIGN KEY (property_id) REFERENCES properties;
