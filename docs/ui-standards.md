# Interface standards

Shigoto uses one UI platform: Svelte 5 and SvelteKit, Tailwind, DaisyUI,
and the components in `web/src/lib/components/`. Extend that platform; do not
introduce another component kit, CSS framework, data client, or generic CRUD engine.

Projects, timesheets, and invoices are the reference workflows. Their useful
patterns are the standard, not every historical implementation detail.
This standard applies to staff screens, settings, authentication, invitations,
dashboards, reports, and the customer portal.

## Choose the right layout

| Task | Layout | Reference |
| --- | --- | --- |
| Browse records | Header, optional status views, filter bar, results, pagination when supported | Projects, invoices, time |
| Review a record | Back navigation, title/status, summary, actions, related records | Project and invoice details |
| Create/edit a short record | Explicit action opens a dialog | Contact, project, property, time entry |
| Enter a complex record | Dedicated page with back navigation and Cancel | New invoice, lease, expense |
| Change settings | Grouped form with Save settings and Cancel | Organisation settings |
| Complete a contextual operation | Drawer, dedicated workflow, or bounded dialog with a server-checked preview, preserving context | Bank-line reconciliation, billing run (dialog on Invoices) |
| Review totals | Sections/cards, labeled filters, semantic financial tables/charts | Dashboard, reports |
| Authenticate | Compact form using shared fields, buttons, and feedback | Login, invitations |
| View customer data | The same presentation rules in the separate portal shell | Portal invoices and leases |

Do not embed a permanently open create/edit form beneath a record list.
Do not turn reports, dashboards, or read-only lease summaries into CRUD tables.
Keep the reconciliation drawer: the selected bank line is part of its context.

## Shared components

Paths below are relative to `web/src/lib/components/`.

| Component | Contract |
| --- | --- |
| `PageHeader.svelte` | `title`, optional `back: {href, label}`, `actions` snippet, optional `heading` snippet. A custom heading supplies exactly one `h1`. |
| `Button.svelte` | `variant: primary/secondary/danger`, `size: sm/md`, `type`, `disabled`, `busy`, `onclick`. Default type is `button`; busy blocks clicks and exposes `aria-busy`. |
| `Field.svelte` | `label`, unique `id`, optional `hint` and `error`; children render the control. Hint/error IDs are `${id}-hint` and `${id}-error`; snippet arguments expose `describedBy` and `invalid`. |
| `Select.svelte` | Existing labeled-select control with string values and `{value, label}` options. |
| `FilterBar.svelte` | Labeled, wrapping filter region. Takes `children` and optional `label`; it does not fetch or filter data. |
| `DataTable.svelte` | `columns`, `rows`, stable `rowKey`, optional accessible `caption`. Columns support cell snippets and `align: right` for numbers. |
| `Pagination.svelte` | `offset`, `pageSize`, `total`, `count`, `onchange(offset)`. The route owns URL updates and server requests. |
| `LoadState.svelte` | `loading`, `error`, `empty`, `loadingLabel`, empty-state copy, `onretry`, and result children. Errors take precedence over loading and empty states. |
| `EmptyState.svelte` | A concise empty/error explanation with an optional action. Never represent permission or load failures as an empty collection. |
| `Dialog.svelte` | Bindable `open`, `title`, `busy`, optional explicit `dirty`, `onclose`. Children receive `{close}` for user cancellation. Native modal supplies focus trapping. |
| `Drawer.svelte` | Same dismissal rules; keeps existing `label`, `side`, and `width` options. |
| `FormActions.svelte` | Submit and optional Cancel within a form; `busy`, `submitLabel`, `busyLabel`, `oncancel`. |
| `Toast.svelte` | Existing inline feedback component despite its historical name. Danger announces an alert; other tones announce status. Place feedback in the active surface, not behind a modal. |
| `StatusBadge.svelte` | Status text plus a domain-supplied tone. Color alone never conveys meaning. |
| `Money.svelte`, `DateText.svelte` | Shared display formatting. Use `lib/format.ts` when formatting inside prose or table snippets. |

Prefer normal HTML links for navigation, including links styled as buttons.
Use buttons for actions. Do not add click handlers to noninteractive divs or rows.
Domain tables can keep custom semantic markup where grouping, totals, captions,
or row headers make `DataTable` inappropriate. Use the same table surface,
numeric alignment, and local horizontal scrolling.

## Data interaction

### Read

1. Check enabled modules and current-organisation permissions before fetching.
2. Identify the view by organisation, record ID, and filters.
3. Show an announced initial loading state.
4. Accept only the latest response for the current view and permissions.
5. Show records, an empty collection, no matches, or a recoverable error distinctly.

An initial-load error needs a retry action. A background refresh of the same
view must not destroy the user's place. Keep valid records visible while
identifying refresh failure. Do not display the previous organisation's data
or present a previous filter's records under a new filter label.

Use the existing API's paging contract. Never paginate only a fetched page as
though it were the full collection. Unpaginated APIs remain unpaginated until
a separate server-side change supports pagination.

Preserve existing URL-backed status views and offsets, browser Back/Forward,
and filter resets. New shareable list filters belong in the URL. Existing
local filters retain their established behavior during this standardization;
do not silently add persistence or change server filtering semantics.

### Write

1. Open a fresh draft explicitly; preload edits without mutating the saved record.
2. Validate input before sending. Keep server validation authoritative.
3. Check organisation, permissions, active editor, and busy state in the handler.
4. Disable affected inputs/actions while saving; the handler also rejects a second submit.
5. Keep rejected input and the server's error in the active editor.
6. On success, mark clean, close/navigate or reset deliberately, and show success/warnings.
7. Refresh the affected view separately. A failed refresh is not a failed write.

Do not automatically repeat invoice issuance, payments, billing runs, imports,
or destructive actions because navigation or refresh failed. Preserve recovery
links from the API. Partial financial success needs a review path, not another
create action.

All requests go through `web/src/lib/api.ts`; it owns CSRF and authentication
handling. Keep API payloads, validation, types, and domain rules in their module
or route rather than put them into shared presentation components.

### Drafts and dismissal

- Native dialogs/drawers track changed controls by default. Supply explicit
  `dirty` when a workflow has a richer notion of completion.
- Render user cancellation through the `{close}` snippet callback. It checks
  busy/dirty state. Assign `open = false` only for successful completion,
  context invalidation, or an intentional non-editor transition.
- Escape, backdrop dismissal, Cancel, and route navigation must not silently
  discard changed input. A pending save prevents editor dismissal.
- Full-page editors use `useUnsavedChanges` from `lib/forms/unsaved.svelte.ts`.
  Mark clean before successful navigation or after confirmed reset.
- Nested page/dialog guards ask once for navigation, not once per component.
- Logout and session-loss redirects must remain possible. Never persist
  passwords, tokens, or drafts in local storage, or carry them between accounts
  or organisations.
- Authentication teardown calls `session.clear()` before redirecting to login
  or leaving expired-session re-login for SSO. Both navigation and beforeunload
  guards recognize the cleared session; a stale cached session must not trap logout.
- Settings Cancel restores the last confirmed server response, including its
  normalization, not empty defaults or the original values from before a save.

## Financial and security boundaries

UI consistency never changes authorization. The API still enforces access;
UI controls also reflect module availability and current permissions.

Keep the portal's layout tree, endpoints, and domain types separate from staff
interfaces. Share presentation components, not staff navigation, organization
switching, private invoice fields, mutation controls, or password/SSO re-login.
Tenant-only lease navigation remains tenant-only. Customer records remain
contact-scoped server-side.

Preserve warnings and invoice links after lease or expense changes. Closing an
action dialog must not hide a required financial review. Existing invoices are
not recalculated by lease UI actions. Keep explicit irreversible-action
confirmations and existing billing/payment idempotency safeguards.

Use integer cents for money payloads and existing parsers for money, quantities,
and duration. Never replace validated parsing with floating-point rounding.
Keep leading zeros in bank details and preserve optional-field clearing behavior.

## Accessibility and visual rules

- Exactly one page `h1`, clear section `h2`s, and a descriptive modal title.
- Real associated labels; placeholders do not replace labels.
- Associate hints/errors with their control; expose invalid fields with
  `aria-invalid` and announce errors with `role="alert"`.
- Announce loading, refresh, and success without unnecessarily moving focus.
- Preserve native focus trapping and return focus to the dialog launcher.
- Identify current navigation with `aria-current`; both shells have skip links.
- Give tables column headers; right-align money/duration/quantity with tabular digits.
- Keep wide tables inside an overflow container. The page itself must not
  overflow horizontally at narrow widths.
- Wrap headers/actions/filters; use full-width controls inside constrained forms.
- Use existing semantic DaisyUI theme classes (`base-*`, `primary`, `error`,
  `warning`, `success`), not hard-coded light-only colors. Keep corporate/business themes.
- Prefer shared components and scoped classes to global selectors targeting all
  forms, tables, inputs, or buttons.

## Examples

A short operation belongs in a domain component using the shared dialog:

```svelte
<Dialog bind:open title="Edit contact" {busy}>
  {#snippet children({ close })}
    <form onsubmit={save}>
      <fieldset disabled={busy}>
        <Field label="Name" id="contact-name">
          <input class="input w-full" id="contact-name" bind:value={name} required />
        </Field>
      </fieldset>
      {#if error}<p role="alert" class="alert alert-error alert-soft">{error}</p>{/if}
      <FormActions {busy} submitLabel="Save contact" oncancel={close} />
    </form>
  {/snippet}
</Dialog>
```

See `lib/modules/core/contacts/ContactDialog.svelte` for the actual shared
contact editor used by both Contacts and New invoice. See
`routes/[org]/expenses/new/+page.svelte` for a full-page editor with option
loading, uploads, context guards, success links, and warnings.

## Existing-screen coverage

Paths below are relative to `web/src/routes/`; `lib/test/...` paths are
relative to `web/src/`. Colocated tests cover the named screen unless another
proof is listed. `lib/test/editors/*.yaml` entries run the shared editor
contract (see `docs/testing.md`). Shared visual changes also apply to
screens that need no route-specific rewrite.

| Screen or shell | Standardization / retained layout | Regression proof |
| --- | --- | --- |
| `+layout.svelte` | Announced session loading; retained retry and re-login | `+layout.test.ts`, `e2e/financial-workflows.spec.ts` |
| `[org]/+layout.svelte` | Active navigation, retained skip link, org-keyed content and mobile drawer | `+layout.svelte.test.ts`, `e2e/smoke.spec.ts` |
| `portal/+layout.svelte` | Separate shell, active navigation, skip link, shared spacing | `+layout.svelte.test.ts`, `e2e/portal.spec.ts` |
| `+page.svelte` | Redirect-only; no artificial form or heading required | `e2e/smoke.spec.ts` |
| `[org]/[...rest]/+page.svelte` | Existing PageHeader not-found layout retained | route review; shared PageHeader tests |
| `[org]/+page.svelte` | Existing shared heading/cards/state layout retained | `+page.test.ts`, `e2e/smoke.spec.ts` |
| `[org]/projects/+page.svelte` | Shared filters/load states; explicit create dialog | `+page.svelte.test.ts` |
| `[org]/projects/[id]/+page.svelte` | Shared back/header/filters; domain summary/chart/tables retained | `+page.svelte.test.ts`; project dialog `lib/test/editors/project-dialog.yaml` |
| `[org]/time/+page.svelte` | Shared header/filter/load states; keyboard-operable view trigger | `+page.svelte.test.ts`, `lib/test/editors/log-time-dialog.yaml`, `e2e/financial-workflows.spec.ts` |
| `[org]/invoices/+page.svelte` | Existing URL status views; shared pagination; `BillingRunDialog` (timesheets module) with task selection, split and per-invoice results | `+page.test.ts`, `lib/modules/timesheets/BillingRunDialog.test.ts`, `lib/modules/timesheets/billingSelection.test.ts`, `lib/test/editors/billing-run-dialog.yaml`, `e2e/smoke.spec.ts`, `e2e/financial-workflows.spec.ts` |
| `[org]/invoices/new/+page.svelte` | Dedicated guarded editor; shared contact dialog | `+page.test.ts`, `lib/test/editors/invoice-new-contact.yaml`, `e2e/financial-workflows.spec.ts` |
| `[org]/invoices/[id]/+page.svelte` | Existing detail/actions retained; protected shared payment dialog; reason dialog for write-off/reinstate | `+page.test.ts`, `lib/test/editors/invoice-payment.yaml`, `lib/test/editors/invoice-uncollectible.yaml`, `e2e/financial-workflows.spec.ts` |
| `[org]/contacts/+page.svelte` | List/filter/state layout; reused contact dialog | `+page.svelte.test.ts`, `lib/test/editors/contact-dialog-*.yaml`, `e2e/financial-workflows.spec.ts` |
| `[org]/properties/+page.svelte` | List, explicit property dialog, New lease navigation | `+page.test.ts`, `e2e/financial-workflows.spec.ts` |
| `[org]/leases/new/+page.svelte` | New dedicated guarded lease editor | `+page.svelte.test.ts`, `lib/test/editors/lease-new.yaml`, `e2e/financial-workflows.spec.ts` |
| `[org]/leases/[id]/+page.svelte` | Short action dialogs; warning panel remains on detail page | `+page.svelte.test.ts`, `+page.test.ts`, `lib/test/editors/lease-rent-change.yaml`, `e2e/financial-workflows.spec.ts` |
| `[org]/expenses/+page.svelte` | List/filter/state layout; New expense navigation | `+page.svelte.test.ts`, `e2e/financial-workflows.spec.ts` |
| `[org]/expenses/new/+page.svelte` | New dedicated guarded expense editor, existing shared split form | parent `expenses/+page.svelte.test.ts`, `lib/test/editors/expense-new.yaml` |
| `[org]/expenses/[id]/+page.svelte` | Shared back/header/table; stored IDs remain fallback for missing names | `+page.svelte.test.ts` |
| `[org]/reconciliation/+page.svelte` | Shared filters/pagination; contextual matching drawer retained | module `LineDrawer.test.ts`, `e2e/financial-workflows.spec.ts` |
| `[org]/reconciliation/accounts/+page.svelte` | Shared back/header; protected account/import dialogs | module `ImportDialog.svelte`; import API covered by `e2e/financial-workflows.spec.ts` |
| `[org]/reports/+page.svelte` | Shared filter surfaces; financial tables, grouping, CSV retained | `+page.test.ts`, `e2e/financial-workflows.spec.ts` |
| `[org]/settings/+page.svelte` | Grouped draft with Save/Cancel and navigation protection | `+page.svelte.test.ts`, `lib/test/editors/settings-page.yaml`, `e2e/financial-workflows.spec.ts` |
| `[org]/settings/members/+page.svelte` | Existing stable background-refresh list; protected invite dialogs | `+page.test.ts`, module invite/table tests |
| `portal/+page.svelte` | Shared header; picker/single-org redirect retained | `e2e/portal.spec.ts` |
| `portal/[org]/invoices/+page.svelte` | Shared table/format/state layout; numeric alignment | `e2e/portal.spec.ts` |
| `portal/[org]/invoices/[id]/+page.svelte` | Shared back/header/table; restricted customer invoice view retained | `+page.svelte.test.ts`, `e2e/portal.spec.ts` |
| `portal/[org]/leases/+page.svelte` | Read-only lease cards retained; scoped response cleanup | route review; `e2e/portal.spec.ts` validates isolation/auth |
| `login/+page.svelte` | Shared fields/actions/feedback; pending-request input locks | `+page.test.ts`, all browser suites |
| `auth/magic/+page.svelte` | Compact status/error layout; one-use token handling retained | `+page.test.ts`, `e2e/portal.spec.ts` |
| `invite/[token]/+page.svelte` | Compact shared-field form; existing invalid/expired states retained | `+page.test.ts`, `e2e/invite.spec.ts` |

## New-screen checklist

- [ ] Choose one of the layouts above and reuse its existing components.
- [ ] Declare module/permission gates and keep server authorization intact.
- [ ] Define view identity and invalidate reads, writes, and uploads on context change/unmount.
- [ ] Cover initial loading, retryable error, empty data, and no filter matches.
- [ ] Keep filter/pagination semantics honest; preserve existing URL navigation.
- [ ] Provide explicit submit/cancel, duplicate-submit guards, and dirty/busy protection.
- [ ] Preserve failed input; separate write success from refresh/navigation failure.
- [ ] Register every editor in `web/src/lib/test/editors/` so the shared contract checks the two items above.
- [ ] Keep warnings, financial safeguards, and recovery links visible.
- [ ] Check labels, descriptions, headings, focus, keyboard access, narrow viewport, and both themes.
- [ ] Add behavior tests in the layer that owns them (`docs/testing.md`); add a real-browser check only for behavior a browser alone can show.
- [ ] Add the route and proof to the coverage ledger; explain any layout exception.
- [ ] Run repository Taskfile checks sequentially; record any blocked verification honestly.

Use `uv run --locked task web:test`, `uv run --locked task lint`, and
`uv run --locked task web:e2e`. See README for local prerequisites and isolated
browser database safety. Do not run those tasks concurrently.
