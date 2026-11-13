# Declarative test harnesses

## Intent

Cut the amount of test code that has to be read and maintained. Each tested
behaviour lives in exactly one layer, and the shapes that repeat are written as
YAML cases run by one shared harness per layer. A new feature adds cases, not
bespoke test functions. The case files are also the intended surface for agents
writing tests: small, declarative, and checked by the harness.

The user's driver is maintainability (reading and maintaining tests), not
authoring speed. They approved removing cross-layer duplicate coverage.

## Current state (2026-11-13 survey)

| Layer | Files | Tests | Lines |
|---|---|---|---|
| Go (`internal/`, `cmd/`) | 58 | 420 | 17,488 |
| Vitest (`web/src`) | 44 | ~253 | 4,501 |
| Playwright (`web/e2e`) | 4 | 19 | 1,209 |

- Go has no shared fixture package. Only `db.TestPool` (`internal/db/testdb.go`)
  and `internal/auth/oidctest` are shared. Org, contact, user, membership,
  login, handler, request, invoice, property/lease, billing-service, settings and
  date helpers are copied between packages: 6 to 11 copies each, with drifting
  signatures. `sessionCookieName` is copied 6 times because the real one is
  unexported.
- About 80 Go tests (~2.8k lines) follow the pattern "request as role R in org O,
  expect status S", for authz, cross-org 404, module-disabled 404, portal
  isolation and validation. Cross-org 404 alone has 13+ hand-written variants.
- About 78 Go pure-function tests (~1.6k lines) are written one case per
  function. `internal/property/periods_test.go` has 17 single-case functions;
  `internal/config/config_test.go` has 25.
- About 132 Go workflow tests (~5.9k lines) cover bill run, rent run,
  reconciliation, payouts, webhooks, reports and sandbox. About 8 of them are
  concurrency tests that depend on `pg_locks` and goroutines.
- About 90 Vitest tests cover the editor contract, stale-response/org-switch
  races and role gating. They repeat the same boilerplate in 15 to 25 files: the
  `showModal` polyfill, `vi.mock` of `lib/api` with hoisted mocks, a `route()`
  dispatcher, `$app/state`/`$app/navigation` mocks, and `session.setMe`.
- 18 of the 19 Playwright tests re-check behaviour already asserted in Go or
  Vitest. Only the 375px overflow check, and focus trap/restore within the dialog
  test, are e2e-only.

## Goals

- One shared Go fixture package that replaces every copied helper.
- A YAML-driven HTTP case runner for the "role/org/request → status/body/no side
  effect" shape.
- YAML tables for Go pure functions where cases share one signature.
- A Vitest editor-contract harness, driven by YAML, that each dialog or editor
  page registers into, plus shared render and API-mock helpers.
- Playwright reduced to a small set of connection paths plus browser-only checks.
- An explicit layer-ownership rule, documented, so duplicates don't return.
- Rough target: 40 to 50% fewer test lines, with every behaviour still owned by
  one layer.

## Non-goals

- No change to product code, except minimal test seams (for example, exporting
  the session cookie name, or `export_test.go` shims) and fixes for bugs the new
  harnesses expose (see Migration and safety).
- No new test frameworks. Keep `go test`, Vitest, Testing Library and Playwright.
- Not converting multi-step workflows or concurrency tests to YAML.
- Not changing how integration databases are isolated (schema per test via
  `db.TestPool`). Per-test migration cost is out of scope.
- No change to the Taskfile's public interface. `task test`, `web:test`,
  `web:e2e` and `verify` keep working as they do now.

## Anti-goals

- A YAML format that grows loops, conditionals, variables-with-expressions or
  step sequencing. If a case needs that, it belongs in code.
- Coverage silently lost during migration. Every deleted test maps to a
  surviving owner, or is recorded as an intentional drop.
- Case failures that don't say which YAML file, case name and expectation
  failed.
- A harness that hides the behaviour under test. A reader must still see the
  request and the expected outcome in the case.

## Approaches

1. **YAML for repeated shapes, shared code for the rest. Selected.** Shared Go
   fixtures; a YAML HTTP case runner; YAML pure-function tables; a Vitest YAML
   editor-contract suite and shared helpers; Playwright cut to connection paths.
   Workflows and concurrency stay in Go on top of the shared fixtures. Trade-off:
   two styles (YAML and code), each used where it fits.
2. **YAML everywhere, workflows as step lists.** The largest reduction on paper.
   The step format becomes a weak programming language, concurrency tests still
   can't move, and money-flow failures get harder to debug. Rejected.
3. **Typed tables in Go/TS, no YAML.** Same deduplication with compile-time
   checking. Slightly more lines, and cases can't be read without the
   surrounding code. Rejected in favour of YAML for the repeated shapes.

Per-layer harnesses were chosen over one cross-layer case format. They are
simpler and easier to debug, and each layer's case shape stays small.

## Layer ownership rule

Document this in `docs/testing.md` and link it from `AGENTS.md`:

| Behaviour | Owner |
|---|---|
| Permissions, org isolation, portal isolation, module gating, validation status codes | Go HTTP cases (YAML) |
| Money, periods, parsing, config, formatting rules | Go or TS pure tables (YAML), in the language that implements the rule |
| Store invariants and multi-step financial workflows, concurrency | Go code using `testkit` |
| Editor contract: failed-input preservation, duplicate submit, cancel, dirty/busy dismissal, stale-response/org-switch | Vitest editor contract (YAML) |
| UI role gating (control hidden/disabled for a role) | Vitest, as a field of the same YAML suite |
| Page-specific rendering of API data | Vitest code using shared helpers |
| Pieces connected end to end; real browser behaviour (focus, layout, navigation guards, session expiry) | Playwright |

When a behaviour is asserted in more than one layer, keep it in its owner and
delete the rest. Exception: a Playwright connection path may pass through a
behaviour without being its owner, as long as it doesn't re-assert edge cases.

## Go design

### `internal/testkit` (shared fixtures)

A new non-`_test` package, compiled only under `//go:build integration` where
it touches the database. It provides:

- `testkit.New(t) *Env`: wraps `db.TestPool`, and owns the pool and the fake
  mailer/payments/billing deps. No product clock seam is added: only
  `auth.WithClock` is injectable today, and `testkit` exposes just that. Seeds
  and workflow tests pass explicit dates, as they do now. *(Builder call.)*
- Builders with options, one per entity, replacing every copied helper:
  `Org(slug)`, `Contact(org, opts...)`, `User(email, opts...)`,
  `Member(user, org, role)`, `Login(user) Session`, `Invoice(org, contact,
  opts...)` (draft/issued/source/amount), `Property`, `Lease`, `Project`,
  `Entry`, `Settings(org, opts...)` (GST, pay options), `Date("2026-01-31")`.
  Options use functional-option style, so one signature covers every current
  variant.
- `Env.Handler(opts...)`: builds `httpapi.New(httpapi.Deps{...})` with module
  toggles, mailer, payments and billing overrides.
- `Session.Do(method, path, body) *Response`: sets the cookie and the
  `X-Shigoto-CSRF` header and decodes JSON. Replaces the ~12 request helpers.
- `Env.Snapshot(tables...)`: a JSON snapshot of the named tables plus the audit
  count, for "no side effect" assertions. Generalises
  `internal/httpapi/security_integration_test.go`.
- Count helpers, replaced by `Env.Count(table, where...)`.

**Import cycles.** `testkit` imports `core`, `auth` and `httpapi`, so only
external test packages (`package x_test`) can use it. Integration tests now in
internal packages (`package auth`, `httpapi`, `property`, `sandbox`, and
others) move to `x_test`. Where they need unexported symbols, add minimal
`export_test.go` shims. Export `auth.SessionCookieName` instead of copying it.

### YAML HTTP case runner

- All HTTP cases live in `internal/httpapi/testdata/cases/*.yaml`, one file per
  domain area (`invoices.yaml`, `contacts.yaml`, `property.yaml`, `members.yaml`,
  `portal.yaml`, `timesheets.yaml`, `recon.yaml`, `reports.yaml`,
  `settings.yaml`).
- One runner test, `internal/httpapi/cases_integration_test.go`
  (`package httpapi_test`), loads every file. Each file gets a fresh `testkit.Env`
  and its seed is applied once. Each case runs as `t.Run(file/case)`.
- Cases that mutate state get their own Env, flagged with `isolate: true`.
  Read-only and rejected-write cases share the file's seed.

Proposed shape:

```yaml
seed:
  orgs: [acme, foreign]
  modules: { property: true, timesheets: true }
  users:
    owner:  { org: acme, role: owner }
    keeper: { org: acme, role: bookkeeper }
    tenant: { org: acme, portal_contact: c_acme }
  contacts:
    c_acme:    { org: acme, kind: client }
    c_foreign: { org: foreign, kind: client }
  invoices:
    inv_foreign: { org: foreign, contact: c_foreign, status: issued, amount: 1000 }

cases:
  - name: foreign invoice is not found
    as: owner
    request: { method: GET, path: /api/v1/orgs/acme/invoices/{inv_foreign} }
    expect: { status: 404 }

  - name: bookkeeper cannot write contacts
    as: [keeper, tenant]            # expands to one subtest per actor
    request:
      method: POST
      path: /api/v1/orgs/acme/contacts
      body: { kind: client, name: X }
    expect: { status: 403 }
    unchanged: [contacts, audit_log]

  - name: amount above bound rejected
    as: owner
    request: { method: POST, path: /api/v1/orgs/acme/invoices, body: { ... } }
    expect:
      status: 422
      body: { error: { field: amount } }   # JSON subset match
```

Rules:

- `{name}` interpolates only seeded IDs. There are no expressions.
- `as` takes one actor or a list. A list is the only expansion mechanism. `as:
  anonymous` means no session.
- `expect.body` is a subset match. `expect.headers` is optional.
- `unchanged` snapshots the named tables before and after the request.
- The seed schema is closed: an unknown key fails the load. The seed loader maps
  onto `testkit` builders, so the YAML can't do anything `testkit` can't.
- On failure, report the file, case name, actor, request and a diff of expected
  versus actual.

YAML library: add `go.yaml.in/yaml/v3` (test-only use) with
`KnownFields(true)`.

### YAML pure-function tables

- Pattern: `testdata/<func>.yaml` next to the package, plus a small typed loader
  in the `_test.go` that maps `in` and `want` onto the function's types. There
  is no reflection-based universal runner. Each table has an explicit adapter of
  about 10 lines.
- Candidates: `property/periods_test.go`, `core/money_test.go`,
  `reports/periods_test.go`, `reports/allocate_test.go`, `config/config_test.go`,
  `auth/rbac_test.go` (the permission matrix becomes a YAML grid), and the
  repeatable `importers/csv_test.go` cases.
- Tests that don't share a signature stay as Go.

### Go code that stays

Workflows (bill run, rent run, match, payouts, webhooks, reports, bonds,
sandbox, scheduler), concurrency tests, OIDC protocol tests (17 callback
rejections become one Go table, not YAML, because they mutate `oidctest`
claims), mail/SMTP, Stripe, webui/CSP, migrations, CLI. All are rewritten onto
`testkit` and lose their local helpers. Where a test exists both at store level
and at HTTP level (for example, `TestCreateLeaseRequiresOnePrimary`), keep the
store test for invariants, and keep the HTTP status as a YAML case only if the
status code mapping is itself the behaviour.

## Vitest design

### Shared helpers: `web/src/lib/test/`

- `setup.ts`: added to `setupFiles`. Holds the `HTMLDialogElement`
  `showModal`/`close` polyfill and default `window.confirm` handling. This
  removes 17 copies.
- `api.ts`: `mockApi(routes)` drives the existing `api(method, path, body)`
  mock with a route table such as
  `{ 'GET /api/v1/orgs/acme/contacts': [...],
  'POST /api/v1/orgs/acme/contacts': error(422, {...}) }`, plus deferred responses to control races. It replaces the 22
  `vi.mock` blocks and the 8 `route()` dispatchers. `jsonResponse` replaces 6
  fetch-stub copies, for the few tests that must stub `fetch`.
- `app.ts`: `$app/state` and `$app/navigation` mocks via one module-level
  `vi.mock` in setup, with `setPage({ params })` and a `goto` spy.
- `render.ts`: `renderAs(Component, { role, org, props, api })` sets the
  session (`setMe`, `setOrg`), installs the API routes and renders.

### YAML editor contract

- Each editor (dialog or form page) gets one entry in
  `web/src/lib/test/editors/*.yaml`. A single
  `web/src/lib/test/editors.test.ts` runs every entry through the contract.
- Vite can't import YAML natively, so load it with the `yaml` npm package
  (devDependency) via `import.meta.glob('./editors/*.yaml', { query: '?raw' })`.
- Component references are resolved through a typed registry,
  `editors.registry.ts`, which maps `id` to the component import and its props.
  This keeps import paths type-checked.

Proposed shape:

```yaml
id: contacts-create
component: ContactsPage          # registry id; a dialog or a route page
props: {}
page: { org: acme }
api:                             # same keys as mockApi; full /api/v1 paths
  GET /api/v1/orgs/acme/contacts: []
  submit: POST /api/v1/orgs/acme/contacts
open: New contact                # button to click first, if the editor isn't open on render
fill:                       # label -> value, via getByLabelText
  Name: Jane
  Email: jane@example.com
submit: Save
contract:
  preserves_input_on: 422          # failed response keeps typed values
  blocks_duplicate_submit: true
  cancel: Cancel
  dirty_dismiss_confirms: true
  busy_dismiss_blocked: true
  stale_org_response_ignored: true # only for org-scoped editors
roles:
  hidden_for: [bookkeeper, tenant]
```

- Each `contract` key maps to one reusable assertion function in
  `contract.ts`. A missing key means "not applicable" and must be justified in
  a `skip:` field with a reason. That makes the AGENTS.md editor rules
  checkable.
- Page-specific rendering tests stay as code, written with `renderAs` and
  `mockApi`.
- `format.test.ts`, and similar pure tables, move to `testdata/*.yaml` using
  the same per-function adapter pattern as Go.

## Playwright design

- Keep a shared `web/e2e/fixtures.ts`: database and server bootstrap, login,
  and the session-expiry helper now inlined per spec.
- Keep, consolidated:
  1. Staff login → dashboard → org switch (smoke).
  2. Invite accept → lands in org.
  3. Portal magic link → own invoice; reused link expired; expired session
     redirects.
  4. Money path: log time → bill run → issue; CSV import → match → paid → P&L.
  5. Property path: property + lease → rent run → ledger.
  6. Session expires mid-save → relogin keeps draft → issued once.
  7. Dialog focus trap/restore and dirty-navigation guard (browser-only).
  8. 375px overflow in light and dark.
- Slice 7 is cut and merge only. Paths 4–7 are built by merging the existing
  tests named below (`financial-workflows.spec.ts:287,351` → 4; `:329` → 5;
  `:173` → 6; `:201,248` → 7). No new end-to-end flows are written. *(Builder
  call.)*
- Delete the rest. Each one's edge-case assertions already have an owner:

  | Deleted test (`financial-workflows.spec.ts`) | Owner |
  |---|---|
  | `:37` contact edit/clear/cancel | `internal/httpapi/contacts_integration_test.go:17`; Vitest `contacts/+page.svelte.test.ts:49,71` |
  | `:65` expense splits, INP warning, filters | `internal/recon/expenses_integration_test.go:113,155,351,386`; Vitest `ExpenseForm.test.ts:16`, `expenses/+page.svelte.test.ts:91,101` |
  | `:232` relogin cancel → `/login` | Vitest `Dialog.test.ts:53`, `api.test.ts:192` |
  | `:375` settings BSB normalised | `internal/httpapi/settings_integration_test.go:77`, `internal/core/settings_test.go:28` |
  | `:401` contact kinds/filters | Vitest `contacts/+page.svelte.test.ts:138,148` |
  | `:440` time entry edit/filters | `internal/timesheets/entries_integration_test.go:130,327`; Vitest `LogTimeDialog.test.ts:49`, `time/+page.svelte.test.ts:68` |
  | `:467` project/task rates | `internal/timesheets/projects_integration_test.go:112,209,407`; Vitest `ProjectDialog.test.ts:35`, `projects/[id]/+page.svelte.test.ts:71` |
  | `:521` lease onboarding | `internal/property/store_integration_test.go:77,246`, `rentrun_integration_test.go:400`; Vitest `leases/new/+page.svelte.test.ts:35,58,78` |
  | `:565` billing-run failure link | `internal/timesheets/billrun_integration_test.go:506`; Vitest `billing-run/+page.svelte.test.ts:27,37` |
  | `:600` rent-change warnings | `internal/httpapi/property_warnings_integration_test.go:124,262`; Vitest `leases/[id]/+page.svelte.test.ts:55,66` |

  Line numbers are from the 2026-11-13 survey. Before deleting, re-check that
  each assertion still has an owner, and add the missing owner case first.

## Migration and safety

- Roll out in independent slices. Each slice lands green on its own:
  1. `testkit` + `auth.SessionCookieName` export; migrate one package (`core`)
     to prove it.
  2. HTTP YAML runner; move cross-org/authz/module/validation cases from all
     packages.
  3. Remaining Go packages onto `testkit`; move internal-package integration
     tests to `_test` packages.
  4. Go pure-function YAML tables.
  5. Vitest shared helpers + setup file.
  6. Vitest editor contract YAML; migrate editors.
  7. Playwright consolidation.
  8. `docs/testing.md` ownership rule + AGENTS.md link.
- **Product bugs found by the new harnesses** get fixed in a separate commit
  within the same slice. That commit comes before the slice's final commit and
  is described as a fix, so the slice still lands green and the product change
  can be reviewed apart from the test refactor. No known-failure or skip
  markers for real bugs. If a fix would need a design decision (for example,
  changing what a role may do), stop and ask instead of guessing. *(Decided by
  user.)*
- **Coverage ledger.** Each slice's plan lists the tests it removes, and for
  each one names its new owner (YAML case, test name) or records it as an
  intentional drop with a reason. The ledger goes in the slice's commit message
  or in the plan.
- **Mutation spot-check.** For each new YAML runner, temporarily break one
  guarded behaviour (for example, remove an org check) and confirm the case
  fails, then revert. This proves the runner actually asserts.
- Run Go coverage before and after each Go slice, with
  `-coverpkg=./internal/... -tags integration` and unit plus integration
  profiles merged. This lets HTTP YAML cases count toward the domain package
  they exercise. Compare per-package statement coverage of the merged profile.
  A drop means the slice is incomplete unless the ledger explains it.
  *(Builder call.)*
- The 40–50% line reduction is a guide, not an acceptance criterion. "One owner
  per behaviour, nothing lost" wins when they conflict. *(Builder call.)*
- Validation follows the existing Taskfile, run sequentially: `task test`,
  `task test SUITE=integration`, `task web:test`, `task web:e2e`.

## Decided

- Approach 1, separate harness per layer, YAML only for repeated shapes.
- Cross-layer duplicates are cut to one owner.
- The purpose is maintainability, and case files are meant to be agent-writable.
- Product bugs exposed by the harnesses get a separate fix commit in the same
  slice.

## Left to the builder

- Exact names of `testkit` options and the YAML field spellings, within the
  shapes above.
- Whether a given HTTP case file needs `isolate` per case or can share seed.
- Which page-render Vitest tests collapse into `renderAs` one-liners versus stay
  bespoke.
- Final slice boundaries, if a slice proves too large to land green.
