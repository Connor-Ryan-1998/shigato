# Web Test Harness Implementation Plan (spec slices 5–8)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the per-file Vitest boilerplate with shared setup and
helpers. Run every editor's contract checks from YAML. Cut Playwright to
connection paths and browser-only checks on a shared bootstrap. Document who
owns each kind of test.

**Architecture:**
- **Shared setup:** `web/src/lib/test/setup.ts` is added to Vitest
  `setupFiles`. It installs:
  - the dialog polyfill;
  - a global mock of `lib/api`, driven by `mockApi(routes)`;
  - global mocks of `$app/state` and `$app/navigation`, driven by `setPage()`
    and a `goto` spy.

  A test that needs the real API client opts out with `vi.unmock('<relative path to lib/api>')`.
- **Editor contract:** `web/src/lib/test/editors.test.ts` loads
  `web/src/lib/test/editors/*.yaml`. It resolves each component through a
  typed registry and runs one assertion function per contract key.
- **Playwright:** `web/e2e/stack.ts` owns database creation, the server
  process, `psql`, `api` and login. The specs keep only connection paths and
  browser-only checks.

**Tech Stack:** Svelte 5, SvelteKit, Vitest 5, Testing Library, Playwright
1.65, and the `yaml` npm package (new devDependency).

**Spec:** `docs/superpowers/specs/2026-11-13-test-harness-design.md`

## Global Constraints

- **No commits.** Leave every change uncommitted for the user to review. The
  ledger records what each task changed, not commit SHAs.
- **Product code:** don't change it. If a harness exposes a real product bug,
  stop and report it.
- **YAML editor format:** no loops, conditionals or expressions. A missing
  contract key needs a `skip:` entry with a reason.
- **Failure messages:** name the YAML file, the editor `id` and the contract
  key.
- **Assertions:** change none when moving boilerplate. Every deleted test maps
  to a surviving owner, or is recorded as an intentional drop with a reason
  (ledger + final report).
- **Fixtures:** pin explicitly every fixture value an assertion depends on.
- **Validation:** run sequentially:
  1. `task web:test` (`npm run check`, then `vitest --run`)
  2. `task web:e2e` (builds `bin/shigoto` and needs compose Postgres and
     Chromium)
  3. `go test ./...`
- **Databases:** never point at production or operator databases.
- **Reduction:** cut lines as a guide; "one owner per behaviour, nothing lost"
  wins.

## Review Focus

1. **Mock ownership:** a test that forgets `vi.unmock` but needs the real API
   client must fail loudly. A mocked `api` returning `undefined` must not let
   it pass. `mockApi` throws on an unrouted call, naming the method and path.
2. **Mock leakage:** mock state must not leak between tests. Routes, the `goto`
   spy, the page params and `confirm` are reset in a global `beforeEach`.
3. **Contract keys:** an editor YAML key that the runner doesn't know (a typo
   such as `blocks_duplicat_submit`) must fail the load. It must not silently
   skip a check.
4. **Duplicate submit:** the check must hold the first request pending. A
   resolved first request would let a second call happen legitimately and
   hide the bug.
5. **Failed-input preservation:** after a rejected save, the check must read
   the field's current value. Seeing that the error text appeared is not
   enough.

---

### Task 1: Shared Vitest setup and helpers

**Files:** Create:
- `web/src/lib/test/setup.ts`
- `web/src/lib/test/app.svelte.ts`
- `web/src/lib/test/api.ts`
- `web/src/lib/test/session.ts`
- `web/src/lib/test/harness.test.ts`

Modify: `web/vite.config.ts` (`setupFiles`).

**Interfaces (produces):**
- **`api.ts`:**
  - `apiMock`: the `vi.fn` that the global mock's `api` delegates to.
  - `mockApi(routes: Record<string, unknown | ((body: unknown) => unknown)>)`.
    Keys are `'METHOD /full/path'`. A value can be:
    - data, which resolves;
    - `apiError(status, message)`, which rejects with the mocked `ApiError`;
    - `pending()`, a never-resolving promise;
    - `deferred()`, which returns `{ promise, resolve, reject }`.

    An unrouted call throws `unrouted api call: METHOD path`.
  - The mocked `ApiError` class has the same constructor as the real one.
  - `jsonResponse(status, body)` for tests that stub `fetch`.
- **`app.svelte.ts`:**
  - `page`: `$state({ params: {}, url: new URL('http://localhost/') })`.
  - `setPage({ params?, url? })`.
  - `goto`: a `vi.fn`.
  - `beforeNavigate` / `afterNavigate`: no-op spies that record their callbacks.
- **`session.ts`:** `signIn({ role, org?, orgs?, perms? })` calls `session.setMe`
  and `session.setOrg`. It uses sensible defaults: org `acme`, and the
  permissions for each role taken from the real role map, where the web app
  exposes one. Otherwise the permissions are an explicit list.
- **`setup.ts`:**
  - The `HTMLDialogElement` `showModal`/`close` polyfill (sets and removes
    `open`).
  - `vi.mock('../api', ...)` with relative paths. The `$lib` alias fails in
    setup files.
  - `vi.mock('$app/state')` and `vi.mock('$app/navigation')`.
  - A `beforeEach` that resets routes, the spies and the page, and sets
    `window.confirm` to return `true`.

Steps:
- [ ] **Step 1: write the failing tests** in `harness.test.ts`:
  - `mockApi resolves routed data`
  - `mockApi rejects with ApiError status and message`
  - `unrouted call throws naming method and path`
  - `routes reset between tests` (two `it`s)
  - `setPage updates params read through $app/state`
  - `dialog polyfill opens and closes`
- [ ] **Step 2:** run `npx vitest --run src/lib/test` and confirm it fails.
- [ ] **Step 3:** implement, then run it again and confirm it passes.
- [ ] **Step 4:** run `task web:test` and confirm it passes. Existing files keep
  their own mocks, which override the global one.

### Task 2: Move every Vitest file onto the shared setup

**Files:** every `web/src/**/*.test.ts`.

For each file:
- Delete the local dialog polyfill, the `vi.mock` of `lib/api`, the
  `$app/state` and `$app/navigation` mocks, the `route()` dispatchers and the
  `jsonResponse` copies. Use `mockApi`, `setPage` and `signIn` instead.
- Files that test the real API client use `vi.unmock` and keep their `fetch`
  stubs: `api.test.ts`, `Dialog.test.ts`, `ReloginDialog.test.ts`,
  `login/+page.test.ts`, `invite/[token]/+page.test.ts` and
  `billing-run/+page.svelte.test.ts`.
- Assertions are unchanged.
- The test count stays the same: compare the `vitest --run` totals before and
  after.
- `format.test.ts` and the other pure tests: convert to a YAML table only if
  that removes code. Otherwise leave them, with a ledgered ruling.
- [ ] Record the test count before the change. Migrate the files in batches
  by directory, running `npx vitest --run <dir>` after each batch. Finish
  with `task web:test` green and the same total.

### Task 3: YAML editor contract

**Files:** Create:
- `web/src/lib/test/contract.ts` (one exported check per key)
- `web/src/lib/test/editors.registry.ts`
- `web/src/lib/test/editors.test.ts`
- `web/src/lib/test/editors/*.yaml`

Add the `yaml` devDependency.

**YAML shape:**

```yaml
id: contacts-edit                 # unique
component: ContactsPage           # registry key
props: {}
page: {org: acme}
session: {role: owner}
api:                              # mockApi routes used for the initial render
  GET /api/v1/orgs/acme/contacts: [{id: 7, kind: tenant, name: Tina Tenant}]
open: Edit Tina Tenant            # button to click first (optional)
fill: {Name: Corrected}           # label -> value (exact label)
submit: Save contact              # submit button name
request: PUT /api/v1/orgs/acme/contacts/7
contract:
  blocks_duplicate_submit: true   # first request pending; second submit makes no call
  preserves_input_on: 422         # rejected save keeps every filled value
  cancel: Cancel                  # closes the editor with no write
  busy_dismiss_blocked: true      # cancel event prevented while pending
  dirty_dismiss_confirms: true    # dirty cancel asks window.confirm; refusal keeps it open
skip:
  dirty_dismiss_confirms: page editor; covered by navigation guard test
```

**Rules:**
- Unknown top-level or `contract` keys fail the load (Review Focus 3).
- Every known key is either in `contract` or in `skip`, with a reason.
- Editors whose save is a prop callback (`onSubmit`) instead of an API call
  set `submit_prop: onSubmit`. The runner then injects a `vi.fn` that returns
  a pending, rejected or resolved promise in place of `request`.

Steps:
- [ ] **Step 1: write the failing tests** in `editors.test.ts`, covering the
  loader and the contract against one editor (`ProjectDialog`):
  - unknown key rejected;
  - missing key without `skip` rejected;
  - each check passes on `ProjectDialog`.
- [ ] **Step 2:** mutation check. Make `blocks_duplicate_submit` fail by
  resolving the first request (Review Focus 4). Confirm the check stays
  strict, then revert.
- [ ] **Step 3:** make the tests pass, then run `task web:test`.

### Task 4: Move editor-contract tests into YAML

Register every editor that has contract tests today:
- Contacts (create and edit)
- `ProjectDialog`
- `LogTimeDialog`
- `PaymentDialog` (props)
- Settings page
- Lease new
- Lease detail
- Expenses
- Invoice new (contact dialog)
- Billing run
- Login password form

Then delete each test, or the part of a test, that a YAML check now owns.
Tests that also assert page-specific behaviour keep that part. Ledger each
removal as `removed: <file> "<it name>" -> editors/<id>.yaml <key>`. Test
behaviours with no YAML equivalent stay as code: stale org responses, focus,
and `beforeunload`.
- [ ] Run `task web:test` green. For each editor, break one contract key in
  the YAML, confirm the test fails, then revert.

### Task 5: Playwright consolidation

**Files:** Create `web/e2e/stack.ts`. Rewrite the four specs.

**`stack.ts`:**
- `startStack({ db, orgs: [{slug, name}], owner: {email, password}, env? })`
  returns `{ base, stop, psql, api(cookie | request) }` and
  `login(page, email, password)`.
- It creates the database, runs `create-owner` for each org, spawns
  `serve` on a free port with a temp files directory, waits for health and
  cleans up.

**Keep**, merging existing tests into these 8 paths:
1. Smoke: login → dashboard → org switch.
2. Invite accept → lands in the org.
3. Portal magic link → own invoice only; reused link expired; expired
   session → `/login`.
4. Money path: log time → bill run → issue (`:287`), then CSV import →
   match → paid → P&L + CSV (`:351`).
5. Property path: property + lease → rent run → ledger (`:329`).
6. Session expires mid-save → relogin keeps the draft → issued once (`:173`).
7. Dialog focus trap/restore and the dirty-navigation guard (`:201`, `:248`).
8. No overflow at 375px in light and dark (`:275`).

**Delete:** `:37`, `:65`, `:232`, `:375`, `:401`, `:440`, `:467`, `:521`,
`:565` and `:600`, with the owners listed in the spec table.
- Before deleting each one, grep for its owner tests and confirm they still
  exist. If an owner is missing, add it in Go or Vitest first.
- `:232` (cancelled relogin → `/login` with no `beforeunload` prompt) is
  owned by `Dialog.test.ts` "does not prompt beforeunload…" and
  `api.test.ts` "falls back to the redirect…".
- [ ] Run `task web:e2e` and confirm it passes.

### Task 6: Ownership doc

- [ ] Write `docs/testing.md`:
  - the layer-ownership table from the spec;
  - how to add a Go YAML case, a Go table, a Vitest editor YAML entry and a
    Playwright path;
  - the `exact: true` denial rule;
  - the "pin values your assertions depend on" rule.
- [ ] Link it from `AGENTS.md`, in the app-level guide.

### Task 7: Final verification and review

- [ ] Run `task web:test`, then `task web:e2e`, then `go test ./...`. All
  pass.
- [ ] Count lines before and after (Vitest, Playwright and new harness).
- [ ] Run the final whole-branch review on the uncommitted diff
  (`git diff HEAD`), then one fix pass.
