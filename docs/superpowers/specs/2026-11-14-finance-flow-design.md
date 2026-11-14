# Finance flow refinement

Status: design recorded for approval before implementation.

## Intent and constraints

Refine the project → time logging → billing run → invoice flow for normal
small-business use. Gather money screens into Finance, give staff more control
over billing selections, and support deliberate invoice status changes.

Shigoto is development-only and not deployed. No production data migration,
legacy API compatibility, or old-route compatibility is required. Existing
migrations may be edited where appropriate; database constraints and fixtures
must still match the resulting schema.

Implementation must be native and inline: no subagents. Reuse the existing Go,
Svelte, Tailwind, DaisyUI, API client, dialogs, and shared YAML test harnesses.

## Approaches agreed

### Navigation

- **Chosen:** nav links declare a display group independently of their owning
  module. Module availability and permissions still control visibility.
- A new Finance module was rejected because it would muddle domain ownership
  and optional-module switches.
- Hard-coded sidebar links were rejected because they bypass the registry.

### Billing preview consistency

- **Chosen:** stateless preview. Submit selected task IDs and the expected
  unbilled minutes and amount for each task. Recompute under database locks
  before creating invoices; reject changed selections with HTTP 409.
- Server-stored preview tokens were rejected because they add persistence,
  expiration, and cleanup without a current need.

## Finance navigation

Finance appears immediately after Projects and contains:

- Invoices
- Reports
- Reconciliation
- Expenses

Remove the separate Reconciliation navigation group. Keep reconciliation domain
ownership and its optional-module gate unchanged. Remove Billing run from the
Projects group; it becomes an action on Invoices.

Nav grouping must preserve organisation permissions and enabled-module checks,
hide empty groups, and leave dashboard module behaviour unchanged.

Proposed grouping for remaining links: Dashboard and Contacts in Overview,
Properties in Property, Projects and Time-Logging in Projects, Settings and
Members in Admin. These labels are implementation proposals, not new modules.

## Billing-run dialog

Replace the standalone billing-run page with a dialog launched by a **Billing
run** button on Invoices. Follow the Properties page's existing Dialog and
FormActions pattern. Show the action only when the timesheets module is enabled
and the user has the existing billing-run permission; enforce both server-side.

### Inputs and preview

1. Choose client, date range, and due days.
2. Load eligible unbilled time-and-materials work grouped by project, then task.
   Show unbilled hours and billing amount for each task and project.
3. Initially select all tasks. A project checkbox selects or clears all its
   tasks and shows a mixed state when only some are selected.
4. An optional client-side text search matches project and task names. Matching
   a project shows its tasks; matching a task retains its project context.
   Hidden rows keep their selection. Project checkbox actions apply to the
   whole project, including hidden tasks; make that scope clear.
5. Choose **One combined invoice** or **One invoice per project**.
6. Retain **Issue immediately** and show a live total for every selected task,
   including tasks hidden by search. Distinguish GST where applicable using
   existing financial formatting and calculations.

Client or date-range changes invalidate the preview. Empty selections cannot
be submitted. No individual time-entry selection and no expense rebilling.

### Commit and recovery

The preview is advisory, not authority. Validate organisation, client ownership,
project billing method, task ownership, dates, permissions, and submitted
selection at the server boundary.

Within one transaction, lock and recompute eligible time for selected tasks.
Compare each selected task's minutes and amount with its preview values.
Missing, already-billed, or changed selected work causes HTTP 409 and no invoice
creation. Show a refresh/review action rather than silently retrying. Changes
to unselected tasks do not invalidate the selected work.

The totals check protects the reviewed financial result; it is not an exact
snapshot of entry identity. Equal-total substitutions may pass. If exact entry
identity later matters, extend the check rather than claiming snapshot semantics.

Preserve existing per-task invoice lines, rates, GST rounding, time-entry
linking, and audit behaviour. Combined output creates one draft; per-project
output creates one draft for each selected project, atomically. No empty invoices.

Issue invoices after draft creation commits, as today. If issuing or provider
sending only partly succeeds, return every created invoice ID and its outcome.
Show a results/recovery list with invoice links; never instruct staff to rerun
the billing operation to recover already-created invoices. For a single fully
successful invoice, navigate to its detail page. Multiple successful invoices
use the same results list, including issued invoices rather than a draft filter.

Preserve input on failure, prevent duplicate submission, provide explicit
cancellation, protect dirty selections, and block dismissal while busy.

## Invoice status changes

"Sent" remains distinct from local invoice status. Do not introduce a separate
sent status or permit arbitrary status assignment.

| Transition | Behaviour |
| --- | --- |
| Open → Uncollectible | Require a reason; audit the change. |
| Uncollectible → Open | Require a reason; audit reinstatement. |
| Uncollectible → Paid | Existing payment recording marks paid once fully covered. |
| Uncollectible → Void | Allow only when no payments exist; free linked time as for existing voids. |

Use the existing `uncollectible` status spelling in schema and API. Add an
Uncollectible filter on the invoice list and appropriate actions on invoice
detail. Short status editors use Dialog with required reason, failed-input
preservation, duplicate-submit protection, and explicit cancellation.

Record enough state to restore the pre-payment collection status when a payment
is reversed: a paid invoice previously uncollectible returns to Uncollectible
when its payment total falls below its balance requirement. An invoice paid
from Open returns to Open. Partial payments must not silently reinstate a
written-off invoice. Persist the collection state rather than inferring it
from whichever audit row happens to be latest.

Reconciliation permits explicit manual matching against Uncollectible invoices.
Do not include them in automatic matching or match suggestions. Preserve
organisation isolation, row locking, payment idempotency, and audit trails.

### Stripe and portal constraints

Preserve provider references, webhook idempotency, and existing portal isolation.
The portal already exposes Uncollectible invoices. Ensure its labels and actions
remain truthful after each transition.

Marking an invoice uncollectible should use Stripe's corresponding operation
when a provider invoice exists. Voiding must preserve provider safeguards.
Provider failures must be explicit and recoverable without duplicate invoices.

**Provider decision (resolved during implementation):** Stripe has no inverse
of marking an invoice uncollectible, but keeps uncollectible invoices payable.
Reinstating is therefore local only: the provider invoice, its reference and
hosted link are left unchanged, Stripe keeps showing it as uncollectible, and
an `invoice.paid` webhook still records payment. The reinstate dialog says so
when the invoice has a Stripe link. Voiding an uncollectible invoice voids it
at Stripe as for open invoices.

## Explicit non-goals

- No manual-payment deletion or new payment-reversal functionality. The user
  explicitly excluded this; existing reconciliation unmatching remains.
- No voiding any invoice with recorded payments, including partial payments.
  Consequently a manually part-paid invoice still cannot be voided through
  this change. Do not promise an unavailable manual-payment deletion path.
- No refunds, credit notes, or customer credit balances.
- No editing invoice lines, amounts, or dates; no Open → Draft transition.
  Preserve invoice numbering and existing void-and-rebill behaviour.
- No new accounting basis or claim that Uncollectible automatically creates
  a tax/accounting write-off. Keep existing cash-based report calculations.

## Scope and verification

Changes reach the nav registry/manifests/layout; invoice list and detail; the
billing-run route/dialog and API client; timesheet preview and billing service;
invoice state, payment and reconciliation logic; provider interface/Stripe
adapter as needed; relevant schema, queries, fixtures, and tests.

Follow `docs/testing.md`, testing behaviour in its owning layer:

- HTTP YAML: permissions, module gates, organisation isolation, invalid input,
  and invalid transitions for preview, billing, and status endpoints.
- Financial integration tests: combined/per-project selection, exclusion of
  fixed-price/already-billed work, stale previews, rounding, atomic draft
  creation, concurrency/duplicate submission, issue recovery, status restoration,
  manual-only reconciliation, void guards, and released time entries.
- Provider tests: status operations, failure/retry behaviour, and webhook races.
- Editor YAML/UI tests: search and hidden selections, project checkbox scope,
  totals, invalidated preview, input preservation, cancellation, busy/dirty
  guards, multi-invoice recovery, and status reasons.
- Registry tests: group order, permissions, module switches, and empty groups.

Update `docs/ui-standards.md`: replace the billing-run workflow guidance and
screen-ledger entry with this agreed bounded dialog pattern. Keep complex
invoice creation on its dedicated route.

Run relevant Taskfile checks sequentially via `uv run --locked`. Never use
production/operator databases or commit credentials or generated secrets.

## Left to implementation

Exact endpoint paths, typed request/response shapes, component boundaries,
storage names for collection state, and audit payload shape follow existing
patterns. All financial actions need retry safety; request deduplication must
cover a billing response lost after commit, not just double-click protection.

After design approval, write the implementation plan and execute inline without
subagents. No implementation work is included in recording this design.
