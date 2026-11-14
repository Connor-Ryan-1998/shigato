# Property details, bills, depreciation and rental reports Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task (native, no subagents, per the user). Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Editable property page with Details, Bills, Depreciation and Notes tabs, plus Accountant and Item 21 rental reports.

**Architecture:** Everything lives in the toggleable property module (`internal/property`, `web/src/lib/modules/property`). File storage moves to `core`/`module` so property can upload and serve documents without importing recon. Rental reports reuse `reports.IncomeRows` for cash-basis rent and query expense/depreciation tables directly.

**Tech Stack:** Go 1.x, pgx/Postgres, SvelteKit 5, Tailwind/DaisyUI, Vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-14-property-details-design.md`

## Global Constraints

- One new migration: `internal/db/migrations/0018_property_details.sql`. Do not edit earlier migrations.
- Money is integer cents (`core.Cents`) in Go and API; UI parses with `parseCents` from `web/src/lib/format.ts`.
- Dates on the wire are `YYYY-MM-DD` (`dateLayout`).
- FY is named by its end year and follows the org's `fy_start_month` (`FY2027` = 2026-07-01..2027-06-30 for month 7). API param `fy=2027`.
- Item 21 label values exactly: `advertising body_corporate borrowing cleaning council_rates capital_allowances gardening insurance interest land_tax legal pest_control agent_fees repairs capital_works stationery water sundry`.
- Property reads `property_read`, writes `property_write`; rental reports `reports_read`; category label edits keep recon's `recon_write`.
- `internal/property` must not import `internal/recon`; `internal/reports` must not import `internal/property`.
- No portal routes touch the new data.
- All validation via `uv run --locked task …`, run sequentially from `apps/shigoto`.

## Review Focus

1. A file ID belonging to another property, or an unattached upload, requested through `/properties/{id}/files/{fileId}` → 404, never the bytes.
2. Expense dated 30 June vs 1 July lands in the right FY in both bills and reports.
3. Capital-category expense line tagged to the property → absent from Item 21 deductions, present in the Accountant export capital section.
4. Payment received in FY for an invoice issued the previous FY → counted in that FY's gross rent; bond payments never counted.
5. Switching org or property while a tab is loading or saving → stale response ignored, dirty edits not silently lost.

---

### Task 1: Move file storage into core

**Files:**
- Create: `internal/core/files.go`, `internal/module/files.go`
- Modify: `internal/recon/files.go` (delete moved code; keep nothing that duplicates), `internal/recon/routes.go:507-620`, any recon/testkit/sandbox callers of `recon.SaveFile`/`recon.GetFile`
- Test: move existing file tests alongside (`internal/core`), keep `internal/httpapi/testdata/cases/recon.yaml` file cases passing unchanged

**Interfaces:**
- Produces: `core.File{ID, OrgID, SHA256, MIME, SizeBytes, OriginalName, CreatedAt}`, `(core.File).StoredPath(dir string) string`, `core.SaveFile(ctx, q, orgID, filesDir, name string, r io.Reader) (core.File, error)`, `core.GetFile(ctx, q, orgID, id) (core.File, error)`, `core.GetFileByHash`, `core.MaxFileBytes = 10 << 20`.
- Produces: `module.HandleUpload(w, r, d Deps, onSaved func(tx pgx.Tx, f core.File) error) (core.File, bool)` (multipart parse, 413/400/422 mapping, SaveFile + audit in one tx, writes the error response itself and returns false on failure), `module.ServeFile(w, f core.File, filesDir string)`, `module.FileResponse{ID, MIME, SizeBytes, OriginalName}` with JSON tags `id mime size_bytes original_name`.

- [ ] **Step 1:** Run `uv run --locked task test` and `uv run --locked task test SUITE=integration` to record the green baseline.
- [ ] **Step 2:** Move the code; recon's upload/download handlers become thin calls to `module.HandleUpload` (with `onSaved` nil) and `module.ServeFile`. Default name stays `"receipt"`.
- [ ] **Step 3:** Run both suites again. Expected: PASS with identical recon file case results.
- [ ] **Step 4:** Commit `refactor: move file storage into core`.

### Task 2: Migration and editable property details

**Files:**
- Create: `internal/db/migrations/0018_property_details.sql`, `internal/core/item21.go`
- Modify: `internal/property/model.go`, `internal/property/store.go`, `internal/property/routes.go`, `internal/core/orgs.go` (seeded categories carry labels), `internal/testkit/records.go` (PropertyOption for details if needed)
- Test: `internal/property/store_integration_test.go`, `internal/httpapi/testdata/cases/property.yaml`

**Interfaces:**
- Produces: migration with every table/column in the spec's Data section, including `categories.item21_label` CHECK against the label list and UPDATE-by-name seeding.
- Produces: `core.Item21Label` (string type), `core.Item21Labels []Item21Label` in display order, `core.ParseItem21Label(s string) (Item21Label, error)`, `core.Item21Title(l) string` (e.g. `council_rates` → "Council rates").
- Produces: `property.Property` gains `ContractDate, SettlementDate *time.Time; PurchasePrice, StampDuty, PurchaseLegalCosts *core.Cents; YearBuilt *int; LotPlan, CouncilAccountRef, WaterAccountRef string; LandAreaM2 *float64; Notes string`.
- Produces: `property.UpdateProperty(ctx, q, orgID int64, p Property) (Property, error)` (all fields except Notes; name required; money >= 0; year_built 1800..current year), `property.UpdateNotes(ctx, q, orgID, id int64, notes string) error`, `property.CurrentTenancy(ctx, q, orgID, propertyID int64, today time.Time) (*Tenancy, error)` with `Tenancy{LeaseID int64; Rent core.Cents; Frequency Frequency; PrimaryTenantID int64}` (lease active on today, latest rent change effective on or before today; nil when none).
- Produces routes: `PUT /api/v1/orgs/{org}/properties/{id}` (strict full payload, same JSON names as the columns, audit `property update`), `PUT /api/v1/orgs/{org}/properties/{id}/notes` body `{notes}`. `GET /properties/{id}` response adds all detail fields plus `notes` and `current_tenancy: {lease_id, rent, frequency, primary_tenant_id, primary_tenant_name} | null`. List response unchanged.

- [ ] **Step 1: Failing tests.** Integration: `TestUpdatePropertyRoundTrips` (set every field, Get returns them; clearing optional fields with null returns nil), `TestUpdatePropertyRejectsNegativeMoney` (`ErrInvalidInput`), `TestUpdatePropertyCrossOrg` (`ErrNotFound`), `TestCurrentTenancy` (terminated lease → nil; active lease with rent change dated after today → older amount). HTTP YAML: bookkeeper `PUT /properties/{p}` → 403 exact; foreign org → 404 exact with `unchanged: [properties]`; owner PUT with `isolate: true` → 200 and body `{name: Renamed}`. (Category label seeding is asserted in Task 5.)
- [ ] **Step 2:** Run `uv run --locked task test SUITE=integration`. Expected: FAIL (UpdateProperty undefined).
- [ ] **Step 3:** Write migration, `core/item21.go`, store and routes.
- [ ] **Step 4:** Run unit + integration suites. Expected: PASS.
- [ ] **Step 5:** Commit `feat(property): editable property details and notes`.

### Task 3: Insurance policies, property documents, dashboard renewals

**Files:**
- Create: `internal/property/insurance.go`, `internal/property/documents.go`
- Modify: `internal/property/routes.go` (routes + `propertyDashboard.InsuranceRenewingSoon int json:"insurance_renewing_soon"`), `web/src/lib/modules/property/DashboardCard.svelte`
- Test: `internal/property/insurance_integration_test.go`, `internal/httpapi/testdata/cases/property.yaml`

**Interfaces:**
- Produces: `property.InsurancePolicy{ID, PropertyID int64; Insurer string; Type string; PolicyNumber string; StartDate, RenewalDate time.Time; SumInsured, Premium, Excess *core.Cents; FileID *int64}`; `CreateInsurancePolicy`, `UpdateInsurancePolicy`, `DeleteInsurancePolicy`, `ListInsurancePolicies(ctx, q, orgID, propertyID)` ordered by renewal_date desc. Type must be `building|landlord|strata_contents|other`; renewal >= start; FileID must exist in org.
- Produces: `property.FileAttachedToProperty(ctx, q, orgID, propertyID, fileID int64) (bool, error)` checking insurance_policies and depreciation_documents (the latter table exists from Task 2's migration).
- Produces routes: `GET|POST /properties/{id}/insurance`, `PUT|DELETE /properties/{id}/insurance/{policyId}`, `POST /properties/{id}/files` (property_write; `module.HandleUpload`; returns `module.FileResponse`), `GET /properties/{id}/files/{fileId}` (property_read; 404 unless attached). Policy JSON: `id insurer type policy_number start_date renewal_date sum_insured premium excess file_id file_name`.
- Produces: dashboard counts policies, latest per property, whose `renewal_date` is within [today, today+30].

- [ ] **Step 1: Failing tests.** `TestInsuranceCRUD`, `TestInsuranceRejectsRenewalBeforeStart`, `TestPropertyFileDownloadRequiresAttachment` (HTTP: upload via `POST /properties/{a}/files`, GET before attach → 404; attach to policy, GET → 200; same file via property b → 404), `TestDashboardInsuranceRenewingSoon` (renewal today+30 counts, today+31 doesn't, superseded older policy doesn't). YAML: bookkeeper POST insurance → 403 exact.
- [ ] **Step 2:** Run integration suite. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run suites. Expected: PASS.
- [ ] **Step 5:** Commit `feat(property): insurance policies and property documents`.

### Task 4: Depreciation items

**Files:**
- Create: `internal/property/depreciation.go`
- Modify: `internal/property/routes.go`
- Test: `internal/property/depreciation_integration_test.go`, `internal/httpapi/testdata/cases/property.yaml`

**Interfaces:**
- Produces: `property.DepreciationItem{ID, PropertyID int64; Name, Type, Notes string; InstalledOn time.Time; Cost *core.Cents; ExpenseID *int64; Amounts []DepreciationAmount; Documents []core.File}`, `DepreciationAmount{FY int; Amount core.Cents}`.
- Produces: `CreateDepreciationItem`, `UpdateDepreciationItem` (replaces Amounts and Documents sets in the same tx), `DeleteDepreciationItem`, `ListDepreciationItems(ctx, q, orgID, propertyID)`. Type `capital_works|plant_equipment`; FY unique per item; amount > 0; ExpenseID and document file IDs must belong to org.
- Produces routes: `GET|POST /properties/{id}/depreciation`, `PUT|DELETE /properties/{id}/depreciation/{itemId}`. JSON: `id name type installed_on cost notes expense_id amounts:[{fy, amount}] documents:[{id, original_name}]`; request takes `document_ids: []`.

- [ ] **Step 1: Failing tests.** `TestDepreciationItemRoundTrip`, `TestDepreciationRejectsDuplicateFY`, `TestDepreciationUpdateReplacesAmountsAndDocuments`, `TestDepreciationCrossOrgExpenseRejected`. YAML: bookkeeper write → 403 exact; foreign property → 404.
- [ ] **Step 2:** Run integration. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run suites. Expected: PASS.
- [ ] **Step 5:** Commit `feat(property): depreciation schedule`.

### Task 5: Item 21 label on categories

**Files:**
- Modify: `internal/recon/categories.go`, `internal/recon/routes.go` (category request/response), `internal/core/orgs.go` default seeds
- Test: `internal/recon/*_integration_test.go`, `internal/httpapi/testdata/cases/recon.yaml`

**Interfaces:**
- Produces: `recon.Category.Item21Label *core.Item21Label`; category JSON `item21_label` (null or label). PUT stays strict and requires the field present (null allowed). Rejects `capital_allowances`/`capital_works` and unknown labels with 422.
- Produces: new orgs get the same default labels as the migration's seeding.

- [ ] **Step 1: Failing tests.** `TestNewOrgCategoriesCarryItem21Labels` (Council rates → council_rates, Equipment → nil), YAML: PUT with `item21_label: capital_works` → 422; with `water` → 200.
- [ ] **Step 2:** Run suites. Expected: FAIL.
- [ ] **Step 3:** Implement; update the web `Category` type in `ExpenseForm.svelte` with optional `item21_label`.
- [ ] **Step 4:** Run `task test`, integration, `task web:test`. Expected: PASS.
- [ ] **Step 5:** Commit `feat(recon): item 21 label on expense categories`.

### Task 6: Bills view and rental reports API

**Files:**
- Create: `internal/property/rental.go`, `internal/property/testdata/item21.yaml` (table cases for the pure rollup)
- Modify: `internal/reports/periods.go` (export FY range), `internal/reports/routes.go` (export org FY month reader), `internal/property/routes.go`
- Test: `internal/property/rental_test.go`, `internal/property/rental_integration_test.go`, `internal/httpapi/testdata/cases/property.yaml`

**Interfaces:**
- Consumes: `reports.IncomeRows`, `core.Item21Labels`, Task 4 `ListDepreciationItems`.
- Produces: `reports.FinancialYear(fyEndYear, fyStartMonth int) (from, to time.Time, err error)`, `reports.OrgFYStartMonth(ctx, q, orgID) (int, error)`.
- Produces: `property.BillLine{ExpenseID, LineID int64; SpentOn time.Time; Supplier, Description, Category string; Capital bool; Label *core.Item21Label; Code core.GSTCode; Incl, GST, Ex core.Cents; ReceiptFileID *int64}`; `property.Bills(ctx, q, orgID int64, propertyID *int64, from, to time.Time) ([]BillLine, error)` (nil propertyID = all properties; each line carries PropertyID).
- Produces: pure `property.Item21(income []reports.IncomeRow, bills []BillLine, dep []DepreciationItem, fy int) Item21Result` with `Item21Result{GrossRent, OtherIncome core.Cents; Deductions map[core.Item21Label]core.Cents; Warnings []string}`; capital lines skipped; nil label → sundry plus warning `"Category \"X\" has no Item 21 label; counted as Sundry"` (once per category).
- Produces routes (`reports_read` for reports, `property_read` for bills): `GET /properties/{id}/bills?fy=2027` → `{fy, from, to, lines:[…], totals:{category: ex}}`; `GET /rental-reports/item21?fy=&property_id=` → `{fy, properties:[{property_id, name, gross_rent, other_income, deductions:{label: cents}, total_deductions, net, warnings}]}`; `GET /rental-reports/accountant?fy=&property_id=` → per property `{income:[rows], expenses:[bill lines non-capital], capital:[bill lines capital], depreciation:[{item, type, amount}]}`. `format=csv` returns `text/csv` attachment `rental-<kind>-FY<fy>.csv`. Accountant CSV columns: `property,section,date,category,item21_label,description,supplier,incl_gst,gst,ex_gst,receipt`; Item 21 CSV: `property,label,amount`. Amounts formatted as dollars `1234.56`.

- [ ] **Step 1: Failing tests.** YAML table for `Item21`: capital line excluded; unmapped → sundry + one warning; plant_equipment → capital_allowances; FY mismatch amount ignored; rent vs manual source split. Integration `TestBillsFYBoundary` (30 Jun in FY2026, 1 Jul in FY2027 with month 7), `TestItem21CashBasisRent` (invoice issued FY2026, paid FY2027 → FY2027 gross rent; bond paid in FY2027 → excluded), `TestRentalReportCSV` (header row exact). YAML: tenant role → 403 exact on `/rental-reports/item21`; property_id from foreign org → 404.
- [ ] **Step 2:** Run `task test` and integration. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run suites. Expected: PASS.
- [ ] **Step 5:** Commit `feat(property): bills view and rental tax reports`.

### Task 7: Property page shell, Details tab and insurance dialog

**Files:**
- Create: `web/src/routes/[org]/properties/[id]/+layout.svelte`, `web/src/routes/[org]/properties/[id]/+page.svelte`, `web/src/lib/modules/property/PropertyDetailsForm.svelte`, `web/src/lib/modules/property/InsuranceDialog.svelte`, `web/src/lib/modules/property/types.ts`, `web/src/lib/test/editors/property-details.yaml`, `web/src/lib/test/editors/insurance-dialog.yaml`
- Modify: `web/src/routes/[org]/properties/+page.svelte` (pencil link), `web/src/lib/modules/property/DashboardCard.svelte`
- Test: `web/src/routes/[org]/properties/[id]/+page.svelte.test.ts`, `web/src/routes/[org]/properties/+page.test.ts`

**Interfaces:**
- Consumes: Task 2/3 API shapes.
- Produces: layout renders `PageHeader` (title = property name, back to `/[org]/properties`) and `<nav aria-label="Property sections" class="tabs tabs-border">` links `Details`, `Bills`, `Depreciation`, `Notes` with `aria-current="page"` on the active one. Layout does not own data; each tab loads what it needs.
- Produces: `types.ts` exports `PropertyDetail`, `InsurancePolicy`, `DepreciationItem`, `Item21Label` matching the JSON above.
- Produces: `PropertyDetailsForm({ property: PropertyDetail, busy, error, onsubmit(payload), onchange })`; `InsuranceDialog({ open, org, propertyId, policy?: InsurancePolicy | null, onsaved })` with optional document upload through `POST /properties/{id}/files`.
- Pencil: inline SVG pencil-square icon in an `<a class="btn btn-ghost btn-xs" aria-label="Edit {name}">` placed after the name via a `nameCell` snippet.
- Maps link: `https://www.google.com/maps/search/?api=1&query=${encodeURIComponent(address)}`, `target="_blank" rel="noopener noreferrer"`, hidden when address is empty.
- Renewal badge: `StatusBadge` "Renews in N days" (warning tone) when 0 ≤ N ≤ 30, "Expired" (error) when N < 0 for the latest policy.

- [ ] **Step 1: Failing tests.** List test: pencil link `getByRole('link', {name: 'Edit Cottage'})` has href `/acme/properties/1`. Details page tests: loads and shows current tenancy link to `/acme/leases/7`; no tenancy shows "No active lease"; bookkeeper sees read-only fields and no Save; org switch mid-load ignores the stale response; Maps link href encodes the address. Editor YAMLs: `property-details` (page editor, mirror `settings-page.yaml` skips, `request: PUT /api/v1/orgs/acme/properties/1`) and `insurance-dialog` (dialog contract, `request: POST /api/v1/orgs/acme/properties/1/insurance`).
- [ ] **Step 2:** Run `uv run --locked task web:test`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `task web:test` and `task web:check`. Expected: PASS.
- [ ] **Step 5:** Commit `feat(web): property details page with insurance`.

### Task 8: Bills tab

**Files:**
- Create: `web/src/routes/[org]/properties/[id]/bills/+page.svelte`, `web/src/lib/modules/property/fy.ts`
- Modify: `web/src/routes/[org]/expenses/new/+page.svelte`, `web/src/lib/modules/reconciliation/ExpenseForm.svelte` (optional `initialPropertyId?: string` seeds the first row's property)
- Test: `web/src/routes/[org]/properties/[id]/bills/+page.svelte.test.ts`, `web/src/lib/modules/property/fy.test.ts`, `web/src/lib/test/editors/expense-new.yaml` unchanged and passing

**Interfaces:**
- Produces: `fy.ts` `currentFY(today: string, startMonth = 7): number`, `fyOptions(current: number, back = 5): {value: string; label: string}[]` labelled `FY2026–27`. FY is kept in the URL as `?fy=2027`.
- Bills page: `FilterBar` with FY select; category totals as a small summary table; `DataTable` columns Date, Supplier, Description, Category, Ex GST, GST, Total (right-aligned), Receipt (link to `/api/v1/orgs/{org}/files/{id}` only when the user has `reports_read`), plus a link to `/[org]/expenses/{expense_id}`. Capital lines show a "Capital" badge. "Add bill" link `/[org]/expenses/new?property_id={id}` only when `session.modules` includes `reconciliation` and perms include `recon_write`.

- [ ] **Step 1: Failing tests.** `currentFY('2026-06-30') === 2026`, `currentFY('2026-07-01') === 2027`. Bills page: requests `/properties/1/bills?fy=2027`; changing FY updates URL and refetches; "Add bill" hidden without recon module; empty FY shows the empty state, not a table; expenses/new with `?property_id=3` preselects Cottage on the first line.
- [ ] **Step 2:** Run `task web:test`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `task web:test`, `task web:check`. Expected: PASS.
- [ ] **Step 5:** Commit `feat(web): property bills tab`.

### Task 9: Depreciation and Notes tabs

**Files:**
- Create: `web/src/routes/[org]/properties/[id]/depreciation/+page.svelte`, `web/src/lib/modules/property/DepreciationItemDialog.svelte`, `web/src/routes/[org]/properties/[id]/notes/+page.svelte`, `web/src/lib/test/editors/depreciation-item-dialog.yaml`, `web/src/lib/test/editors/property-notes.yaml`
- Test: colocated `+page.svelte.test.ts` for both routes

**Interfaces:**
- Consumes: Task 4 API, `fy.ts`.
- Produces: `DepreciationItemDialog({ open, org, propertyId, item?: DepreciationItem | null, onsaved })`: fields Name, Type (Capital works / Plant & equipment), Installed on, Cost, Notes; editable rows of FY + Amount (add/remove row); document upload list (upload via `POST /properties/{id}/files`, remove from list). Duplicate FY rows block submit with an inline error.
- Depreciation page: FY filter; summary "FY2026–27 total" split into Capital works and Plant & equipment; item table with Name, Type, Installed, This FY amount, Documents count, Edit button; New item button (write perm); Delete inside the dialog with confirm.
- Notes page: textarea labelled "Notes", Save notes / Cancel (restores last saved), `useUnsavedChanges`, `PUT /properties/{id}/notes`.

- [ ] **Step 1: Failing tests.** Depreciation: FY total sums only matching-FY amounts; bookkeeper sees no New/Edit. Dialog YAML contract with `request: POST /api/v1/orgs/acme/properties/1/depreciation`; dialog test rejects duplicate FY rows without a request. Notes YAML page contract (`request: PUT /api/v1/orgs/acme/properties/1/notes`).
- [ ] **Step 2:** Run `task web:test`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `task web:test`, `task web:check`. Expected: PASS.
- [ ] **Step 5:** Commit `feat(web): property depreciation and notes tabs`.

### Task 10: Rental reports UI, ledger and end-to-end

**Files:**
- Create: `web/src/routes/[org]/reports/rental/+page.svelte` (printable view), `web/src/lib/modules/property/Item21MappingDialog.svelte`, `web/src/lib/test/editors/item21-mapping.yaml`
- Modify: `web/src/routes/[org]/reports/+page.svelte` (Rental section, shown when property module enabled), `docs/ui-standards.md` ledger, `web/e2e/financial-workflows.spec.ts`, `README.md` (one paragraph on rental reports)
- Test: `web/src/routes/[org]/reports/rental/+page.svelte.test.ts`, `web/src/routes/[org]/reports/+page.test.ts`

**Interfaces:**
- Consumes: Task 5 categories API, Task 6 report API.
- Produces: Reports page Rental section: FY select, property select (All + each), links "Accountant view" / "Item 21 view" to `/[org]/reports/rental?kind=accountant|item21&fy=&property_id=`, CSV download links to the API with `format=csv`, and "Item 21 labels" button opening the mapping dialog (only with `recon_write` and reconciliation enabled). Property Bills and Depreciation tabs link to the rental view filtered to that property.
- Produces: rental page renders warnings first (with a link to open the labels dialog), then the report tables using `Money`, plus a `Print` button calling `window.print()`; `print:hidden` on app chrome controls.
- Produces: `Item21MappingDialog({ open, org, onsaved })` lists expense categories with a label select (excluding the two depreciation-only labels) and saves changed rows with strict `PUT /categories/{id}` payloads.

- [ ] **Step 1: Failing tests.** Rental page renders the "Council rates" row and a sundry warning from a mocked response; Reports page hides the Rental section without the property module; mapping dialog YAML contract. E2E: create property, open via pencil, edit purchase price, save, reload persists; open Item 21 view for current FY.
- [ ] **Step 2:** Run `task web:test`. Expected: FAIL.
- [ ] **Step 3:** Implement; add ledger rows for `[org]/properties/[id]/+page.svelte`, `bills`, `depreciation`, `notes`, `[org]/reports/rental/+page.svelte`, and update the properties list row ("pencil edit link").
- [ ] **Step 4:** Run sequentially: `uv run --locked task test`, `uv run --locked task test SUITE=integration`, `uv run --locked task web:test`, `uv run --locked task web:check`, `uv run --locked task lint`, then the e2e suite per README. Expected: all PASS.
- [ ] **Step 5:** Commit `feat(web): rental tax reports`.
