# Finance Flow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task (user chose native, inline execution — no subagents). Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split billing runs by project/task from a dialog on Invoices, add write-off (uncollectible) status changes, and group money screens under a Finance sidebar section.

**Architecture:** Backend changes stay in their owning packages: invoice state in `internal/core` + `internal/billing`, provider calls in `internal/payments`, reconciliation rules in `internal/recon`, billing runs in `internal/timesheets`. The billing-run page becomes `BillingRunDialog` in the timesheets web module, opened from the core Invoices page. Nav items gain a display `group` independent of their module.

**Tech Stack:** Go 1.x + pgx + Postgres, stripe-go v87, SvelteKit 5 (runes) + Tailwind + DaisyUI, Vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-14-finance-flow-design.md`

## Global Constraints

- Development only: no data migration or legacy-compat work. New schema goes in a new migration file `internal/db/migrations/0017_invoice_written_off.sql`.
- Native, inline implementation. No subagents.
- Status spelling is `uncollectible` everywhere (schema, API, UI label "Uncollectible").
- No invoice content edits, no Open → Draft, no voiding invoices with any payment, no manual-payment deletion, no refunds/credit notes.
- Automatic reconciliation (`Suggest`, `autoMatchCandidates`, `AutoMatch`) keeps matching `status = 'open'` only.
- Every new editor: failed-input preservation, duplicate-submit protection, explicit cancel, dirty/busy dismissal guards (editor contract YAML).
- Keep API calls on `lib/api.ts`'s `api()`; org/seq guards as on existing pages.
- Run validation sequentially via `uv run --locked task ...` from `apps/shigoto`. Never use production/operator databases.

## Review Focus

1. **Retrying a billing run whose response was lost after commit** → must not create a second invoice. Selected entries are now billed, so the stale check returns 409. Pinned in Task 4 (`TestRunBillingRetryAfterCommitIs409`).
2. **Search hides a ticked task, then the user unticks its project** → the hidden task is cleared too, and the total drops. Pinned in Task 5 (`billingSelection.test.ts`, "project toggle applies to hidden tasks").
3. **Written-off invoice part-paid by bank match, then unmatched** → returns to Uncollectible, not Open. Pinned in Task 1 (`TestUnmatchRestoresUncollectible`).
4. **Per-project run where issuing one invoice fails** → every draft still exists; the response lists each invoice and its own issue error. Pinned in Task 4 (`TestRunBillingPerProjectIssueFailureReportsEach`).
5. **Mark uncollectible on an invoice already paid at Stripe, or a Stripe API failure** → no local change; returns 502. Pinned in Task 2 (`TestMarkUncollectibleProviderFailureRollsBack`).

---

### Task 1: Invoice collection state in core and recon

**Files:**
- Create: `internal/db/migrations/0017_invoice_written_off.sql`
- Modify: `internal/core/invoices.go` (Invoice struct, `invoiceColumns`, `scanInvoice`, `VoidInvoice`, new funcs)
- Modify: `internal/recon/match.go:117-127` (`MatchInvoice` status check), `:351-375` (`Unmatch` reopen)
- Test: `internal/core/invoices_integration_test.go`, `internal/recon/match_integration_test.go`

**Interfaces:**
- Produces:
  - `core.Invoice.WrittenOff bool`
  - `func MarkInvoiceUncollectible(ctx context.Context, tx Querier, orgID, id int64) (Invoice, error)`: open → uncollectible, sets `written_off = true`. Any other status returns `ErrInvalidState`; a missing invoice returns `ErrNotFound`. Locks the row `FOR UPDATE`.
  - `func ReinstateInvoice(ctx context.Context, tx Querier, orgID, id int64) (Invoice, error)`: uncollectible → open, sets `written_off = false`. Same error rules.
  - `VoidInvoice` accepts `draft|open|uncollectible`. Payment guard unchanged.

- [ ] **Step 1: Migration**

```sql
ALTER TABLE invoices ADD COLUMN written_off boolean NOT NULL DEFAULT false;
```

- [ ] **Step 2: Write failing core tests** in `invoices_integration_test.go`:
  - `TestMarkUncollectibleAndReinstate`: issued invoice → Mark → `Status == "uncollectible" && WrittenOff`. Reinstate → `Status == "open" && !WrittenOff`.
  - `TestMarkUncollectibleRejectsNonOpen`: draft, paid and void each return `errors.Is(err, core.ErrInvalidState)`. Reinstating an open invoice returns `ErrInvalidState`.
  - `TestVoidUncollectible`: an uncollectible invoice with no payments voids to `"void"`. With one payment it returns `core.ErrInvoiceHasPayments`.
  - `TestUncollectiblePaymentMarksPaid`: `RecordPayment` covering the total on an uncollectible invoice sets `"paid"`, and `WrittenOff` stays `true`.

- [ ] **Step 3: Run tests to verify they fail**

Run: `uv run --locked task test SUITE=integration PACKAGES=./internal/core/...`
Expected: FAIL (undefined `MarkInvoiceUncollectible`)

- [ ] **Step 4: Implement** the struct field, column scan, the two funcs and the `VoidInvoice` status set in `internal/core/invoices.go`.

- [ ] **Step 5: Write failing recon tests** in `match_integration_test.go`:
  - `TestMatchInvoiceAcceptsUncollectible`: a manual `MatchInvoice` against an uncollectible invoice records the payment.
  - `TestUnmatchRestoresUncollectible`: uncollectible invoice, total 10000. A full bank match makes it `paid`. `Unmatch` returns it to `"uncollectible"`. Control case: an invoice paid from open returns to `"open"`.
  - `TestAutoMatchSkipsUncollectible`: a bank line whose description has the reference of an uncollectible invoice with an equal amount → `AutoMatch` returns 0 and `Suggest` returns no invoice suggestion.

- [ ] **Step 6: Implement**
  - `MatchInvoice` allows `open` or `uncollectible`.
  - `Unmatch` sets `status = CASE WHEN written_off THEN 'uncollectible' ELSE 'open' END`.
  - Keep the audit key `reopened_invoice_ids` as is, and update its comment to say "reopened or restored to uncollectible".

- [ ] **Step 7: Run both packages**

Run: `uv run --locked task test SUITE=integration PACKAGES="./internal/core/... ./internal/recon/..."`
Expected: PASS

- [ ] **Step 8: Commit** `feat(invoices): persist write-off state and restore it on unmatch`

---

### Task 2: Status-change service, provider and routes

**Files:**
- Modify: `internal/payments/provider.go` (interface), `internal/payments/fake.go`, `internal/payments/stripe/stripe.go`
- Modify: `internal/billing/billing.go` (new methods)
- Modify: `internal/billing/invoice_response.go` (expose `written_off`)
- Modify: `internal/httpapi/core_routes.go` (2 routes + handlers)
- Test: `internal/billing/billing_integration_test.go`, `internal/payments/stripe/stripe_test.go`, `internal/httpapi/testdata/cases/invoices.yaml`

**Interfaces:**
- Consumes: `core.MarkInvoiceUncollectible`, `core.ReinstateInvoice` (Task 1)
- Produces:
  - `Provider.MarkUncollectible(ctx context.Context, ref string) error`. Stripe: `s.client.V1Invoices.MarkUncollectible(ctx, ref, nil)`, wrapped as `"stripe: marking invoice %s uncollectible: %w"`. Fake: a `MarkUncollectibleErr` one-shot plus a `MarkedUncollectibleRefs() []string` accessor.
  - `var ErrProviderStatusFailed = errors.New("status change failed with payment provider")` in billing.
  - `func (s *Service) MarkUncollectible(ctx context.Context, orgID int64, orgSlug string, invoiceID int64, reason string) (core.Invoice, error)`: detached ctx; one transaction does the core call, audit `"mark_uncollectible"` with `{"reason": reason}`, then the provider call if `ProviderRef != ""`. A provider error rolls back and is wrapped with `ErrProviderStatusFailed`. Same shape as `Void`.
  - `func (s *Service) Reinstate(ctx, orgID int64, invoiceID int64, reason string) (core.Invoice, error)`: local only, audit `"reinstate"` with `{"reason": reason}`. **Stripe decision:** Stripe has no inverse of mark-uncollectible, and Stripe invoices stay payable while uncollectible. So reinstating leaves the provider invoice and link unchanged, and `invoice.paid` webhooks still record payment. Document this in the doc comment.
  - `InvoiceResponse.WrittenOff bool \`json:"written_off"\``
  - Routes, both `PermInvoicesWrite`, body `{"reason": string}`. A trimmed empty reason or more than 500 chars returns 422 `"reason is required"` / `"reason must be at most 500 characters"`. 502 `"failed to update invoice with payment provider"` on `ErrProviderStatusFailed`. Success returns 200 with the invoice JSON.
    - `POST /api/v1/orgs/{org}/invoices/{id}/uncollectible`
    - `POST /api/v1/orgs/{org}/invoices/{id}/reinstate`
  - `billing.Void` already uses `core.VoidInvoice`, so uncollectible void works with no change. Stripe allows voiding uncollectible invoices.

- [ ] **Step 1: Failing tests** in `billing_integration_test.go`:
  - `TestMarkUncollectibleCallsProvider`: issued and sent (fake). Mark → `fake.MarkedUncollectibleRefs()` contains `ProviderRef`, and an audit row `mark_uncollectible` has the reason.
  - `TestMarkUncollectibleProviderFailureRollsBack`: `MarkUncollectibleErr` set → `errors.Is(err, billing.ErrProviderStatusFailed)`, the invoice is still `"open"`, and there is no audit row.
  - `TestReinstateIsLocalOnly`: after Mark, Reinstate → `"open"`, no further provider calls, `ProviderRef` unchanged.
  - `TestUncollectibleWebhookPaymentMarksPaid`: a webhook `invoice.paid` against an uncollectible invoice → `"paid"` (reuse the webhook test helpers in `internal/httpapi/webhooks_integration_test.go` if simpler. Put it there.)
- [ ] **Step 2:** Add a Stripe unit test `TestMarkUncollectibleHitsEndpoint` in `stripe_test.go`, using the existing httptest Stripe stub: it asserts `POST /v1/invoices/in_1/mark_uncollectible`.
- [ ] **Step 3: Run tests to verify they fail**

Run: `uv run --locked task test SUITE=integration PACKAGES="./internal/billing/... ./internal/httpapi/... ./internal/payments/..."`
Expected: FAIL to compile.

- [ ] **Step 4: Implement** the provider method (interface, fake, stripe), the billing methods, the response field, and the routes and handlers.
- [ ] **Step 5: HTTP cases** appended to `invoices.yaml`. Add an `issued_acme: {org: acme, contact: c_acme, status: issued, amount: 10000}` seed, and a `keeper: {org: acme, role: bookkeeper}` user if bookkeepers lack `invoices_write`. Check `internal/auth` perms first; otherwise use a role without it.
  - Writing off a foreign invoice → 404 `{error: not found}` exact, `unchanged: [invoices, audit_log]`
  - Missing reason → 422 `{error: "reason is required"}`, unchanged
  - Writing off a draft (`rent_acme`) → 409, unchanged
  - Reinstating an open invoice → 409, unchanged
  - Role without invoices_write → 403 exact, unchanged
  - Owner writes off an issued invoice → 200 `{status: uncollectible, written_off: true}`, `isolate: true`
- [ ] **Step 6: Run** the same command as step 3. Expected: PASS. Then `uv run --locked task test` (unit). Expected: PASS.
- [ ] **Step 7: Commit** `feat(invoices): mark uncollectible and reinstate endpoints`

---

### Task 3: Invoice status UI

**Files:**
- Create: `web/src/lib/modules/core/invoices/InvoiceStatusDialog.svelte`
- Modify: `web/src/routes/[org]/invoices/+page.svelte` (TABS), `web/src/lib/modules/core/invoices/InvoiceTable.svelte` (tone), `web/src/routes/[org]/invoices/[id]/+page.svelte`
- Create: `web/src/lib/test/editors/invoice-uncollectible.yaml`
- Create: `web/src/routes/[org]/invoices/[id]/+page.test.ts`
- Modify: `docs/ui-standards.md` ledger row for `[org]/invoices/[id]` (add dialog + tests)

**Interfaces:**
- Consumes: the endpoints from Task 2, and `written_off` on the detail JSON.
- Produces: `InvoiceStatusDialog` props `{ open: boolean (bindable); title: string; submitLabel: string; description: string; busy: boolean; error: string; onSubmit: (reason: string) => void }`. It mirrors `PaymentDialog`: the page owns the request and the dialog stays open on error.

- [ ] **Step 1: Failing page tests** (`+page.test.ts` using `mockApi`, `setPage`, `signIn`):
  - open invoice: shows "Mark uncollectible", not "Reinstate".
  - uncollectible invoice with `amount_paid: 0`: shows "Reinstate", "Record payment" and "Void". With `amount_paid > 0` it hides "Void".
  - Submitting the write-off posts `POST /api/v1/orgs/acme/invoices/7/uncollectible` with `{reason: "Client insolvent"}`, then refetches.
  - Reinstate dialog text contains "The Stripe payment link stays active" when `provider_url !== ''`.
  - Record payment on an uncollectible invoice passes `outstandingCents = total - amount_paid` (the API reports outstanding 0 for closed invoices).
- [ ] **Step 2: Editor contract** `invoice-uncollectible.yaml`, modelled on `invoice-payment.yaml`:
  - `open: Mark uncollectible`, `fill: {Reason: Client insolvent}`, `submit: Mark uncollectible`, `request: POST /api/v1/orgs/acme/invoices/7/uncollectible`, `preserves_input_on: 409`
  - all six contract keys true / Cancel
- [ ] **Step 3: Run** `uv run --locked task web:test`. Expected: the new tests FAIL.
- [ ] **Step 4: Implement**
  - Tabs: add `{ label: 'Uncollectible', value: 'uncollectible' }` after Paid.
  - Tone `uncollectible: 'danger'` in both STATUS_TONE maps.
  - Detail actions:
    - open adds "Mark uncollectible".
    - uncollectible shows Record payment, Reinstate, and Void (when `amount_paid === 0`).
  - Reason textarea labelled "Reason", required, `maxlength=500`.
  - Void keeps its `window.confirm`; it's out of scope to restyle.
- [ ] **Step 5: Run** `uv run --locked task web:test`. Expected: PASS.
- [ ] **Step 6: Commit** `feat(web): write off and reinstate invoices`

---

### Task 4: Billing preview and split runs (backend)

**Files:**
- Modify: `internal/timesheets/billrun.go`, `internal/timesheets/routes.go:175-178, 809-894`
- Test: `internal/timesheets/billrun_integration_test.go`, `internal/httpapi/testdata/cases/timesheets.yaml`

**Interfaces:**
- Produces (Go):
  ```go
  type PreviewInput struct { ClientContactID int64; From, To time.Time }
  type PreviewTask struct { TaskID int64; TaskName string; Minutes int; Amount, GSTAmount core.Cents }
  type PreviewProject struct { ProjectID int64; ProjectName string; Tasks []PreviewTask } // tasks sorted by name
  type Preview struct { Projects []PreviewProject } // projects sorted by name
  func PreviewBilling(ctx context.Context, q core.Querier, orgID int64, in PreviewInput) (Preview, error)

  type TaskSelection struct { TaskID int64; Minutes int; Amount core.Cents }
  // RunInput gains:
  //   Split string            // "combined" | "project"
  //   Tasks []TaskSelection   // required, non-empty, unique TaskIDs
  type RunInvoice struct { Invoice core.Invoice; IssueErr error }
  type RunResult struct { Invoices []RunInvoice; EntryCount, TotalMinutes int }
  var ErrPreviewChanged = &core.PublicError{Err: core.ErrInvalidState, Msg: "billing preview changed; refresh and review"}
  ```
  - `Amount` is `LineFor(...).UnitAmount`. `GSTAmount` is `core.GSTOnExclusive(amount, code)` with the code `LineFor` chose, so preview equals the invoice to the cent.
  - Preview and run validate the client the same way (`ErrInvalidInput` for from>to, a missing or non-client contact). The run also rejects `Split` not in the set, empty `Tasks`, and duplicate task IDs (`ErrInvalidInput`).
  - Refactor `lockUnbilledEntries` into `unbilledGroups(ctx, q core.Querier, orgID int64, clientID int64, from, to time.Time, taskIDs []int64, lock bool) ([]taskGroup, error)`. A nil `taskIDs` means all tasks. `lock` appends `FOR UPDATE`.
  - Run: lock the selected groups, then compare each selection with its group by `Minutes` and `LineFor` amount. Any missing task or mismatch returns `ErrPreviewChanged`, and nothing is written.
  - Build invoices:
    - `combined` makes one invoice.
    - `project` makes one invoice per project, ordered by project name.
    - Lines are sorted as today. Everything happens in one transaction.
    - Each invoice gets its own `billing_run` audit row, now with `"split"` and its own `entry_ids`.
    - The zero-total rule applies per invoice.
  - Issue runs after commit, per invoice. A failure is stored in `RunInvoice.IssueErr`, and later invoices are still attempted.
- Produces (HTTP):
  - `GET /api/v1/orgs/{org}/billing-runs/preview?client_contact_id=&from=&to=` (`PermInvoicesWrite`, timesheets module). Returns 200:
    `{projects: [{project_id, project_name, minutes, amount, gst_amount, tasks: [{task_id, task_name, minutes, amount, gst_amount}]}], minutes, amount, gst_amount}`.
    Project and top-level sums are computed server-side.
  - `POST /api/v1/orgs/{org}/billing-runs` body: `{client_contact_id, from, to, due_in_days, issue, split, tasks: [{task_id, minutes, amount}]}`.
    - Returns 201 `{invoices: [{invoice: InvoiceResponse, issue_error?: string}], entry_count, total_minutes}`.
    - `issue_error` is the user-safe message from the same mapping `writeBillingRunError` uses. Change that helper to return `(status int, msg string)` and delete its JSON-writing half.
    - A stale selection returns 409 `{error: "billing preview changed; refresh and review"}`.

- [ ] **Step 1: Failing integration tests** (update the existing tests to pass `Split: "combined"` and full `Tasks` from `PreviewBilling`; add a helper `selectAll(t, preview) []timesheets.TaskSelection` in the test file):
  - `TestPreviewMatchesInvoiceToTheCent`: GST-registered org, 50 min at $120/h on GST → task `Amount 10000, GSTAmount 1000`. Running it gives invoice total 11000.
  - `TestRunBillingSelectedTasksOnly`: two tasks, select one → the other's entries stay unbilled.
  - `TestRunBillingPerProject`: two projects → two invoices, each with only its own project's lines, and two `billing_run` audit rows.
  - `TestRunBillingStaleSelection`: preview, then log 10 more minutes on the selected task → `errors.Is(err, core.ErrInvalidState)`, and no invoices or audit rows. Logging time on an *unselected* task → the run succeeds.
  - `TestRunBillingRetryAfterCommitIs409`: run, then repeat the identical input → `ErrInvalidState`, and the invoice count is unchanged.
  - `TestRunBillingRejectsBadSelection`: empty tasks, duplicate IDs and an unknown split → `ErrInvalidInput`. A foreign-org task ID → `ErrInvalidState` (treated as stale, so nothing leaks).
  - `TestRunBillingPerProjectIssueFailureReportsEach`: timesheet invoices have no natural issue failure, so `billrun.go` calls issuing through a package var `var issueInvoice = (*billing.Service).Issue`. `export_test.go` (package `timesheets`) exposes `func SetIssueForTest(f func(*billing.Service, context.Context, int64, string, int64) (core.Invoice, error)) (restore func())`. The test fails the second call and asserts that both drafts exist, `Invoices[0].IssueErr == nil`, `Invoices[1].IssueErr != nil`, and `Invoices[0].Invoice.Status == "open"`.
  - Keep the existing concurrency test (if present) green with the new input.
- [ ] **Step 2: HTTP cases** (`timesheets.yaml`), replacing the two billing-run cases:
  - bookkeeper → preview 403 exact, and POST 403 exact, `unchanged: [invoices, invoice_lines, time_entries, audit_log]`
  - preview for a foreign client → 422 `{error: "invalid input: client_contact_id not found"}`
  - POST with `tasks: []` → 422 `{error: "invalid input: tasks must not be empty"}`, unchanged
  - POST selecting `{tk}` with `minutes: 60` when nothing is logged → 409 `{error: "billing preview changed; refresh and review"}`, unchanged
  - `modules_disabled.yaml`: preview → 404 when timesheets is disabled (copy the projects case shape)
- [ ] **Step 3: Run** `uv run --locked task test SUITE=integration PACKAGES="./internal/timesheets/... ./internal/httpapi/..."`. Expected: FAIL.
- [ ] **Step 4: Implement** the types, preview, refactor, run, handlers and route.
- [ ] **Step 5: Run** the same command, plus `uv run --locked task test`. Expected: PASS.
- [ ] **Step 6: Commit** `feat(timesheets): preview and split billing runs by task and project`

---

### Task 5: Billing-run dialog on Invoices

**Files:**
- Create: `web/src/lib/modules/timesheets/billingSelection.ts` + `billingSelection.test.ts`
- Create: `web/src/lib/modules/timesheets/BillingRunDialog.svelte` + `BillingRunDialog.test.ts`
- Create: `web/src/lib/test/editors/billing-run-dialog.yaml`; register `BillingRunDialog` in `web/src/lib/test/editors.registry.ts`
- Modify: `web/src/routes/[org]/invoices/+page.svelte` (button + dialog)
- Delete: `web/src/routes/[org]/billing-run/` (page and both tests; ownership moves to the dialog test + contract YAML)
- Modify: `web/e2e/financial-workflows.spec.ts:166-177`
- Modify: `docs/ui-standards.md` (layout table row 21: billing run → "bounded dialog with preview"; ledger: remove the `billing-run` row, add a `BillingRunDialog` note to the `[org]/invoices/+page.svelte` row)

**Interfaces:**
- Consumes: the preview/run API from Task 4.
- Produces (`billingSelection.ts`, pure):
  ```ts
  export type PreviewTask = { task_id: number; task_name: string; minutes: number; amount: number; gst_amount: number };
  export type PreviewProject = { project_id: number; project_name: string; minutes: number; amount: number; gst_amount: number; tasks: PreviewTask[] };
  export function filterProjects(projects: PreviewProject[], query: string): PreviewProject[];
  // case-insensitive trim; project-name match keeps all its tasks; task-name match keeps that task under its project
  export function projectState(p: PreviewProject, selected: ReadonlySet<number>): 'all' | 'some' | 'none';
  export function toggleProject(p: PreviewProject, selected: ReadonlySet<number>): Set<number>;
  // operates on ALL p.tasks, not the filtered view; 'all' → clears, otherwise selects all
  export function selectionTotals(projects: PreviewProject[], selected: ReadonlySet<number>): { minutes: number; amount: number; gst_amount: number };
  export function selectionPayload(projects: PreviewProject[], selected: ReadonlySet<number>): { task_id: number; minutes: number; amount: number }[];
  ```
- `BillingRunDialog` props: `{ open: boolean (bindable); org: string; defaultFrom: string; defaultTo: string }`.
  - On open it loads `GET contacts` (clients only). Once client, from and to are valid it auto-loads the preview with a seq token. Changing any of them discards the preview and reloads.
  - Search input "Search projects and tasks".
  - Project checkbox `indeterminate` when the state is `some`.
  - Radio "One combined invoice" / "One invoice per project".
  - "Due in days" (0–90) and "Issue immediately".
  - Totals use `money()`, showing GST when `gst_amount > 0`.
  - Submit "Create invoices" is disabled with an empty selection.
  - On 201 with one invoice and no `issue_error` → `goto` that invoice. Otherwise it swaps the form for a results list (links plus each `issue_error`) and a Close button.
  - On 409 it keeps the selection, shows the message and a "Refresh preview" button. Refreshing re-selects only task IDs still present.
  - Busy blocks dismissal. Dirty edits confirm before dismissal (Dialog's form tracking).
- Invoices page shows the "Billing run" button only when `session.modules.includes('timesheets')` and the current org's perms include `invoices_write`. It passes `defaultFrom` = first of the month and `defaultTo` = `todayLocal()`.

- [ ] **Step 1: Failing `billingSelection.test.ts`**:
  - "filter by project keeps all tasks"
  - "filter by task keeps project context"
  - "project toggle applies to hidden tasks": select all, filter to one task, toggle its project → `selectionTotals` is 0
  - "mixed state"
  - "payload echoes preview minutes and amount for selected tasks only"
- [ ] **Step 2: Failing `BillingRunDialog.test.ts`**:
  - posts `{client_contact_id: 2, from: "2026-11-01", to: "2026-11-07", due_in_days: 14, issue: false, split: "project", tasks: [...]}` after unticking one task
  - single invoice → `goto('/acme/invoices/77')`
  - two invoices → results list with both links and the `issue_error` text
  - 409 → message, selection preserved, "Refresh preview" re-fetches
  - changing client clears the preview and fetches a new one
  - Invoices page hides "Billing run" without the timesheets module or without `invoices_write` (put this case in `web/src/routes/[org]/invoices/+page.test.ts`, created here)
- [ ] **Step 3: Contract** `billing-run-dialog.yaml`:
  - props `{open: true, org: acme, defaultFrom: "2026-11-01", defaultTo: "2026-11-07"}`, `modules: [timesheets]`
  - `api`:
    - `GET /api/v1/orgs/acme/contacts`
    - `GET /api/v1/orgs/acme/billing-runs/preview?client_contact_id=2&from=2026-11-01&to=2026-11-07` (one project, one task)
  - `ready: Design`, `fill: {Due in days: "30"}`, `submit: Create invoices`, `request: POST /api/v1/orgs/acme/billing-runs`, `preserves_input_on: 409`, all other keys true / Cancel
- [ ] **Step 4: Run** `uv run --locked task web:test`. Expected: FAIL.
- [ ] **Step 5: Implement** the helper, the dialog and the invoices page wiring. Delete the old route and its tests. Update the e2e spec: go to `${base}/finance/invoices`, click "Billing run", select the client and dates in the dialog, then click "Create invoices". The duplicate API POST now sends the same `tasks` and expects **409**. Update `docs/ui-standards.md`.
- [ ] **Step 6: Run** `uv run --locked task web:test`. Expected: PASS. Then `uv run --locked task web:e2e`. Expected: PASS (needs Docker + Chromium; if unavailable, report it as not run).
- [ ] **Step 7: Commit** `feat(web): billing run dialog with task selection and split`

---

### Task 6: Finance navigation group

**Files:**
- Modify: `web/src/lib/modules/registry.ts`, all four `manifest.ts`, `web/src/routes/[org]/+layout.svelte:60-80`
- Test: `web/src/lib/modules/registry.test.ts`

**Interfaces:**
- Produces:
  ```ts
  export type NavGroup = 'overview' | 'property' | 'projects' | 'finance' | 'admin';
  export const NAV_GROUPS: { id: NavGroup; label: string }[] = [
    { id: 'overview', label: 'Overview' }, { id: 'property', label: 'Property' },
    { id: 'projects', label: 'Projects' }, { id: 'finance', label: 'Finance' }, { id: 'admin', label: 'Admin' }
  ];
  // NavItem gains `group: NavGroup`
  export type VisibleNavGroup = { id: NavGroup; label: string; items: NavItem[] };
  export function visibleNav(manifests, enabledModules, perms): VisibleNavGroup[]; // NAV_GROUPS order, items in MODULE_ORDER then manifest order, empty groups omitted
  ```
- Assignments:
  - core:
    - Dashboard, Contacts → overview
    - Invoices, Reports → finance
    - Settings, Members → admin
  - property: Properties → property
  - timesheets: Projects, Time-Logging → projects. Remove the `billing-run` item.
  - reconciliation: Reconciliation, Expenses → finance
- Layout keys on `group.id`, renders `group.label`, and drops the `core` special case: Overview always has Dashboard (`perm: ''`).

- [ ] **Step 1: Rewrite `registry.test.ts`** around groups:
  - owner with all modules → labels `['Overview','Property','Projects','Finance','Admin']`, and Finance hrefs `['invoices','reports','reconciliation','expenses']`
  - reconciliation disabled → Finance hrefs `['invoices','reports']`
  - no perms → only Overview `['']`
  - timesheets disabled → no Projects group
  - no item anywhere has href `billing-run`
  - keep the existing contacts/settings permission assertions, ported to groups
- [ ] **Step 2: Run** `uv run --locked task web:test`. Expected: FAIL.
- [ ] **Step 3: Implement** the registry, manifests and layout.
- [ ] **Step 4: Run** `uv run --locked task web:test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(web): group money screens under Finance`

---

### Task 7: Final verification

- [ ] `uv run --locked task lint`
- [ ] `uv run --locked task test`
- [ ] `uv run --locked task test SUITE=integration`
- [ ] `uv run --locked task web:test`
- [ ] `uv run --locked task web:e2e` (report if the environment can't run it)
- [ ] Update the spec's "Unresolved provider detail" paragraph with the reinstate decision from Task 2, and commit `docs: record reinstate provider decision`.
