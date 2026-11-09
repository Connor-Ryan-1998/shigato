# App-wide interface standard

## Intent

Make every existing screen feel like one application, using the project,
timesheet, and invoice workflows as the reference. Document the rules so
future features extend the same platform rather than introduce another one.
The user approved shared components with task-specific layouts, including
settings, authentication, invitations, dashboards, reports, and the portal.

## Goals

- Consistent headers, navigation, primary actions, filters, tables, forms,
  loading states, errors, empty states, and save/cancel behavior.
- Reuse the existing Svelte 5, SvelteKit, Tailwind, and DaisyUI stack.
- Preserve keyboard access, focus, mobile usability, and both existing themes.
- Preserve business behavior, permissions, financial warnings, and portal isolation.
- Supply a repository-local standard, route coverage ledger, and contributor checklist.

## Non-goals and anti-goals

No new framework, dependency, API redesign, database migration, permission,
business feature, production deployment, or universal CRUD page generator.
Uniform does not mean every screen is a table or every form is a modal.
Do not copy weaknesses in the reference screens: their state handling and
permissions must meet the same rules as the rest of the application.

## Approaches

1. Visual-only cleanup is smaller, but leaves inconsistent data interactions.
2. Shared components with task-specific layouts is selected: reuse the existing
   platform, consolidate repeated presentation, and preserve domain ownership.
3. One universal screen template forces unrelated workflows into the same shape
   and creates configuration rather than clarity.

## Layouts

- Lists: page title and actions, optional status views, labeled filter bar,
  results, and pagination only where the server already supports it.
- Details: title, back navigation, summary, relevant actions, and related records.
- Short create/edit operations: explicit action opens a dialog; save closes only
  after success, cancel discards only with confirmation when changed.
- Complex forms: dedicated routes. Keep invoice creation on its existing route;
  move expense and lease creation out of their lists onto dedicated routes.
- Settings: grouped form, explicit save, and cancel/reset to the last saved values.
- Dashboards/reports: shared sections, feedback, filters, and formatting; keep
  meaningful cards, financial tables, charts, and downloads.
- Authentication/invitations: compact form layout using the same fields, actions,
  and feedback. Never treat passwords or authentication tokens as persistent drafts.
- Portal: reuse presentation components but retain its separate shell, endpoints,
  navigation, and contact-scoped data. Retain tenant-only leases.
- Reconciliation: retain the contextual bank-line drawer and its matching workflow.

## Interaction invariants

- All network requests continue through `web/src/lib/api.ts`.
- Guard reads and writes by the current organisation, record, permissions, and
  request identity. Ignore late results after navigation, dismissal, or unmount.
- Separate saving from refreshing: a successful write followed by a failed refresh
  must not be reported as a failed write or encourage a duplicate submission.
- Prevent duplicate submissions both in the handler and in disabled controls.
- Keep failed form input and show errors inside the active page, dialog, or drawer.
- Show actionable initial-load errors with retry. Keep same-scope records visible
  during background refresh and identify failed refreshes without hiding records.
  Never retain another organisation's records or mislabel another filter's results.
- Distinguish an empty collection, no filter matches, and an out-of-range page.
- Keep warning details and invoice links visible after a successful lease/expense
  action. Existing invoice periods are not recalculated by UI changes.
- Confirm irreversible actions with their existing consequences; do not retry
  payments, sends, or billing runs merely because the UI refreshed.
- Dirty editors confirm dismissal/navigation; busy editors prevent dismissal.
  Explicit logout and session loss must not be blocked by draft protection.
  No drafts persist to local storage or cross organisation/account boundaries.
- Existing URL filters and pagination remain navigable through browser history.
  Use URL state for shareable list filters; keep unsaved form values local.
  Do not invent sorting, search, filters, or pagination unsupported by a workflow.

## Shared platform

Extend `PageHeader`, `Button`, `Field`, `DataTable`, `Dialog`, `Drawer`,
`EmptyState`, `Toast`, `StatusBadge`, `Money`, and `DateText` where they own
the behavior. Introduce only repeated primitives: a filter bar, pagination,
load-state presentation, form action row, and dirty-navigation guard.
Keep API calls, domain types, payloads, and validation in their existing module.

Native dialog owns focus trapping; the shared wrapper owns busy/dirty dismissal
and focus restoration. Navigation protection uses SvelteKit `beforeNavigate`
and browser `beforeunload`, not a parallel router. Field hints/errors expose
stable IDs for `aria-describedby`; invalid fields use `aria-invalid`.
Use real links for navigation, buttons for actions, visible labels for inputs,
semantic headings/tables, and text alongside status colors. Wide tables scroll
within their container rather than widen the page.

## Documentation

`docs/ui-standards.md` is the maintained source for layout decisions, component
contracts, examples, state ownership, accessibility, exceptions, and the new-screen
checklist. Its route ledger covers every `+page.svelte` and the shared shells,
including redirects and not-found routes. Each row records migration/test evidence
or an explicit reason no change is needed. Link it from `README.md` and a local
`AGENTS.md` so contributors and coding agents find it.

## Validation

Use existing Taskfile checks sequentially. Add component and route tests around
changed behavior, not snapshots alone. Browser tests exercise real dialog focus,
Escape/cancel, dirty navigation, settings reset, failed save recovery, mobile and
theme behavior, staff session expiry, and portal isolation. Extend current E2E
fixtures rather than duplicate their database/process infrastructure.

Run `uv run --locked task web:test`, `uv run --locked task lint`, and
`uv run --locked task web:e2e` (substitute `go-task` if installed under that name).
Use the repository's uv-managed environment. Browser fixtures recreate dedicated
`shigoto_e2e*` databases: never use production or operator data. Do not run builds,
dependency installation, or database verification concurrently.

## Builder decisions

Exact component APIs, task order, and test locations are specified in the linked
implementation plan. Keep unchanged API contracts and financial safety tests as
constraints, not opportunities for unrelated refactoring. No commits or deployment
are implied by approval to edit the application.
