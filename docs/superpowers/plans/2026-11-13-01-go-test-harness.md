# Go Test Harness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace copied Go test helpers with a shared `internal/testkit`, move
request/role/status tests into YAML cases run by one runner, turn single-case
pure-function tests into YAML tables, and delete the duplicates. Do this
without losing any covered behaviour.

**Architecture:**
- `internal/testkit` is an integration-tagged package. It owns the database
  pool, entity builders, handler construction and session requests.
- `internal/testkit/table` is an untagged generic YAML table loader for unit
  tests.
- One runner in `internal/httpapi` (`package httpapi_test`) loads
  `internal/httpapi/testdata/cases/*.yaml`. It seeds through `testkit` and
  asserts status, body subset and unchanged tables.
- Workflow and concurrency tests stay as Go, rewritten onto `testkit`.

**Tech Stack:** Go 1.27, pgx v5, `go.yaml.in/yaml/v3`, Postgres via
`docker compose`, Taskfile.

**Spec:** `docs/superpowers/specs/2026-11-13-test-harness-design.md`

This is plan 1 of 3. Plan 2 covers Vitest (spec slices 5–6). Plan 3 covers
Playwright and docs (spec slices 7–8).

## Global Constraints

- Change product code only for test seams (exporting `auth.SessionCookieName`,
  `export_test.go` shims) and for fixes to bugs the harness exposes.
- **Bug fixes:** a bug found by a new case gets its own `fix:` commit inside the
  same task, before the task's final commit. Never use skip or known-failure
  markers for real bugs. If the fix needs a product decision (for example, what
  a role may do), stop and ask the user.
- The YAML formats have no loops, conditionals, expressions or step sequencing.
  The only expansion is a list under `as:`. The only interpolation is
  `{seedName}`, which resolves to a seeded ID.
- Every YAML decode uses `KnownFields(true)`, so an unknown key fails the load.
- Failure messages name the YAML file, case name, actor and request. They also
  show expected versus actual.
- Keep the Taskfile's public interface unchanged: `task test`,
  `task test SUITE=integration`, `task verify`.
- Run validation sequentially, never in parallel: `task test`, then
  `task test SUITE=integration`. Integration tests need `docker compose` Postgres.
- Never point tests at production or operator databases.
- **Coverage ledger:** every task that deletes tests lists, in its commit body,
  one line per removed test: `removed: <pkg>.<TestName> -> <owner>`. The owner
  is a YAML file and case name, or a surviving Go test. An intentional drop is
  written as `-> dropped: <reason>`.
- **Coverage gate:** after each deleting task, merged per-package statement
  coverage (Task 1 command) must not fall below the Task 1 baseline. A fall is
  allowed only when the ledger explains it.
- The 40–50% line reduction is a guide. "One owner per behaviour, nothing lost"
  wins when the two conflict.

## Review Focus

1. A typo in an interpolated name (`{inv_foriegn}`) must fail the load and name
   the token. It must never be sent as a literal path segment, which would
   return a false 404 and pass a "not found" case. Tested in Task 5.
2. A YAML integer `1000` must match a JSON number `1000` in body subset matching.
   A string `"1000"` must not match it. Tested in Task 5.
3. A successful write (non-GET method with an expected 2xx) in a shared-seed file
   must be rejected at load unless it has `isolate: true`. Otherwise later cases
   see mutated state. Tested in Task 5.
4. An `as: anonymous` case must send no cookie and still carry the CSRF header.
   It must produce the server's real 401, not a runner panic. Tested in Task 5.
5. `Snapshot` or `Count` on a table that doesn't exist must fail the test with
   the table name. An empty snapshot must not make `unchanged` pass trivially.
   Tested in Task 2.

---

## File Structure

| Path | Responsibility |
|---|---|
| `internal/auth/middleware.go` | Export `SessionCookieName` (rename `sessionCookieName`). |
| `internal/testkit/table/table.go` | `Load[T]`: decode `testdata/*.yaml` into a typed case slice (untagged). |
| `internal/testkit/env.go` | `Env`, `New`, `Handler`, `HandlerOption`s (integration-tagged). |
| `internal/testkit/people.go` | `Org`, `User`, `Member`, `PortalUser`, `Login`, `Session.Do`. |
| `internal/testkit/records.go` | `Contact`, `Invoice`, `Property`, `Lease`, `Project`, `Task`, `Entry`, `Settings`, `Date`, `BillingService`. |
| `internal/testkit/db.go` | `Snapshot`, `Count`. |
| `internal/testkit/testkit_integration_test.go` | Tests for the above. |
| `internal/httpapi/casefile/casefile.go` | Case file types, load-time validation, interpolation, actor expansion, subset match (untagged, unit-testable). |
| `internal/httpapi/casefile/casefile_test.go` | Unit tests for the above. |
| `internal/httpapi/cases_integration_test.go` | Runner: seeds via `testkit`, executes cases. |
| `internal/httpapi/testdata/cases/*.yaml` | HTTP cases, one file per area. |

---

### Task 1: Coverage baseline and YAML table loader

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/testkit/table/table.go`
- Test: `internal/testkit/table/table_test.go`, `internal/testkit/table/testdata/sample.yaml`

**Interfaces:**
- Produces: `func Load[T any](t testing.TB, path string) []T`. It reads the file,
  decodes a top-level sequence with `KnownFields(true)`, and calls `t.Fatalf`
  with the path and the decoder error on failure.

- [ ] **Step 1: Record the coverage baseline.** Run, with compose Postgres up:

```bash
docker compose up -d --wait db mailpit
SHIGOTO_TEST_DATABASE_URL=postgres://shigoto:shigoto@localhost:5432/shigoto_test?sslmode=disable \
  go test -tags integration -count=1 -timeout=15m -coverpkg=./internal/... -coverprofile=/tmp/shigoto-cov.out ./...
go tool cover -func=/tmp/shigoto-cov.out | awk '{split($1,a,"/"); pkg=a[1]"/"a[2]"/"a[3]; sub(/%/,"",$NF); s[pkg]+=$NF; n[pkg]++} END {for (p in s) printf "%s %.1f\n", p, s[p]/n[p]}' | sort > /tmp/shigoto-cov-baseline.txt
wc -l internal/**/*_test.go cmd/**/*_test.go | tail -1 >> /tmp/shigoto-cov-baseline.txt
```

Expected: every test passes. The baseline file lists one line per package, plus
the total test-line count. Later tasks rerun the same two commands into
`/tmp/shigoto-cov-now.txt` and run `diff` against the baseline. Don't commit
these files.

- [ ] **Step 2: Write the failing test.** Write `TestLoadDecodesCases`: `sample.yaml`
  has two `{name, in, want}` entries. Assert `len == 2`, then assert the
  second entry's `want`.

  Write `TestLoadRejectsUnknownKey`: use a fake `testing.TB` that records
  `Fatalf`. Load a temp file containing `- {name: a, wnat: 1}`. Assert that
  `Fatalf` was called and that the message contains both the path and `wnat`.

- [ ] **Step 3: Run it.** `go test ./internal/testkit/table/` → FAIL (package missing).
- [ ] **Step 4: Implement `Load[T]`.** Run `go get go.yaml.in/yaml/v3`.
- [ ] **Step 5: Run it.** `go test ./internal/testkit/table/` → PASS.
- [ ] **Step 6: Commit.** `test: add YAML table loader`

---

### Task 2: `testkit` core: env, people, sessions, snapshots

**Files:**
- Modify: `internal/auth/middleware.go:20` and every in-package use. Rename to
  `SessionCookieName` with the same value, `"shigoto_session"`.
- Create: `internal/testkit/env.go`, `people.go`, `db.go`. All start with
  `//go:build integration`.
- Test: `internal/testkit/testkit_integration_test.go`

**Interfaces:**
- Consumes: `db.TestPool`, `core.CreateOrg`, `auth.New`, `(*auth.Auth).StartSession`,
  `httpapi.New(httpapi.Deps{...})`, `payments.Fake`
- Produces:
  - `type Env struct { T testing.TB; Pool *pgxpool.Pool; Ctx context.Context }`
  - `func New(t testing.TB) *Env`
  - `func (e *Env) Org(slug string) core.Org`. The name is `slug + " org"`, as in
    the current helpers.
  - `type User struct { ID int64; Email string }`
  - `func (e *Env) User(email string, opts ...UserOption) User`. Option:
    `WithPassword(pw)`. The default is passwordless (`password_hash NULL`).
  - `func (e *Env) Member(u User, org core.Org, role auth.Role)`
  - `func (e *Env) PortalUser(email string, org core.Org, role auth.Role, contactID int64) User`.
    The role is `auth.Tenant` or `auth.Client`; the membership row carries
    `contact_id`.
  - `type Session struct { Token string; env *Env; h http.Handler }`
  - `func (e *Env) Login(u User, h http.Handler) Session`. It uses
    `StartSession` and reads the `auth.SessionCookieName` cookie. If portal
    routes reject sessions made this way, fall back to magic-link verify, the
    way `portal_integration_test.go:162` does. This is checked by the test in
    Step 1.
  - `func (e *Env) Anonymous(h http.Handler) Session`. `Token` is empty and no
    cookie is sent.
  - `type Response struct { Code int; Header http.Header; Body []byte }`, plus
    `func (r Response) JSON(v any)`. `JSON` fails the test, showing the body,
    on a decode error.
  - `func (s Session) Do(method, path string, body any) Response`. It sets
    `Content-Type: application/json` and `X-Shigoto-CSRF: 1`, and sets the
    cookie when `Token` isn't empty. The body is JSON-encoded unless it is
    `nil` or an `io.Reader`, which is sent raw.
  - `func (e *Env) Handler(opts ...HandlerOption) http.Handler`. The defaults are
    every `config.Known` module enabled, `GetEnv` returning `""`, and
    `auth.New(pool, false)`. Options: `Modules(map[string]bool)`,
    `Mailer(*mail.Mailer)`, `Payments(func(string) (payments.Provider, error))`,
    `Billing(*billing.Service)`, `AuthOptions(...auth.Option)`.
  - `func (e *Env) Snapshot(tables ...string) string`. It returns the jsonb
    array of each table ordered by its first column, as text.
  - `func (e *Env) Count(table string, where string, args ...any) int`

- [ ] **Step 1: Write the failing tests:**
  - `TestLoginOwnerCanReadOrg`: owner `GET /api/v1/orgs/acme/contacts` → 200.
  - `TestPortalLoginReachesPortal`: tenant `GET /api/v1/portal/acme/invoices`
    → 200. Use the portal route path from `internal/httpapi/server.go`.
  - `TestAnonymousGets401`.
  - `TestModulesOptionDisables`: `Modules{"property": false}` →
    `/api/v1/orgs/acme/properties` returns 404.
  - `TestSnapshotDetectsChange`: insert a contact; the snapshots differ.
  - `TestSnapshotUnknownTableFails`: use a recording `testing.TB`; assert the
    `Fatalf` message contains `no_such_table`.
  - `TestCountWhere`.
- [ ] **Step 2: Run it** (Task 1 integration command, `PACKAGES=./internal/testkit/...`) → FAIL.
- [ ] **Step 3: Implement**, including the cookie rename. Run `go build ./...` to
  catch every renamed use in `internal/auth`.
- [ ] **Step 4: Run it** → PASS. Also run `task test` → PASS.
- [ ] **Step 5: Commit.** `test: add shared testkit env, sessions and snapshots`

---

### Task 3: `testkit` records

**Files:**
- Create: `internal/testkit/records.go` (integration-tagged)
- Test: extend `internal/testkit/testkit_integration_test.go`

**Interfaces:**
- Consumes: `core.CreateContact`, `core.CreateDraftInvoice`, `core.IssueInvoice`,
  `property.CreateProperty`, `property.CreateLease`, `timesheets.CreateProject`,
  `timesheets.CreateTask`, `timesheets.CreateEntry`, `billing.Service`
- Produces:
  - `func (e *Env) Contact(org core.Org, opts ...ContactOption) core.Contact`.
    Defaults: `Kind: "client"`, `Name: "Test Contact"`. Options: `Kind(k)`,
    `Name(n)`, `Email(e)`.
  - `func (e *Env) Invoice(org core.Org, contactID int64, opts ...InvoiceOption) core.Invoice`.
    Defaults: one `N-T` line totalling 1000 cents, `Source: "manual"`,
    `DueInDays: 14`, draft. Options: `Amount(cents int64)`, `Source(s)`,
    `Issued(today time.Time)`. `Issued` issues the invoice in its own
    transaction.
  - `func (e *Env) Property(org core.Org, opts ...PropertyOption) property.Property`.
    Defaults: `Name: "1 Test St"`, `Address: "1 Test St, Testville"`,
    `Residential: true`. Options: `Commercial()`, `PropertyName(n)`.
  - `func (e *Env) Lease(org core.Org, propertyID, tenantContactID int64, opts ...LeaseOption) property.Lease`.
    Defaults: `StartDate: 2026-01-05`, `Weekly`, `RentAmount: 50000`, a single
    primary tenant. Options: `Start(d)`, `Frequency(f)`, `Rent(c)`,
    `CoTenant(contactID)`, `PaidTo(d)`, `Arrears(c)`, `Bond(c)`.
  - `func (e *Env) Project(org core.Org, clientID int64, opts ...ProjectOption) timesheets.Project`
    and `func (e *Env) Task(org core.Org, projectID int64, opts ...TaskOption) timesheets.Task`.
    Use the defaults from the current `testProject` in
    `internal/timesheets/projects_integration_test.go:52`.
  - `func (e *Env) Entry(org core.Org, userID int64, entry timesheets.Entry, today time.Time) timesheets.Entry`
  - `func (e *Env) Settings(org core.Org, opts ...SettingsOption)`. Options:
    `GSTRegistered(bool)` and `PayOptions()`. `PayOptions()` sets BSB
    `062-000`, account `12345678` and PayID `owner@example.com`, the exact
    values from `billrun_integration_test.go:46`.
  - `func Date(s string) time.Time`: parses `2006-01-02` in UTC and panics on a
    bad literal.
  - `func (e *Env) BillingService(opts ...BillingOption) *billing.Service`. The
    default has no provider (returns `payments.ErrNoProvider`) and is UTC.
    Option: `Provider(payments.Provider)`.

- [ ] **Step 1: Write the failing tests:**
  - `TestInvoiceIssuedHasNumber`
  - `TestLeaseDefaultsWeeklyPrimary`
  - `TestSettingsPayOptionsAllowIssue`: issue through `BillingService` → no error.
  - `TestDateParsesUTC`
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** → PASS.
- [ ] **Step 5: Commit.** `test: add testkit record builders`

---

### Task 4: Prove `testkit` on `core`

**Files:**
- Modify: `internal/core/invoices_integration_test.go`. Delete its local
  helpers: `createOrg`, `createContact`, `createUser`, `addMembership`,
  `createDraft`, `createIssuedInvoice`, `sessionCookieName`, and the inline
  handler construction at `:548,579,631,652`.

- [ ] **Step 1:** Rewrite every test in the file onto `testkit`. Change no
  assertions.
- [ ] **Step 2: Run** `task test SUITE=integration PACKAGES=./internal/core/...` → PASS.
  The test count must equal the count before the change (`go test -v | grep -c '^--- PASS'`).
- [ ] **Step 3:** Rerun the coverage commands and diff against the baseline.
  `internal/core` must be at or above it.
- [ ] **Step 4: Commit.** `test: move core integration tests onto testkit`
  (no ledger needed; nothing is deleted).

---

### Task 5: HTTP case file format and runner

**Files:**
- Create: `internal/httpapi/casefile/casefile.go`, `casefile_test.go`
- Create: `internal/httpapi/cases_integration_test.go` (`package httpapi_test`)
- Create: `internal/httpapi/testdata/cases/smoke.yaml`. It holds one case per
  feature. Task 6 moves these cases into real case files.

**Interfaces:**
- Produces (`casefile`, untagged):

```go
type File struct {
	Path  string `yaml:"-"`
	Seed  Seed   `yaml:"seed"`
	Cases []Case `yaml:"cases"`
}
type Seed struct {
	Orgs       []string                `yaml:"orgs"`
	Modules    map[string]bool         `yaml:"modules"`    // nil: all enabled
	Users      map[string]SeedUser     `yaml:"users"`      // key is the actor name
	Contacts   map[string]SeedContact  `yaml:"contacts"`
	Invoices   map[string]SeedInvoice  `yaml:"invoices"`
	Properties map[string]SeedProperty `yaml:"properties"`
	Leases     map[string]SeedLease    `yaml:"leases"`
	Projects   map[string]SeedProject  `yaml:"projects"`
	Settings   map[string]SeedSettings `yaml:"settings"`   // key is the org slug
}
type SeedUser struct{ Org, Role, Contact string } // Contact required for tenant/client
type SeedContact struct{ Org, Kind, Name, Email string }
type SeedInvoice struct{ Org, Contact, Status, Source string; Amount int64 } // Status: draft|issued
type SeedProperty struct{ Org, Name string; Commercial bool }
type SeedLease struct{ Org, Property, Tenant string; Rent int64 }
type SeedProject struct{ Org, Client string }
type SeedSettings struct{ GSTRegistered bool `yaml:"gst_registered"`; PayOptions bool `yaml:"pay_options"` }

type Case struct {
	Name      string   `yaml:"name"`
	As        Actors   `yaml:"as"`       // scalar or list; "anonymous" allowed
	Request   Request  `yaml:"request"`
	Expect    Expect   `yaml:"expect"`
	Unchanged []string `yaml:"unchanged"`
	Isolate   bool     `yaml:"isolate"`
}
type Actors []string // UnmarshalYAML accepts a scalar or a sequence
type Request struct {
	Method  string            `yaml:"method"`
	Path    string            `yaml:"path"`
	Body    any               `yaml:"body"`
	Headers map[string]string `yaml:"headers"`
}
type Expect struct {
	Status  int               `yaml:"status"`
	Body    any               `yaml:"body"`
	Headers map[string]string `yaml:"headers"`
}

func Load(path string) (File, error)                       // KnownFields(true) + Validate
func (f File) Validate() error
func Interpolate(s string, ids map[string]int64) (string, error)
func InterpolateBody(v any, ids map[string]int64) (any, error)
func Subset(want, got any) (ok bool, diff string)          // got is decoded JSON
```

- `Validate` errors carry the file and case name. Each of these is an error:
  - an empty `Name`, or duplicate names within a file;
  - an empty `As`;
  - an actor that isn't in `Seed.Users` and isn't `anonymous`;
  - `Expect.Status == 0`;
  - a non-GET method with a 2xx status and `Isolate == false`;
  - a seed reference to an unknown org or name;
  - a tenant or client user without `Contact`;
  - a `{token}` in a path or body that isn't a seed name.
- `Interpolate` replaces `{name}` only. Any `{` that isn't a known name is an
  error naming the token.
- `Subset` normalises both sides through a JSON marshal/unmarshal round trip:
  - maps: every wanted key must be present, and each value is a subset;
  - slices: lengths must be equal, element-wise subset;
  - scalars: equal after normalisation.

  `diff` gives the JSON path of the first mismatch.

**Runner** (`TestHTTPCases`):
- For each `testdata/cases/*.yaml`: `t.Run(file)`.
- Shared seed: create a `testkit.Env`, apply the seed, build
  `Handler(Modules(seed.Modules))` and log each user in.
- For each case and actor, run `t.Run(case.Name + "/" + actor)`. An isolated case
  builds a fresh Env and seed first.
- Snapshot the `Unchanged` tables before and after the request.
- On failure, report:
  `file: case "<name>" as <actor>: <METHOD> <path>: status got X want Y\nbody: <got body>`
  or the `Subset` diff.
- Seed name → ID map: contacts, invoices, properties, leases, projects and users
  by key. Orgs are referenced by slug in paths, so they are not interpolated.

- [ ] **Step 1: Write the failing unit tests** in `casefile_test.go`. These pin
  Review Focus 1–3:
  - `TestInterpolateUnknownTokenFails`: `{inv_foriegn}` gives an error
    containing `inv_foriegn`.
  - `TestSubsetNumberNormalisation`: YAML `1000` against JSON `1000` → ok;
    `"1000"` against `1000` → not ok.
  - `TestSubsetMissingKeyDiffPath`: the diff contains `error.field`.
  - `TestSubsetArrayLengthMismatch`
  - `TestValidateRejectsSharedSeedWrite`: a POST expecting 201 without
    `isolate` gives an error naming the case.
  - `TestValidateRejectsUnknownActor`
  - `TestValidatePortalUserNeedsContact`
  - `TestLoadRejectsUnknownKey`: `expcet:`.
  - `TestActorsScalarOrList`
- [ ] **Step 2: Run** `go test ./internal/httpapi/casefile/` → FAIL. **Step 3: Implement `casefile`.** **Step 4: Run** → PASS.
- [ ] **Step 5: Write `smoke.yaml`** with these cases:
  - owner GET of a foreign invoice → 404;
  - `as: [keeper, tenant]` POST contact → 403 with `unchanged: [contacts, audit_log]`;
  - `as: anonymous` GET contacts → 401 (Review Focus 4);
  - isolated owner POST contact → 201 with body subset `{name: X}`.
- [ ] **Step 6: Implement the runner.** Run the integration suite for
  `./internal/httpapi/...` → PASS.
- [ ] **Step 7: Mutation spot-check.** Temporarily remove the org check that
  produces the cross-org 404 (in the invoice lookup handler). The `smoke.yaml`
  case must fail with the formatted message. Revert, then confirm it passes
  again. Record the result in the commit body.
- [ ] **Step 8: Commit.** `test: add YAML HTTP case runner`

---

### Task 6: Move HTTP-shaped tests into case files

Do this as three commits, one per group. Each group:
1. Writes its YAML files.
2. Runs the cases. Any real bug gets its own `fix:` commit first (see Global
   Constraints).
3. Deletes the replaced Go tests and their now-unused helpers.
4. Runs the coverage diff.
5. Commits with the ledger.

A Go test moves only if it fits the shape "seed → one request as one or more
actors → status/body/unchanged". A test needing several requests, a clock, a
fake provider's recorded calls, or concurrency stays in Go (Task 7).

| Group | Case files | Sources (survey bucket (a) and section 6) |
|---|---|---|
| 6a | `invoices.yaml`, `contacts.yaml`, `settings.yaml`, `members.yaml`, `security.yaml` | `core/invoices_integration_test.go:537,708,732` (cross-org, bounds); `httpapi/contacts_integration_test.go:102`; `httpapi/settings_integration_test.go:122`; `httpapi/members_integration_test.go:281,313,483`; `httpapi/security_integration_test.go:20,89` (the access matrix becomes `as:` lists); `httpapi/me_integration_test.go:31`; `httpapi/dashboard_integration_test.go:210,234` |
| 6b | `property.yaml`, `timesheets.yaml`, `recon.yaml`, `reports.yaml` | `httpapi/property_integration_test.go:76,110,193`; `timesheets/projects_integration_test.go:236,333`; `timesheets/billrun_integration_test.go:678,699`; `timesheets/entries_integration_test.go:236`; `recon/bank_integration_test.go:230,252`; `recon/expenses_integration_test.go:218,417`; `recon/match_integration_test.go:626`; reports route status/permission tests |
| 6c | `portal.yaml`, `auth.yaml` | `portal/portal_integration_test.go:388,905,940` and its other single-request isolation tests; `auth/auth_integration_test.go:153,183`; `auth/oidc_integration_test.go:550` (status only) |

**Duplicate store-vs-HTTP pairs (spec, "Go code that stays").** For
`TestCreateLeaseRequiresOnePrimary`, `TestCreateLeaseRejectsCrossOrgTenant`,
`TestRentChangeWarning` and `TestVoidRefusesInvoiceWithPayments`, keep the store
test. Replace the HTTP twin with a YAML case only if the status mapping is
asserted; otherwise delete it with ledger reason `store test owns invariant`.

- [ ] **Step 1 (each group):** Write the case files and run
  `task test SUITE=integration PACKAGES=./internal/httpapi/...` → PASS. Any
  real bug: write a `fix:` commit, then rerun.
- [ ] **Step 2 (each group):** Delete the replaced tests and helpers. Run
  `go vet -tags integration ./...` to catch unused helpers.
- [ ] **Step 3 (each group):** Run the full integration suite → PASS, then the
  coverage diff → no unexplained fall.
- [ ] **Step 4 (each group):** Commit `test: move <group> HTTP checks to YAML cases`
  with the ledger.

---

### Task 7: Remaining packages onto `testkit`

Rewrite the rest of the Go integration tests onto `testkit`, and delete every
copied helper listed in the spec's survey. Wherever a package needs `testkit`,
move its integration tests to an external test package (`package x_test`).
Where they need unexported symbols, add an `export_test.go` (in-package,
integration-tagged) that re-exports only what's needed, for example:
`var CreateSession = createSession`.

Do this as four commits:

| Group | Packages |
|---|---|
| 7a | `auth` (including the OIDC callback rejections, which become one Go table test — not YAML), `portal`, `mail` |
| 7b | `property`, `scheduler`, `billing` |
| 7c | `timesheets`, `recon`, `reports` |
| 7d | `httpapi` (webhooks, warnings, dashboard, members remainder), `sandbox`, `db`, `cmd/shigoto` |

Keep the concurrency tests and `waitUntilBlockedBy` as they are, apart from
their fixture calls.

The rate-limiter tests duplicate each other: `auth/ratelimit_test.go:13` (unit)
and `auth/auth_integration_test.go:318-490` (6 tests). Keep the unit test for
the limiter's own maths. Keep one integration test showing the login endpoint
is wired to the limiter. Delete the rest, with ledger reason
`limiter maths owned by ratelimit_test`.

- [ ] **Step 1 (each group):** Rewrite the tests. Change no assertions except
  removing tests that a Task 6 case or another survivor now owns.
- [ ] **Step 2 (each group):** Run the integration suite for those packages →
  PASS. The test count is unchanged, minus ledger lines.
- [ ] **Step 3 (each group):** Check that no copied helper remains:
  `grep -rnE "^func (test|create|webhookTest|new[A-Z][a-zA-Z]*TestHandler|mustDate|noProvider|setOrg|setGST|itoa)" <packages> --include=*_test.go`
  should print nothing beyond deliberate package-specific helpers. List any
  such helper in the commit body.
- [ ] **Step 4 (each group):** Run the coverage diff, then commit
  `test: move <group> onto testkit` with the ledger.

---

### Task 8: Pure-function YAML tables

Each table: `testdata/<name>.yaml`, plus a `_test.go` with a case struct, a
`table.Load[case]` call and a loop of `t.Run(c.Name, ...)`. Write one adapter
per table. Don't use reflection.

| Table | Replaces |
|---|---|
| `internal/property/testdata/periods.yaml` | the 17 single-case functions in `periods_test.go` (keep the `d`/`dp` helpers only inside the adapter) |
| `internal/core/testdata/money.yaml` | `money_test.go` |
| `internal/reports/testdata/periods.yaml`, `allocate.yaml` | `reports/periods_test.go`, `allocate_test.go` |
| `internal/config/testdata/load.yaml` | the 25 tests in `config_test.go` (case: `env map[string]string`, `want` subset of `Config` fields, or `err` substring) |
| `internal/auth/testdata/rbac.yaml` | `rbac_test.go`, as a role × permission grid |
| `internal/importers/testdata/csv.yaml` | the repeatable `csv_test.go` cases; the CommBank fixture test stays Go |

Tests that don't fit one signature stay as Go.

- [ ] **Step 1 (each table):** Write the YAML from the existing assertions, using
  exact values. Write the adapter, and run the new table test alongside the old
  tests → both PASS.
- [ ] **Step 2 (each table):** Delete the old functions. Run `task test` → PASS.
- [ ] **Step 3:** Run the coverage diff (unit packages), then commit
  `test: convert pure-function tests to YAML tables` with the ledger.

---

### Task 9: Final verification

- [ ] **Step 1:** Run `task test` → PASS.
- [ ] **Step 2:** Run `task test SUITE=integration` → PASS.
- [ ] **Step 3:** Run the coverage diff against the baseline. Every package is
  at or above baseline, or the ledger explains the fall.
- [ ] **Step 4:** Compare the test-line count with the baseline. Report the
  before and after counts, and the percentage, in the final summary. This is a
  guide, not a gate.
- [ ] **Step 5:** Repeat the Task 5 mutation spot-check on one case from each
  group in Task 6 (6a, 6b, 6c), then revert.
