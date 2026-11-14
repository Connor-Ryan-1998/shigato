# Property details, bills, depreciation and rental tax reports

## Intent

Properties cannot be edited after creation, and nothing in Shigoto holds
what a landlord needs at tax time. This adds a property page with Details,
Bills, Depreciation and Notes tabs, reached from a pencil icon beside each
property name, plus two end-of-year exports: an Accountant export and an
Item 21 (ATO rental schedule) report. The organisation has a single owner,
so no ownership-share split is applied.

## Goals

- Edit every property field after creation.
- Record purchase, build and reference details; keep an insurance policy
  history with documents.
- See each property's bills (existing expenses tagged to it) per financial
  year, and start a new one with the property preselected.
- Record surveyor-supplied depreciation items with per-financial-year
  amounts and supporting documents.
- One free-text notes page per property, edited over time.
- Accountant export and Item 21 report per financial year, one property or
  all, as CSV and a printable page.

## Non-goals

- Recharging bills to tenants (handled separately with existing lease
  charges).
- Calculating depreciation. Amounts are typed in as the surveyor's report,
  or the owner, states them.
- Ownership percentages, CGT calculations, property deletion, lease
  document uploads.
- The pencil icon on other screens (a separate later pass).

## Anti-goals

- Double counting: bills are existing expenses, never a new record type.
- Capital purchases counted twice: expense lines whose category is
  `capital` are never deductions in Item 21; the Accountant export lists
  them separately as "Capital (see depreciation)".
- Any change to BAS, reconciliation or GST behaviour.
- Exposure through the tenant portal.

## Constraints

- `docs/ui-standards.md`: complex editors are dedicated routes; short ones
  are dialogs; every editor registers in `web/src/lib/test/editors/`.
- Modules are toggleable. `reports` is core (always on) and never imports
  property/recon/timesheets; property may import `reports`. Property must
  not import `recon`.
- Development only: schema changes go in one new migration
  (`0018_property_details.sql`) with no data backfill beyond seeding
  category labels.

## Approaches

- **A (chosen): the property module owns it all.** Details, insurance,
  depreciation, notes, bills view and both rental reports live in
  `internal/property`, reading invoices/payments/expenses through
  `reports.IncomeRows` and its own SQL over the shared tables. Self-contained,
  respects module boundaries, no change to BAS or recon.
- B: a hook in `reports` that modules register report sections through.
  Rejected: new plumbing for one consumer.
- C: depreciation stored as expense lines in a "Depreciation" category.
  Rejected: non-cash rows leak into recon, BAS and GST.

## Decisions

### Data (migration `0018_property_details.sql`)

- `properties` gains `contract_date`, `settlement_date` (date),
  `purchase_price`, `stamp_duty`, `purchase_legal_costs` (bigint cents,
  >= 0), `year_built` (int), `lot_plan`, `land_area_m2` (numeric),
  `council_account_ref`, `water_account_ref` (text), `notes` (text, not
  null default '').
- `insurance_policies`: org_id, property_id, insurer, type
  (`building|landlord|strata_contents|other`), policy_number, start_date,
  renewal_date (>= start_date), sum_insured, premium, excess (cents, >= 0,
  nullable), file_id (nullable, references files).
- `depreciation_items`: org_id, property_id, name, type
  (`capital_works|plant_equipment`), installed_on (date), cost (nullable
  cents), notes, expense_id (nullable, references expenses).
- `depreciation_amounts`: item_id (cascade), fy (int, the financial year's
  end year, matching the reports' `FY2027` convention), amount (cents > 0),
  UNIQUE(item_id, fy).
- `depreciation_documents`: item_id (cascade), file_id, PK(item_id, file_id).
- `categories.item21_label` nullable text, checked against the label list
  below; seeded for the default categories by name.

### Item 21 labels

`advertising`, `body_corporate`, `borrowing`, `cleaning`, `council_rates`,
`capital_allowances`, `gardening`, `insurance`, `interest`, `land_tax`,
`legal`, `pest_control`, `agent_fees`, `repairs`, `capital_works`,
`stationery`, `water`, `sundry`. Expense categories may use any label except
`capital_allowances` and `capital_works`, which only depreciation feeds.
Seeded defaults: Repairs & maintenance → repairs, Insurance → insurance,
Council rates → council_rates, Water → water, Strata / body corporate →
body_corporate, Agent & advertising fees → agent_fees, Loan interest →
interest, Accounting & legal → legal, Bank fees → sundry. Others unmapped.

### Report rules

- Financial year follows the org's `fy_start_month`; named by end year
  (`FY2027` = 2026-07-01 to 2027-06-30 for July).
- **Rent is counted when received**: payments dated in the FY, allocated to
  property-tagged invoice lines via `reports.IncomeRows`, bonds excluded.
  Source `rent` → Gross rent; other sources → Other rental income. Ex-GST.
- **Expenses**: expense lines tagged to the property with `spent_on` in the
  FY, ex-GST (equal to the full amount for input-taxed residential lines).
  Capital-category lines are excluded from deductions. Unmapped categories
  roll into `sundry` and produce a warning naming the category.
- **Depreciation**: each item's amount for that FY, `plant_equipment` →
  capital_allowances, `capital_works` → capital_works.
- Amounts are 100% the owner's.

### Backend (property module, org-scoped)

- File storage (`File`, `SaveFile`, `GetFile`, sanitising, serving) moves
  from `recon` to `core`/`module`; recon routes behave as before.
- `PUT /properties/{id}`, `PUT /properties/{id}/notes`,
  `GET /properties/{id}` extended with details and read-only current tenancy.
- Insurance CRUD, depreciation item/amount/document CRUD, property-scoped
  file upload and a download that only serves files attached to that
  property's policies or depreciation items.
- `GET /properties/{id}/bills?fy=`, `GET /rental-reports/{accountant|item21}?fy=&property_id=&format=csv`.
- Dashboard: count of insurance policies renewing within 30 days.
- Reads require `property_read`; writes `property_write`; rental reports
  `reports_read`.

### UI

- Properties list: pencil icon link (`aria-label="Edit {name}"`) beside the
  name to `/[org]/properties/[id]`.
- `/[org]/properties/[id]` layout with header and tab links to Details,
  Bills, Depreciation, Notes (nested routes).
- Details: guarded page editor (Save/Cancel restores last saved), address
  "Open in Maps" Google Maps search link, read-only current tenancy with
  lease link, insurance policy list with a dialog editor and a "Renews in N
  days" badge within 30 days.
- Bills: FY selector, totals by category, table with receipt link; "Add
  bill" to `/expenses/new?property_id=` when reconciliation is enabled and
  the user has `recon_write`.
- Depreciation: FY selector and total, item list, dialog for item fields,
  yearly amounts and documents.
- Notes: single textarea, guarded page editor.
- Reports page: Rental section with FY/property pickers, Accountant and
  Item 21 printable views and CSV, and an Item 21 category-mapping dialog.

## Left to the builder

CSV column order beyond what the plan pins, print styling, and component
splits inside tabs.
