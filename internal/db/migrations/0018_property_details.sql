-- Property details, insurance policies, depreciation schedule and the
-- Item 21 (ATO rental schedule) label on expense categories.
ALTER TABLE properties
    ADD COLUMN contract_date        date,
    ADD COLUMN settlement_date      date,
    ADD COLUMN purchase_price       bigint CHECK (purchase_price >= 0),
    ADD COLUMN stamp_duty           bigint CHECK (stamp_duty >= 0),
    ADD COLUMN purchase_legal_costs bigint CHECK (purchase_legal_costs >= 0),
    ADD COLUMN year_built           int,
    ADD COLUMN lot_plan             text,
    ADD COLUMN land_area_m2         numeric(12, 2) CHECK (land_area_m2 >= 0),
    ADD COLUMN council_account_ref  text,
    ADD COLUMN water_account_ref    text,
    ADD COLUMN notes                text NOT NULL DEFAULT '';

CREATE TABLE insurance_policies (
    id              bigserial PRIMARY KEY,
    org_id          bigint NOT NULL REFERENCES orgs,
    property_id     bigint NOT NULL REFERENCES properties,
    insurer         text NOT NULL,
    type            text NOT NULL CHECK (type IN ('building', 'landlord', 'strata_contents', 'other')),
    policy_number   text NOT NULL DEFAULT '',
    start_date      date NOT NULL,
    renewal_date    date NOT NULL,
    sum_insured     bigint CHECK (sum_insured >= 0),
    premium         bigint CHECK (premium >= 0),
    excess          bigint CHECK (excess >= 0),
    file_id         bigint REFERENCES files,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CHECK (renewal_date >= start_date)
);

CREATE INDEX insurance_policies_property ON insurance_policies (property_id);

CREATE TABLE depreciation_items (
    id              bigserial PRIMARY KEY,
    org_id          bigint NOT NULL REFERENCES orgs,
    property_id     bigint NOT NULL REFERENCES properties,
    name            text NOT NULL,
    type            text NOT NULL CHECK (type IN ('capital_works', 'plant_equipment')),
    installed_on    date NOT NULL,
    cost            bigint CHECK (cost >= 0),
    notes           text NOT NULL DEFAULT '',
    expense_id      bigint REFERENCES expenses,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX depreciation_items_property ON depreciation_items (property_id);

-- fy is the financial year's end year (FY2027 = 2027), matching the
-- reports' FY<year> naming.
CREATE TABLE depreciation_amounts (
    item_id     bigint NOT NULL REFERENCES depreciation_items ON DELETE CASCADE,
    fy          int NOT NULL,
    amount      bigint NOT NULL CHECK (amount > 0),
    PRIMARY KEY (item_id, fy)
);

CREATE TABLE depreciation_documents (
    item_id     bigint NOT NULL REFERENCES depreciation_items ON DELETE CASCADE,
    file_id     bigint NOT NULL REFERENCES files,
    PRIMARY KEY (item_id, file_id)
);

-- capital_allowances and capital_works are fed only by depreciation items,
-- so a category may not carry them.
ALTER TABLE categories ADD COLUMN item21_label text CHECK (item21_label IN (
    'advertising', 'body_corporate', 'borrowing', 'cleaning', 'council_rates',
    'gardening', 'insurance', 'interest', 'land_tax', 'legal', 'pest_control',
    'agent_fees', 'repairs', 'stationery', 'water', 'sundry'
));

UPDATE categories SET item21_label = v.label
FROM (VALUES
    ('Repairs & maintenance',    'repairs'),
    ('Insurance',                'insurance'),
    ('Council rates',            'council_rates'),
    ('Water',                    'water'),
    ('Strata / body corporate',  'body_corporate'),
    ('Agent & advertising fees', 'agent_fees'),
    ('Loan interest',            'interest'),
    ('Accounting & legal',       'legal'),
    ('Bank fees',                'sundry')
) AS v(name, label)
WHERE categories.name = v.name AND categories.type = 'expense';
