# App-wide Interface Standard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Standardize every application screen and document the existing UI platform for future extensions.

**Architecture:** Extend existing shared Svelte components and keep data access and business rules in their owning modules. Use list, detail, dialog, full-page form, settings, report/dashboard, and authentication layouts rather than a universal CRUD generator. Keep portal and staff shells separate.

**Tech Stack:** Svelte 5, SvelteKit, TypeScript, Tailwind 4, DaisyUI 5, Vitest, Testing Library, Playwright; existing Go JSON API unchanged.

**Spec:** `docs/superpowers/specs/2026-11-09-ui-standard-design.md`

## Execution record

Implemented natively on the existing `main` branch. No commits, push, worktree,
or deployment. All seven task groups are complete; the original checklists
below describe the planned sequence. The maintained result is
[`docs/ui-standards.md`](../../ui-standards.md), including all 32 routes/shells.

Final verification on 2026-11-09:

| Command | Result |
| --- | --- |
| `uv run --locked task web:test` | 44 test files, 277 tests passed; type check clean |
| `uv run --locked task lint` | Formatting, Go vet, and Svelte check passed; zero Svelte warnings |
| `uv run --locked task web:e2e` | Build and 20 Chromium workflows passed |
| `uv run --locked task test` | Go race unit suite passed |
| `uv run --locked task test SUITE=integration` | Go race integration suite passed; slowest package 303 seconds |
| `git diff --check` | Passed |
| Route/standards ledger comparison | All 32 screens and shells accounted for |

The first integration attempt hit the tool's 120-second timeout, not a test
failure. The rerun with a 660-second allowance completed successfully.
The fresh reviewer found three Important issues, all fixed with regressions:
invoice creation recovery after failed/delayed navigation, authentication
teardown bypassing beforeunload guards, and expense option invalidation after
permission/module changes. No Minor findings were deferred.

### Decisions and boundaries

| Decision | Reason and remaining cost |
| --- | --- |
| Stay on the current branch without commits/worktree | Explicit user instruction; uncommitted work must remain intact. |
| Track ordinary dialog input natively rather than duplicate dirty state in every form | Less state duplication; richer domain workflows can still supply explicit dirty state. |
| Aggregate nested navigation guards and bypass them after authentication teardown | One prompt per navigation; guard teardown and auth cancellation require regression coverage. |
| Keep the single-use property dialog inside its route | No unused abstraction; extract it if a second owner needs it. |
| LineEditor signals dirty events rather than partially parsed payloads | Keeps validation and unfinished line state in the editor; callers cannot inspect incomplete lines. |
| Preserve existing comments during contact-dialog consolidation | Retains context; a route comment now describes behavior owned by the shared dialog. |
| Retain native focus cycling through browser chrome | Avoids a custom focus trap; browser-specific differences may require additional browser checks. |
| Preserve existing local filters and existing URL-backed filters | Avoids silently changing persistence/server semantics; new shareable filters must use URL state. |
| Retain separate portal and org-keyed staff shells | Preserves access boundaries; broader role/session concurrency remains a separate audit. |
| Do not expand into pre-existing invoice-detail/reconciliation request gaps | No unrelated concurrency redesign; edge-case races outside tested workflows may remain. |
| Leave unrelated `.dockerignore`, `.gitignore`, and `j` untouched | Preserves user work and cookie data; those changes are outside this review. |

No dependency/security audit or live-provider acceptance was performed by this
UI change. Existing README deployment limitations still apply.

## Global Constraints

- No new framework, dependency, API redesign, database migration, permission, business feature, or production deployment.
- All network requests continue through `web/src/lib/api.ts`.
- Preserve financial warnings, payment/billing safeguards, permission checks, portal isolation, existing comments, and unrelated user changes.
- Keep the separate portal shell, endpoints, navigation, contact-scoped data, and tenant-only leases.
- No drafts persist to local storage or cross organisation/account boundaries.
- Explicit logout and session loss must not be blocked by draft protection.
- Do not invent sorting, search, filters, or pagination unsupported by a workflow.
- Existing URL filters and pagination remain navigable through browser history.
- Run Taskfile checks through `uv run --locked`; substitute `go-task` for `task` when required.
- Run verification sequentially; browser fixtures may recreate only dedicated `shigoto_e2e*` databases.
- No commits unless requested. Before execution, apply the worktree skill and preserve local `.dockerignore`, `.gitignore`, and untracked `j` changes.

## Review Focus

- A save succeeds but refresh fails: report saved state and offer refresh, never invite another write (Tasks 2–5).
- Organisation/record/permission changes during requests: ignore late data, navigation, notices, and option loads (Tasks 2–6).
- Dirty or busy editor dismissed through Escape, Cancel, browser Back, or a link: preserve the draft or confirm intentional discard (Tasks 1–6).
- Lease/expense actions return advisory invoice warnings: retain warnings and links outside closing dialogs (Tasks 3–4).
- Portal sessions or anonymous authentication routes: never render staff navigation, private fields, password re-login, or drafts from another account (Task 6).

## File responsibilities and coverage

- `web/src/lib/components/`: shared presentation and native interaction behavior, no domain API calls.
- `web/src/lib/forms/`: small dirty-navigation helper only; no generic form engine.
- `web/src/lib/modules/`: existing domain forms/tables, plus extracted contact/property/lease forms where needed.
- `web/src/routes/`: route state, permission gates, loading/mutation orchestration, and composition.
- `web/src/app.css`: existing theme setup and only repeated presentation styles that cannot live cleanly in components.
- `docs/ui-standards.md`, `README.md`, `AGENTS.md`: maintained conventions and discovery.
- Existing colocated tests and `web/e2e/*.spec.ts`: regression proof; add missing page tests next to their routes.

The route inventory must include these exact existing paths (relative to `web/src/routes/`):

| Group | Routes |
| --- | --- |
| Staff overview | `[org]/+page.svelte`, `[org]/reports/+page.svelte` |
| Core billing | `[org]/contacts/+page.svelte`, `[org]/invoices/+page.svelte`, `[org]/invoices/new/+page.svelte`, `[org]/invoices/[id]/+page.svelte`, `[org]/billing-run/+page.svelte` |
| Timesheets | `[org]/projects/+page.svelte`, `[org]/projects/[id]/+page.svelte`, `[org]/time/+page.svelte` |
| Property | `[org]/properties/+page.svelte`, `[org]/leases/[id]/+page.svelte` |
| Reconciliation | `[org]/reconciliation/+page.svelte`, `[org]/reconciliation/accounts/+page.svelte`, `[org]/expenses/+page.svelte`, `[org]/expenses/[id]/+page.svelte` |
| Settings | `[org]/settings/+page.svelte`, `[org]/settings/members/+page.svelte` |
| Portal | `portal/+page.svelte`, `portal/[org]/invoices/+page.svelte`, `portal/[org]/invoices/[id]/+page.svelte`, `portal/[org]/leases/+page.svelte` |
| Authentication | `login/+page.svelte`, `auth/magic/+page.svelte`, `invite/[token]/+page.svelte` |
| Routing | `+page.svelte`, `[org]/[...rest]/+page.svelte` |
| Shared shells | `+layout.svelte`, `[org]/+layout.svelte`, `portal/+layout.svelte` |

### Task 1: Shared interaction and presentation primitives

**Files:** Modify existing `PageHeader.svelte`, `Button.svelte`, `Field.svelte`, `DataTable.svelte`, `Dialog.svelte`, `Drawer.svelte`, `Toast.svelte` under `web/src/lib/components/`. Create `FilterBar.svelte`, `Pagination.svelte`, `LoadState.svelte`, `FormActions.svelte`, their colocated `.test.ts` files, and `web/src/lib/forms/unsaved.svelte.ts` with its test.

**Interfaces:**
- Extend `PageHeader` with optional `back?: { href: string; label: string }` and `heading?: Snippet`; retain `title: string` and `actions?: Snippet`. Custom headings preserve one accessible h1.
- Extend `Button` with `busy?: boolean` and `size?: 'sm' | 'md'` while preserving its existing props and default type. Busy implies disabled and `aria-busy`.
- `FilterBar({ label?: string, children: Snippet })` supplies the wrapping labeled filter region, not filter logic.
- `Pagination({ offset: number, pageSize: number, total: number, count: number, onchange: (offset: number) => void })` renders range and Previous/Next using the existing labels. Caller owns URL navigation.
- `LoadState({ loading: boolean, error?: string, empty?: boolean, loadingLabel: string, emptyTitle?: string, emptyDescription?: string, onretry?: () => void, children: Snippet })` owns initial presentation only. Keep background-refresh feedback outside it.
- `FormActions({ busy?: boolean, submitLabel: string, busyLabel?: string, oncancel?: () => void })` provides submit and optional Cancel buttons inside an owning form.
- Extend `Dialog` with `busy?: boolean`, `dirty?: boolean`; retain `open`, `title`, children, and `onclose`. All user dismissal paths share a discard check; successful save marks clean before programmatic close.
- Extend `Drawer` with the same busy/dirty defaults without changing navigation drawer behavior.
- `useUnsavedChanges(isDirty: () => boolean, isBusy?: () => boolean): { allowLeave: () => boolean }` registers SvelteKit/browser navigation protection during component initialization. Message: `Discard unsaved changes?`; pending save message: `Please wait for the current save to finish.` Navigation from successful saves bypasses the guard by clearing dirty first. Logout/auth teardown must have an explicit tested bypass.
- `Field` exposes hint/error IDs as exported snippet arguments without breaking existing zero-argument children. Callers bind those IDs to `aria-describedby`, and errors to `aria-invalid`.
- Extend `DataTable` columns with optional `align?: 'left' | 'right'`; optional `caption?: string` provides an accessible name. Keep existing column snippets and `rowKey`.
- `Toast` keeps its existing inline semantics and name for compatibility; danger uses alert semantics, other tones use status semantics. Do not add a global notification bus.

- [ ] Write failing primitive tests: busy disables buttons; header contains one h1; pagination handles zero/last/out-of-range pages; load states distinguish empty/error/loading; table numeric columns align; fields expose unique description IDs.
- [ ] Write dismissal/navigation tests: unchanged cancel closes; dirty cancel/Escape requires confirmation; declined discard preserves values; busy blocks dismissal; successful save can close; logout/session loss bypasses blocking; teardown removes browser listeners.
- [ ] Run `uv run --locked task web:test`; confirm failures identify the missing behavior, not test harness errors.
- [ ] Implement the contracts above with native dialog/SvelteKit behavior. Read all existing Dialog/Drawer callers before changing dismissal handling. Keep guards page-local and avoid nested discard prompts.
- [ ] Run `uv run --locked task web:test`. Expected: all component and existing route tests pass. Real focus and Escape behavior also require Task 7 browser checks.

### Task 2: Bring reference screens onto the standard

**Files:** Modify all five project/time/invoice routes in the route inventory, plus `[org]/invoices/new/+page.svelte`, `web/src/lib/modules/timesheets/{ProjectDialog,LogTimeDialog,EntriesTable}.svelte`, and `web/src/lib/modules/core/invoices/{InvoiceTable,LineEditor,PaymentDialog}.svelte`. Extend their existing tests; create missing invoice list component tests.

**Interfaces:** Consume Task 1 primitives. Preserve `ProjectDialog`'s `onsaved(project: Project)`, `LogTimeDialog`'s `onsaved(): void`, and existing invoice payloads. `LineEditor` gains `busy?: boolean` and `onchange?: (lines: ManualLine[]) => void` so its page can guard the entire editor and track dirty state without duplicating line storage.

- [ ] Add tests for filter changes with reversed response order, permission loss while a dialog is open, duplicate submit, failure preserving drafts, and a successful save followed by failed refresh.
- [ ] Add tests that invoice URL status/offset behavior is unchanged, filter changes reset offset, stale responses cannot navigate after org/record changes, and invoice creation cancellation writes nothing.
- [ ] Run `uv run --locked task web:test`; verify new tests fail for the intended reasons.
- [ ] Replace bespoke header/filter/pagination/state markup with shared components. Preserve timesheet status labels, existing filters, project tabs, billing links, and invoice lifecycle actions. Replace the header dropdown's ad hoc interactive div with a keyboard-operable control.
- [ ] Integrate dirty/busy protection in project/time/payment dialogs and invoice creation. Guard handlers as well as buttons. Preserve money/duration parsing and financial constraints. Separate post-write refresh failures from mutation failures.
- [ ] Run `uv run --locked task web:test`; expected all original and new reference-flow tests pass.

### Task 3: Contacts and property workflows

**Files:** Modify `[org]/contacts/+page.svelte`, `[org]/properties/+page.svelte`, `[org]/leases/[id]/+page.svelte` and their existing tests. Create `web/src/lib/modules/core/contacts/ContactDialog.svelte`, `web/src/lib/modules/property/{PropertyDialog,LeaseForm}.svelte`, `[org]/leases/new/+page.svelte`, and colocated component/page tests. Reuse `ContactDialog` from invoice creation rather than retain a second hand-written contact dialog.

**Interfaces:** `ContactDialog({ open, org, contact?: Contact | null, onsaved: (contact: Contact) => void })` creates/edits using the existing contacts API; optional values remain clearable and edit never changes kind. `PropertyDialog({ open, org, onsaved: () => void })` uses the existing property create payload. `LeaseForm({ busy, error?: string, properties, tenants, onsubmit, oncancel })` retains the exact current lease fields and payload types; export its named `NewLease` payload type for the new route.

- [ ] Add failing tests that New contact/New property open clean dialogs, Edit preloads current data, cancel makes no API write, failed saves preserve input, and stale option/save responses do not affect a different org.
- [ ] Add lease creation tests preserving co-tenants, paid-to date, opening arrears, frequency, optional bond values, validation, and advisory warnings. Verify the new route loads successfully when opened directly and handles missing prerequisites.
- [ ] Add lease action tests preserving overlapping-invoice warnings/links after dialog closure and rejecting duplicate or unauthorized actions.
- [ ] Run `uv run --locked task web:test` to establish the expected failures.
- [ ] Move compact contact/property create/edit forms into their dialogs. Give properties a standard list with explicit New property/New lease actions. Move the longer lease creation form to `/[org]/leases/new`; keep successful creation warnings visible before offering navigation to the saved lease.
- [ ] Turn rent-change, charge, bond-lodgement, and termination editors into explicit short action dialogs on lease detail. Keep ledger, summaries, bond invoice action, irreversible confirmation, and persistent affected-invoice panel on the page. Reuse existing action payloads and parsers, not new business logic.
- [ ] Run `uv run --locked task web:test`; existing contact identity and property billing-warning assertions must remain covered after selector updates.

### Task 4: Expenses and reconciliation

**Files:** Modify `[org]/expenses/+page.svelte`, `[org]/expenses/[id]/+page.svelte`, `[org]/reconciliation/+page.svelte`, `[org]/reconciliation/accounts/+page.svelte`, their tests, and `web/src/lib/modules/reconciliation/{ExpenseForm,LineDrawer,ImportDialog,MatchInvoicePanel,MatchPayoutPanel,BankLineTable}.svelte`. Create `[org]/expenses/new/+page.svelte` and its tests.

**Interfaces:** Reuse `ExpenseForm`'s `NewExpense`, upload callback, and existing optional project/property support. Add `oncancel?: () => void` and `onchange?: () => void` without breaking its embedded reconciliation use. Lists own URL filters; the new expense route owns option loading, receipt upload, save state, warnings, and dirty state. The contextual drawer retains its existing callbacks.

- [ ] Add failing tests for dedicated expense creation, cancel without writing, receipt-upload errors, optional modules disabled, saved warnings/links, and save success followed by failed navigation/refresh.
- [ ] Add stale-response, duplicate-submit, failed-import/match recovery, dirty-drawer dismissal, and filter-history regression tests. Preserve matching an existing expense rather than creating another one.
- [ ] Run `uv run --locked task web:test` and confirm expected failures.
- [ ] Replace embedded new-expense form with New expense navigation. Move existing creation logic to the new route without changing payloads or receipt lifecycle semantics. Use shared list state/filters and preserve date validation.
- [ ] Standardize accounts/import/matching forms and pagination. Keep the bank-line drawer because its context is part of the workflow; do not replace it with a generic CRUD dialog.
- [ ] Run `uv run --locked task web:test`; expected financial and reconciliation regressions remain green.

### Task 5: Settings, members, overview, reports, and billing runs

**Files:** Modify `[org]/settings/+page.svelte`, `[org]/settings/members/+page.svelte`, `[org]/+page.svelte`, `[org]/reports/+page.svelte`, `[org]/billing-run/+page.svelte`, their colocated tests, `web/src/lib/modules/core/members/{MembersTable,InviteDialog,PortalInviteDialog}.svelte`, and the existing module dashboard cards.

**Interfaces:** Settings retain `Settings` and full-record PUT semantics; store independent saved and draft copies. Cancel resets to last confirmed server values, not empty defaults. Members preserve invite-link/manual-email results and per-row actions. Reports retain CSV URLs, grouping, tax periods, and financial table semantics. Billing run retains existing idempotency behavior and result links.

- [ ] Add failing tests for settings reset, dirty-navigation cancellation, empty optional settings, failed saves preserving fields, and successful name changes updating session state.
- [ ] Add tests for member action errors retaining the table and selections, last-owner refusal, invite success details remaining visible, duplicate actions, and stale completions after org changes.
- [ ] Add dashboard/report/billing tests for actionable initial failures, empty results, filter changes, CSV links, and successful billing followed by failed refresh without another billing request.
- [ ] Run `uv run --locked task web:test` to establish failures.
- [ ] Apply shared page sections, form actions, filters, load feedback, table presentation, and formatting. Keep settings as a grouped page form; reset only after confirmed discard. Preserve report captions and row-header semantics rather than force reports into DataTable.
- [ ] Run `uv run --locked task web:test`; expected current tax, settings, membership, and idempotency regressions pass.

### Task 6: Shells, portal, authentication, and route states

**Files:** Modify the three shells and every portal/authentication/routing route in the inventory, their existing tests, `web/src/lib/components/ReloginDialog.svelte`, and `web/src/lib/api.test.ts` only as needed to pin unchanged auth behavior. Create missing portal list/lease and route-state tests. Modify `web/src/app.css` only for genuinely shared styles.

**Interfaces:** Use existing session/theme/module registry and `api` hooks. Do not merge staff and portal layout trees or endpoint types. Keep staff password/SSO re-login distinct from portal magic-link authentication. Redirect-only routes require no artificial form/header but must be recorded as reviewed.

- [ ] Add failing tests for portal active navigation and skip link, contact-scoped read-only fields, tenant-only leases, no staff controls, failed/empty portal loads, and stale responses after org/record changes.
- [ ] Add tests preserving anonymous invitation acceptance, expired/invalid invitations, safe login return URL behavior, magic-link failures, and session-expiry behavior. Dirty guards must not block logout or retain credentials after session loss.
- [ ] Run `uv run --locked task web:test`; confirm new presentation tests fail without changing existing authentication expectations.
- [ ] Apply common field/action/feedback typography and accessible headings to login/invitations/magic-link pages. Standardize portal headers, summaries, empty states, and tables, but retain the simpler portal shell. Add active navigation and consistent skip links to both shells.
- [ ] Review redirect/not-found routes and document explicit no-change decisions. Preserve theme selection and current access boundaries.
- [ ] Run `uv run --locked task web:test`; expected auth, portal, layout, and API tests pass.

### Task 7: Document, prove, and close route coverage

**Files:** Create `docs/ui-standards.md` and local `AGENTS.md`; modify `README.md`, existing `web/e2e/{smoke,financial-workflows,portal,invite}.spec.ts`, and this plan's task checkboxes. Add shared browser assertions in `web/e2e/ui-assertions.ts` only if repeated by multiple suites. Do not duplicate database setup into another standalone test suite.

**Interfaces:** Standard documentation references actual component contracts from Task 1 and representative route/module files. E2E helpers consume Playwright's `Page` only and perform assertions; existing suites retain process/database ownership.

- [ ] Add failing browser checks for a short dialog and a full-page editor: initial focus, Tab containment, Escape, focus return, dirty discard refusal/acceptance, failed save recovery, and prevention of duplicate writes.
- [ ] Add browser checks for settings reset and navigation guards, staff session expiry, portal magic-link expiry, no staff data/navigation in the portal, narrow viewport usability, and both themes. Assert tables scroll locally without whole-page overflow. Use real browser focus behavior, not jsdom claims.
- [ ] Run `uv run --locked task web:e2e`; confirm any new failures correspond to missing behavior. Never weaken financial/isolation assertions to match changed markup.
- [ ] Write `docs/ui-standards.md`: layout decision tree; component APIs; create/edit and save/refresh flows; permissions/org/request ownership; URL state; errors/warnings; money/date formatting; accessibility/mobile/themes; intentional portal/report/drawer exceptions; and checklist for adding a screen.
- [ ] Include every route and shell from the inventory plus the two new create routes in a coverage ledger. Record code and regression test paths, or a concrete reason no code change is needed. No screen may remain silently deferred.
- [ ] Link the standard from `README.md`; add local `AGENTS.md` pointing to it and preserving inherited security/validation instructions. Do not copy the entire standard into agent instructions.
- [ ] Run `uv run --locked task web:test`, then `uv run --locked task lint`, then `uv run --locked task web:e2e`. Expected: all commands exit zero. If the environment lacks uv/task, Docker, Node 24, or Chromium, report the exact blocker rather than claim browser proof.
- [ ] Compare `git diff --check`, changed files, and the completed coverage ledger against the spec. Review portal isolation, financial warnings, stale callbacks, and dirty/busy teardown specifically. Report actual proof, remaining risks, and unrelated pre-existing changes. Do not commit or deploy without a request.

## Self-review

- All route groups and shells are assigned, including settings, portal, authentication, redirects, and not-found.
- Shared APIs are defined once; domain callbacks and API payloads stay with their current owners.
- Every Review Focus failure mode is assigned regression tests in its owning task.
- Existing unpaginated APIs stay unpaginated; no generic data layer or dependency is introduced.
- Browser-only focus, navigation, responsive, and theme claims require browser evidence.
- Execution is complete; see the execution record above for proof and decisions.
