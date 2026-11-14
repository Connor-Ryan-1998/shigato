# Testing

Each behaviour is tested in exactly one layer. Repeated shapes are written
as YAML cases run by one shared harness per layer. A new feature adds cases,
not new test plumbing. Design: `docs/superpowers/specs/2026-11-13-test-harness-design.md`.

## Who owns what

| Behaviour | Owner | Where |
|---|---|---|
| Permissions, org isolation, portal isolation, module gating, validation status codes | Go HTTP cases | `internal/httpapi/testdata/cases/*.yaml` |
| Money, periods, parsing, config, formatting | YAML tables in the language that implements the rule | `internal/*/testdata/*.yaml`, `web/src/lib/testdata/*.yaml` |
| Store invariants, multi-step financial workflows, concurrency | Go code on `internal/testkit` | `internal/*/*_integration_test.go` |
| Editor rules: duplicate-submit guard, failed-input preservation, cancel, busy and dirty dismissal, navigation guard | Vitest editor contract | `web/src/lib/test/editors/*.yaml` |
| UI role gating, stale-response/org-switch handling, page-specific rendering | Vitest code on the shared setup | colocated `*.test.ts` |
| Pieces joined end to end; real-browser behaviour (focus, layout, navigation, session expiry) | Playwright | `web/e2e/*.spec.ts` |

When a behaviour is asserted in two layers, keep it in its owner and delete
the other. A Playwright path may pass through a rule without re-checking its
edge cases.

## Rules for every test

- **Pin what you assert.** Set every fixture value an assertion depends on
  (amounts, GST code, residency, rates, dates, frequency). Don't rely on a
  harness default.
- **Denials leak nothing.** A 401/403/404 case whose body is only
  `{error: ...}` sets `exact: true`, so any extra field fails the case.
- **Prove a new check can fail.** Before trusting it, break the guarded
  behaviour, watch the case fail, then revert.
- **Deleting a test needs an owner.** Name the case or test that now asserts
  the same thing, or say why the check was dropped.

## Go

Run with `task test` (unit) and `task test SUITE=integration` (needs compose
Postgres).

### HTTP cases (`internal/httpapi/testdata/cases/*.yaml`)

`TestHTTPCases` runs every file. Each file declares a seed, then cases:

```yaml
seed:
  orgs: [acme, foreign]
  modules: {property: true}          # omit for all modules on
  users:
    owner:  {org: acme, role: owner}  # email: owner@example.com
    tenant: {org: acme, role: tenant, contact: c_tenant}
  contacts:
    c_tenant:  {org: acme, kind: tenant, name: Tina}
    c_foreign: {org: foreign, kind: client, name: Far}
  invoices:
    inv_foreign: {org: foreign, contact: c_foreign, status: issued, amount: 1000}

cases:
  - name: owner cannot read a foreign org's invoice
    as: owner                         # one actor, a list, or anonymous
    request: {method: GET, path: "/api/v1/orgs/acme/invoices/{inv_foreign}"}
    expect: {status: 404, body: {error: not found}, exact: true}
    unchanged: [invoices, audit_log]  # snapshot before and after
```

- **Seed kinds:** `orgs`, `modules`, `users`, `contacts`, `invoices`,
  `properties`, `leases`, `projects`, `tasks`, `invites`, `settings`. The
  loader rejects unknown keys.
- **Interpolation:** `{name}` becomes a seeded record's ID. An unknown name
  fails the load. There are no other expressions.
- **Writes:** a case that expects a successful write must set
  `isolate: true`, which gives it its own freshly seeded database.
- **Body matching:** `expect.body` is a subset match, so extra keys are
  allowed. `exact: true` requires equality.

### Shared fixtures (`internal/testkit`)

Workflow tests build data with `testkit.New(t)` and its builders, for example
`e.Org`, `e.User`, `e.Login`, `e.Invoice(org, contact, testkit.Amount(...))`,
`e.Lease`, `e.Settings(org, testkit.PayOptions())`. Requests use
`session.Do`, and "nothing changed" checks use `e.Snapshot`. Add a builder
option to testkit instead of a local helper when two packages need it.

### Tables (`testdata/*.yaml`)

Use `table.Load[T](t, path)` for single-signature cases. The loader rejects
unknown keys and missing or duplicate names. Each table has its own small
adapter, without reflection. Tests that are already compact Go tables stay
in Go.

## Vitest (`web/`)

Run with `task web:test`.

### Shared setup (`web/src/lib/test/`)

`setup.ts` runs before every file and provides:
- **API mock:** `api()` goes through `apiMock`. Route it with
  `mockApi({'GET /api/v1/...': data, 'POST ...': apiError(422, 'msg')})`,
  plus `pending()` and `deferred()` for in-flight saves and response
  ordering. An unrouted call throws.
- **Route and navigation:** `$app/state` (`setPage({params, url})`) and
  `$app/navigation` (`goto`, `beforeNavigate`, … spies).
- **Dialogs:** a `<dialog>` polyfill.
- **Confirm:** `window.confirm` returns `true` unless a test says otherwise.

Routes, route params, navigation spies, the session store and `confirm` reset
before each test, and an unrouted `api()` call fails the test even when the page
swallowed the error. A file that tests the real API client
over a stubbed `fetch` opts out with `vi.unmock('<relative path>/lib/api')`.
Use `signIn({role, perms})` for a signed-in session.

### Editor contract (`web/src/lib/test/editors/*.yaml`)

Every editor (dialog or form page) gets one file:

```yaml
id: contact-edit
component: ContactDialog              # key in editors.registry.ts
props: {open: true, org: acme, contact: {id: 7, kind: tenant, name: Tina}}
callbacks: [onsaved]                  # props given a vi.fn()
page: {org: acme}                     # route params
session: {perms: [contacts_write]}    # default: owner of acme
modules: [reconciliation]             # enabled modules, if gated
api: {GET /api/v1/orgs/acme/contacts: []}
ready: Text to wait for               # optional
open: Edit Tina                       # button that opens the editor, optional
fill: {Name: Retained, Phone: ""}     # exact label -> value ("true"/"false" ticks a checkbox)
submit: Save contact
request: PUT /api/v1/orgs/acme/contacts/7
contract:
  blocks_duplicate_submit: true       # every control disabled while saving; second submit sends nothing
  preserves_input_on: 422             # rejected save keeps values, editable and retryable
  cancel: Cancel                      # closes without a write
  busy_dismiss_blocked: true          # Cancel/Escape while saving change nothing (or name a page's Cancel)
  dirty_dismiss_confirms: true        # Escape with edits asks; refusing keeps it open
  dirty_navigation_blocked: true      # leaving with edits asks; refusing cancels
skip:
  cancel: reason it doesn't apply     # every key is checked or skipped with a reason
```

Keep page-specific behaviour (payloads, warnings, links) in the page's own
test.

## Playwright (`web/e2e/`)

Run with `task web:e2e`. It builds `bin/shigoto` and needs compose Postgres
and Chromium. Each spec calls `startStack({db, owner, orgs, env?, seed?})`
from `stack.ts`, which creates its own database and server. Keep specs to
connection paths and browser-only checks, and never point them at a
production or operator database.
