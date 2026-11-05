-- Categories, split expenses and receipt files. capital is added to
-- categories (not a new table) so the default "Equipment" category can be
-- flagged for later capital-vs-revenue BAS treatment. Existing orgs are
-- seeded with the same 15 default expense categories core.CreateOrg seeds
-- new orgs with (internal/core/orgs.go); ON CONFLICT DO NOTHING means an
-- org that already has a same-named category keeps its own row.
ALTER TABLE categories ADD COLUMN capital boolean NOT NULL DEFAULT false;

INSERT INTO categories (org_id, name, type, default_gst_code, capital)
SELECT o.id, v.name, 'expense', v.default_gst_code, v.capital
FROM orgs o
CROSS JOIN (VALUES
    ('Repairs & maintenance',     'GST',  false),
    ('Insurance',                 'GST',  false),
    ('Council rates',             'N-T',  false),
    ('Water',                     'FRE',  false),
    ('Strata / body corporate',   'GST',  false),
    ('Agent & advertising fees',  'GST',  false),
    ('Loan interest',             'INP',  false),
    ('Bank fees',                 'INP',  false),
    ('Stripe fees',               'GST',  false),
    ('Software & subscriptions',  'GST',  false),
    ('Accounting & legal',        'GST',  false),
    ('Travel',                    'GST',  false),
    ('Office & supplies',         'GST',  false),
    ('Equipment',                 'GST',  true),
    ('Other expenses',            'GST',  false)
) AS v(name, default_gst_code, capital)
ON CONFLICT (org_id, name) DO NOTHING;

-- files holds uploaded receipts. The stored path on disk is derived only
-- from org id + hex sha256 (see internal/recon/files.go), never the
-- original filename; original_name is kept only as display/download
-- metadata. UNIQUE(org_id, sha256) is what makes re-uploading identical
-- bytes return the existing row instead of a duplicate.
CREATE TABLE files (
    id              bigserial PRIMARY KEY,
    org_id          bigint NOT NULL REFERENCES orgs,
    sha256          bytea NOT NULL,
    mime            text NOT NULL,
    size_bytes      int NOT NULL,
    original_name   text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, sha256)
);

CREATE TABLE expenses (
    id                  bigserial PRIMARY KEY,
    org_id              bigint NOT NULL REFERENCES orgs,
    spent_on            date NOT NULL,
    supplier_contact_id bigint REFERENCES contacts,
    description         text NOT NULL,
    receipt_file_id     bigint REFERENCES files,
    bank_line_id        bigint UNIQUE REFERENCES bank_lines,
    created_at          timestamptz NOT NULL DEFAULT now()
);

-- Each expense line carries its own category, GST code/amount and optional
-- property/project tag, so one expense can be split across several cost
-- centres (e.g. a shared invoice covering two properties).
CREATE TABLE expense_lines (
    id              bigserial PRIMARY KEY,
    org_id          bigint NOT NULL REFERENCES orgs,
    expense_id      bigint NOT NULL REFERENCES expenses ON DELETE CASCADE,
    category_id     bigint NOT NULL REFERENCES categories,
    amount          bigint NOT NULL CHECK (amount > 0),
    gst_code        text NOT NULL,
    gst_amount      bigint NOT NULL,
    property_id     bigint REFERENCES properties,
    project_id      bigint REFERENCES projects,
    CHECK (property_id IS NULL OR project_id IS NULL)
);

CREATE INDEX expense_lines_expense ON expense_lines (expense_id);
