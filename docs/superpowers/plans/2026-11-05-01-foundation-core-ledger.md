# shigoto Plan 1: Foundation, Core Ledger and Stripe Invoicing

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A runnable Go binary plus Postgres. It covers orgs, staff login, RBAC, contacts, and gap-free invoices with GST maths. Invoices are sent through Stripe, and payments are recorded from verified webhooks or entered by hand (bank transfer or PayID).

**Architecture:** One `net/http` server under `/api/v1/orgs/{org}/...`. It uses `pgx/v5` with hand-written SQL and embedded migrations. Optional modules are registered from one list in `internal/httpapi`. `payments.Provider` is the only interface in this plan, with a Stripe implementation and a fake for tests. The SPA, portal, OIDC and magic links come in later plans. This plan serves only the JSON API.

**Tech Stack:**
- Go 1.27 with stdlib `net/http` routing.
- `github.com/jackc/pgx/v5`.
- `golang.org/x/crypto/bcrypt`.
- `github.com/stripe/stripe-go/v<latest major>`.
- Postgres 18, run through docker compose.
- go-task.

**Spec:** `docs/design.md`

**Plan series** (each plan ships working, tested software):
1. This plan.
2. Property: properties, leases, rent periods, bond, scheduler, QLD rules.
3. Timesheets: projects, time entries, billing runs.
4. Recon and reports: bank CSV, matching, expenses, Stripe payout fees, P&L, BAS, CSV export.
5. Web and portal: embedded SPA, OIDC for staff, magic link and password for tenants and clients, portal scoping.

## Global Constraints

- Go module path `shigoto`. Binary `cmd/shigoto`. Every env var is prefixed `SHIGOTO_`.
- Runtime dependencies are only the binary and Postgres. No Redis, queue or worker.
- Money is `int64` cents (`core.Cents`) plus a currency code (`AUD` default). Never use floats, in Go, SQL (`bigint`) or JSON (integer).
- GST codes are exactly `GST`, `FRE`, `INP` and `N-T`. GST rate is 10%.
- Every business table has `org_id`, and every query filters on it.
- Stripe is not the source of truth. Invoice number, amounts and status live in Postgres. Stripe IDs are references only.
- Webhooks are signature-verified and idempotent, keyed on the provider event ID.
- Invoice numbers are sequential per org with no gaps. A number is assigned only when the invoice is issued. Drafts have no number.
- Do not copy code or schema files from MicroRealEstate.
- Toolchain: Go, Docker and `task` (go-task). There is no Python here, so there is no uv. This revises the spec's "Docker plus uv" call; `docs/design.md` is updated in Task 1.
- Pin every container image to an exact tag in `docker-compose.yml` and `Dockerfile`.

## Review Focus

1. **Stripe redelivers the same webhook, or two copies arrive at once.** Expect exactly one payment row and no error. Tested in Task 7.
2. **Two invoices are issued at the same moment in one org.** Expect distinct consecutive numbers with no gap, including when one issue fails and rolls back. Tested in Task 6.
3. **A partial payment, then a second payment, then an overpayment.** The invoice stays `open` until the summed payments reach or pass the total, then becomes `paid`. An overpayment is stored as-is. Tested in Task 6.
4. **A user of org A requests org B's contact or invoice by ID.** Expect `404`, never `403` (which would leak that the ID exists) and never the data. Tested in Tasks 4 and 6.
5. **A webhook with a bad signature, or for an unknown org.** Expect `400` or `404` and no state change. Tested in Task 7.

---

### Task 1: Scaffold, config and health endpoint

**Files:**
- Create: `go.mod`, `cmd/shigoto/main.go`, `internal/config/config.go`, `internal/config/config_test.go`, `Taskfile.yml`, `docker-compose.yml`, `.gitignore`, `README.md`
- Modify: `docs/design.md`. In Verification, replace "Docker plus uv" with "Docker plus go-task", and replace `uv run --locked task` with `task`.

**Interfaces:**
- Produces:
  - `config.Load(getenv func(string) string) (Config, error)`.
  - `Config{Listen string; DatabaseURL string; Modules map[string]bool; FilesDir string; BaseURL string; Stripe map[string]StripeOrg}`.
  - `StripeOrg{SecretKey, WebhookSecret string}`.
  - `config.Known = []string{"property","timesheets","reconciliation"}`.

Env vars:
- `SHIGOTO_LISTEN`: default `:8080`.
- `SHIGOTO_DATABASE_URL`: required.
- `SHIGOTO_MODULES`: comma list, default all of `Known`. An unknown name is an error.
- `SHIGOTO_FILES_DIR`: default `./data/files`.
- `SHIGOTO_BASE_URL`: required.
- `SHIGOTO_STRIPE_<ORGSLUG>_SECRET_KEY` and `SHIGOTO_STRIPE_<ORGSLUG>_WEBHOOK_SECRET`. These are read later by the slug from Task 2, through `config.StripeFor(getenv, slug) (StripeOrg, bool)`. Convert the slug to upper case and replace `-` with `_`.

- [ ] **Step 1: Write failing tests**

```go
func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"SHIGOTO_DATABASE_URL": "postgres://x", "SHIGOTO_BASE_URL": "http://localhost:8080"}))
	require(t, err == nil && c.Listen == ":8080" && c.Modules["property"] && c.Modules["timesheets"] && c.Modules["reconciliation"])
}
func TestLoadRejectsUnknownModule(t *testing.T) { /* SHIGOTO_MODULES=property,payroll -> error containing "payroll" */ }
func TestLoadRequiresDatabaseURL(t *testing.T) { /* missing -> error containing "SHIGOTO_DATABASE_URL" */ }
func TestModulesSubset(t *testing.T) { /* SHIGOTO_MODULES=timesheets -> only timesheets true */ }
func TestStripeFor(t *testing.T) { /* slug "my-org" reads SHIGOTO_STRIPE_MY_ORG_SECRET_KEY; missing -> ok=false */ }
```

- [ ] **Step 2: Run `go test ./internal/config/` and confirm it FAILS. Expected: undefined `Load`.**
- [ ] **Step 3: Implement `config.Load` and `config.StripeFor`.**
- [ ] **Step 4: Implement `cmd/shigoto/main.go`.**
  - The binary has two subcommands: `serve` (the default) and `admin`. `admin` is added in Task 4.
  - `serve` loads config and listens on the configured address.
  - `GET /healthz` returns `200 ok`. Database wiring comes in Task 2.
  - Shut down gracefully on SIGTERM.
- [ ] **Step 5: Write the tooling files.**
  - `docker-compose.yml`:
    - Service `db` uses `postgres:18.<current patch>-alpine`, pinned to an exact tag. It has a healthcheck and named volume `pgdata`.
    - Service `app` builds the `Dockerfile` (added in Task 8) and depends on `db` being healthy.
    - Test database: `POSTGRES_DB=shigoto_test` is created by an init script at `deploy/initdb/01-test.sql`, which is mounted.
  - `Taskfile.yml` tasks:
    - `test`: `SUITE=unit` (the default) runs `go test -race ./...`. `SUITE=integration` runs `docker compose up -d --wait db`, then `go test -race -tags integration -count=1 ./...` with `SHIGOTO_TEST_DATABASE_URL=postgres://shigoto:shigoto@localhost:5432/shigoto_test?sslmode=disable`.
    - `build`: `go build -trimpath -o bin/shigoto ./cmd/shigoto`.
    - `dev`.
    - `lint`: `gofmt -l` must be empty, and `go vet ./...`.
  - `.gitignore`: `/bin/`, `/data/`, `.env`.
- [ ] **Step 6: Run `task test` and `task lint`. Expected: PASS, with no output from `gofmt -l`.**
- [ ] **Step 7: Commit:** `feat: scaffold shigoto binary, config and tooling`

### Task 2: Database, migrations and core schema

**Files:**
- Create: `internal/db/db.go`, `internal/db/migrate.go`, `internal/db/migrations/0001_core.sql`, `internal/db/migrate_test.go` (build tag `integration`), `internal/db/testdb.go` (build tag `integration`)
- Modify: `cmd/shigoto/main.go`. `serve` should open the pool, migrate, and then serve. `/healthz` should ping the database.

**Interfaces:**
- Consumes: `config.Config.DatabaseURL`.
- Produces:
  - `db.Open(ctx, url string) (*pgxpool.Pool, error)`.
  - `db.Migrate(ctx, pool *pgxpool.Pool) error`. It is idempotent. It takes `pg_advisory_lock(7413)`. It records applied files in `schema_migrations(name text primary key, applied_at timestamptz)` and runs each file in its own transaction, in lexical order of the embedded `migrations/*.sql`. Later plans add `0002_property.sql` and so on. Every module's migrations always run.
  - `db.TestPool(t *testing.T) *pgxpool.Pool`. It reads `SHIGOTO_TEST_DATABASE_URL` and skips the test if it is unset. It creates a fresh schema `t_<random>`, sets the pool's `search_path` to that schema, migrates, and drops the schema in `t.Cleanup`.

**`0001_core.sql`** (every money column is `bigint`; every `org_id` is `NOT NULL REFERENCES orgs`):
- `orgs`: `id bigserial pk`, `slug text unique not null`, `name`, `abn text`, `gst_registered bool not null default false`, `bas_frequency text check in ('quarterly','monthly')`, `fy_start_month smallint not null default 7`, `currency char(3) not null default 'AUD'`, `bank_bsb text`, `bank_account text`, `payid text`. The last three are printed on invoices to satisfy the QLD rule of at least 2 payment methods.
- `users`: `id`, `email citext unique`, `name`, `password_hash text null`, `oidc_subject text unique null`, `created_at`. This needs `CREATE EXTENSION IF NOT EXISTS citext`.
- `memberships`: `user_id`, `org_id`, `role text check in ('owner','staff','bookkeeper','tenant','client')`, `contact_id bigint null`. `contact_id` is set for portal roles. Primary key `(user_id, org_id)`.
- `sessions`: `token_hash bytea pk` (SHA-256 of the cookie value), `user_id`, `expires_at`.
- `contacts`: `id`, `org_id`, `kind text check in ('tenant','client','supplier')`, `name`, `email`, `phone`, `company_name`, `abn`, `provider_customer_ref text`, plus `unique(org_id, provider_customer_ref)`.
- `categories`: `id`, `org_id`, `name`, `type text check in ('income','expense')`, `default_gst_code`, plus `unique(org_id, name)`.
- `invoices`:
  - Identity: `id`, `org_id`, `contact_id`, `number int null`, `source text check in ('rent','bond','timesheet','manual')`.
  - Status: `status text check in ('draft','open','paid','void','uncollectible') default 'draft'`.
  - Dates: `issue_date date null`, `due_date date null`.
  - Money: `currency`, `subtotal bigint`, `gst_total bigint`, `total bigint`.
  - Payment details: `payment_reference text`, `provider_ref text`, `provider_url text`.
  - Constraints: `unique(org_id, number)`, `unique(org_id, payment_reference)`.
- `invoice_counters`: `org_id pk`, `last_number int not null default 0`.
- `invoice_lines`: `id`, `invoice_id`, `org_id`, `description`, `quantity_milli bigint` (1.5 hours is stored as 1500), `unit_amount bigint`, `gst_code`, `gst_amount bigint`, `amount bigint` (exclusive of GST), `property_id bigint null`, `project_id bigint null`. There are no foreign keys on the property and project IDs yet; later plans add them.
- `payments`: `id`, `org_id`, `invoice_id`, `amount bigint check > 0`, `paid_on date`, `method text check in ('stripe','bank_transfer','payid','cash','other')`, `provider_ref text`, `bank_line_id bigint null`, plus `unique(org_id, provider_ref)`.
- `webhook_events`: `provider text`, `event_id text`, `org_id`, `received_at`. Primary key `(provider, event_id)`.
- `audit_log`: `id`, `org_id`, `actor_user_id null`, `entity text`, `entity_id bigint`, `action text`, `diff jsonb`, `at timestamptz default now()`.

- [ ] **Step 1: Write failing integration tests.** `TestMigrateIdempotent`: run `Migrate` twice, with no error, and expect `schema_migrations` to have 1 row. `TestCoreConstraints`: inserting a payment with `amount = 0` fails, and inserting a duplicate `(org_id, number)` invoice fails.
- [ ] **Step 2: Run `task test SUITE=integration` and confirm it FAILS. Expected: undefined `Migrate`.**
- [ ] **Step 3: Implement `db.Open`, `db.Migrate`, `db.TestPool`, and `0001_core.sql`.**
- [ ] **Step 4: Run `task test SUITE=integration`. Expected: PASS.**
- [ ] **Step 5: Commit:** `feat: add postgres pool, embedded migrations and core schema`

### Task 3: Money and GST maths

**Files:**
- Create: `internal/core/money.go`, `internal/core/money_test.go`

**Interfaces:**
- Produces:
  - `type Cents int64`.
  - `type GSTCode string`, with constants `GST`, `FRE`, `INP` and `NT = "N-T"`, and `func ParseGSTCode(s string) (GSTCode, error)`.
  - `func LineAmount(quantityMilli int64, unit Cents) Cents`. This is `quantityMilli × unit / 1000`, rounded half away from zero.
  - `func GSTOnExclusive(amount Cents, code GSTCode) Cents`. This is `amount / 10`, rounded half away from zero, when `code == GST`. Otherwise it is 0.
  - `func GSTInInclusive(total Cents, code GSTCode) Cents`. This is `total / 11`, rounded half away from zero, when `code == GST`. Otherwise it is 0. Plan 4 uses it for expenses.
  - `func Totals(lines []Line) (subtotal, gst, total Cents)`, with `type Line struct{ QuantityMilli int64; Unit Cents; Code GSTCode }`. GST is calculated **per line** and then summed. Do not apply GST to the summed subtotal.
- Rounding rule (record it in a doc comment and in the README): GST is calculated per invoice or expense line and rounded half away from zero to the cent.

- [ ] **Step 1: Write failing tests** (table-driven, with these exact cases):

```go
LineAmount(1500, 12000) == 18000   // 1.5h @ $120.00
LineAmount(333, 1)      == 0       // 0.333 → 0
LineAmount(500, 1)      == 1       // 0.5 → 1 (half away from zero)
GSTOnExclusive(18000, GST) == 1800
GSTOnExclusive(5, GST)     == 1    // 0.5 → 1
GSTOnExclusive(4, GST)     == 0
GSTOnExclusive(50000, INP) == 0
GSTInInclusive(1100, GST)  == 100
GSTInInclusive(1000, GST)  == 91   // 90.909 → 91
GSTInInclusive(1000, FRE)  == 0
Totals([{1000,5,GST},{1000,5,GST}]) == (10, 2, 12) // per-line rounding: 1+1, not round(1.0)
ParseGSTCode("GST-ish") returns error
```

- [ ] **Step 2: Run `go test ./internal/core/` and confirm it FAILS. Expected: undefined.**
- [ ] **Step 3: Implement the functions in `money.go`.** Use integer-only arithmetic, rounding half away from zero with `(2n + d) / (2d)`, mirrored for negative values.
- [ ] **Step 4: Run `go test ./internal/core/`. Expected: PASS.**
- [ ] **Step 5: Commit:** `feat: add cents type and per-line GST rounding`

### Task 4: Staff local auth, sessions and RBAC

**Files:**
- Create: `internal/auth/password.go`, `internal/auth/session.go`, `internal/auth/rbac.go`, `internal/auth/middleware.go`, `internal/auth/rbac_test.go`, `internal/auth/auth_integration_test.go` (tag `integration`), `internal/core/orgs.go`
- Modify: `cmd/shigoto/main.go`. Add `shigoto admin create-owner --org-slug S --org-name N --email E`. It reads the password from stdin, creates the org, the user (bcrypt cost 12) and an `owner` membership, and is idempotent on email plus slug.

**Interfaces:**
- Produces:
  - `type Role string`, with constants `Owner`, `Staff`, `Bookkeeper`, `Tenant` and `Client`.
  - `type Perm string`, with constants `PermOrgAdmin`, `PermContactsWrite`, `PermInvoicesWrite`, `PermInvoicesRead`, `PermPaymentsWrite`, `PermReportsRead` and `PermReconWrite`. Later plans append their own.
  - `func Can(r Role, p Perm) bool`, backed by a single map literal:
    - `Owner`: every permission.
    - `Staff`: everything except `PermOrgAdmin`.
    - `Bookkeeper`: `PermInvoicesRead`, `PermReportsRead`, `PermReconWrite` and `PermPaymentsWrite`.
    - `Tenant` and `Client`: none. Portal access is added in Plan 5.
  - `auth.Login(ctx, pool, email, password) (token string, err error)`. It creates a 32-byte random token and stores its SHA-256. Sessions last 30 days.
  - `auth.Logout(ctx, pool, token)`.
  - `type Principal struct{ UserID, OrgID int64; Role Role; ContactID *int64 }`, read with `auth.From(ctx) Principal`.
  - `func (a *Auth) RequireOrg(p Perm) func(http.Handler) http.Handler`. It resolves the cookie `shigoto_session` to a user, resolves `{org}` (the slug from `r.PathValue("org")`) to a membership, and checks `Can`.
    - No session: `401`.
    - Not a member of the org: `404`.
    - Member but missing the permission: `403`.
  - Routes, mounted in Task 5: `POST /api/v1/auth/login` with `{email,password}` sets an HttpOnly, Secure (when BaseURL is https), SameSite=Lax cookie. `POST /api/v1/auth/logout`.

- [ ] **Step 1: Write failing tests.**
  - `TestCanMatrix`: a table covering every role and permission in the lists above.
  - `TestLoginWrongPasswordGeneric` (integration): a wrong password and an unknown email return the same error.
  - `TestRequireOrgCrossOrg404` (integration): with orgs A and B and a user who is owner of A, a request to `/api/v1/orgs/B/...` returns `404`.
  - `TestRequireOrgBookkeeperCannotWriteContacts`: returns `403`.
  - `TestExpiredSession401`.
- [ ] **Step 2: Run `task test` and `task test SUITE=integration`, and confirm they FAIL.**
- [ ] **Step 3: Implement the password, session, RBAC and middleware code, plus `core.CreateOrg`, `core.OrgBySlug` and `admin create-owner`.**
- [ ] **Step 4: Run both suites. Expected: PASS.**
- [ ] **Step 5: Commit:** `feat: add local staff login, sessions and role permissions`

### Task 5: HTTP API skeleton and module registry

**Files:**
- Create: `internal/httpapi/server.go`, `internal/httpapi/json.go`, `internal/httpapi/server_test.go`
- Modify: `cmd/shigoto/main.go`. `serve` uses `httpapi.New`.

**Interfaces:**
- Consumes: `config.Config`, `*auth.Auth`, `*pgxpool.Pool`.
- Produces:
  - `type Module struct{ Name string; Routes func(mux *http.ServeMux, d Deps) }`.
  - `type Deps struct{ Pool *pgxpool.Pool; Auth *auth.Auth; Cfg config.Config; Payments func(orgSlug string) (payments.Provider, error) }`. `payments` comes from Task 7. Until then, use a nil func placeholder.
  - `var modules = []Module{}`. Plans 2, 3 and 4 each append one entry.
  - `func New(d Deps) http.Handler`. It mounts the core routes, mounts `Routes` for each module where `Cfg.Modules[m.Name]` is true, and serves `GET /api/v1/meta` returning `{"modules":["…enabled, sorted…"]}`.
  - `func writeJSON(w, status int, v any)` and `func readJSON(r, v any) error`. `readJSON` limits the body to 1 MiB and uses `DisallowUnknownFields`.
  - Errors use the shape `{"error":"<message>"}`.

- [ ] **Step 1: Write failing tests.** `TestMetaListsEnabledModules`: inject a fake module `property` while `Cfg.Modules = {"property":false}`. Its route returns `404`, and `/api/v1/meta` returns `{"modules":[]}`. With the module enabled, the route answers and meta lists it. `TestReadJSONRejectsUnknownField`.
- [ ] **Step 2: Run `go test ./internal/httpapi/` and confirm it FAILS.**
- [ ] **Step 3: Implement it.** Make `modules` package-level so a test can swap it out and restore it.
- [ ] **Step 4: Run it. Expected: PASS.**
- [ ] **Step 5: Commit:** `feat: add api router with module toggles and meta endpoint`

### Task 6: Contacts, invoices, numbering and manual payments

**Files:**
- Create: `internal/core/contacts.go`, `internal/core/invoices.go`, `internal/core/payments.go`, `internal/core/audit.go`, `internal/core/invoices_integration_test.go` (tag `integration`), `internal/httpapi/core_routes.go`

**Interfaces:**
- Consumes: `core.Cents`, `core.Totals`, `auth.RequireOrg`, `auth.From`.
- Produces (every function takes `ctx` and a `pgx.Tx` or the pool, and `orgID` comes first):
  - `CreateContact(ctx, q, orgID, Contact) (Contact, error)`, `GetContact(ctx, q, orgID, id)`, `ListContacts(ctx, q, orgID, kind string)`.
  - `CreateDraftInvoice(ctx, q, orgID, NewInvoice{ContactID int64; Source string; DueInDays int; Lines []NewLine}) (Invoice, error)`. It computes line `amount` and `gst_amount`, and the invoice subtotal, GST and total, with `Totals`. The payment reference is `"INV-" + base32(random 5 bytes)`, unique per org.
  - `IssueInvoice(ctx, tx, orgID, id int64, today time.Time) (Invoice, error)`:
    - Locks the counter with `SELECT ... FOR UPDATE` on `invoice_counters` (upserted), increments it, and sets `number`, `issue_date`, `due_date` and `status='open'`.
    - Only `draft` can be issued. Anything else returns `ErrInvalidState`.
    - Must run in the caller's transaction, so a rollback also rolls back the counter. That keeps numbering gap-free.
  - `VoidInvoice(ctx, tx, orgID, id)`: works on `open` only and keeps the number.
  - `RecordPayment(ctx, tx, orgID, NewPayment{InvoiceID int64; Amount Cents; PaidOn time.Time; Method string; ProviderRef string}) (Payment, created bool, err error)`:
    - Uses `ON CONFLICT (org_id, provider_ref) DO NOTHING` when `ProviderRef != ""`.
    - After inserting, if the sum of payments is at least `total`, sets `status='paid'`.
    - Rejects payments to `draft` and `void` invoices.
  - `Audit(ctx, q, orgID, actor *int64, entity string, id int64, action string, diff any)`. It is called for every write above.
- Routes. "Contacts write" means `PermContactsWrite`, and the other permissions follow the same pattern.
  - `POST /api/v1/orgs/{org}/contacts` and `GET /api/v1/orgs/{org}/contacts?kind=`, with `PermContactsWrite` and `PermInvoicesRead`.
  - `POST /api/v1/orgs/{org}/invoices` (creates a draft), `GET .../invoices/{id}`, `GET .../invoices?status=`.
  - `POST .../invoices/{id}/issue`. Task 7 adds sending it to the provider.
  - `POST .../invoices/{id}/void`.
  - `POST .../invoices/{id}/payments`: manual payment, method `bank_transfer` or `payid` or `cash`, with `PermPaymentsWrite`.
  - The invoice JSON includes a `pay_options` array built from the org's bank, PayID and provider URL. For rent and bond invoices, the API returns `422` on issue if fewer than 2 options are available (QLD rule).

- [ ] **Step 1: Write failing integration tests.**
  - `TestIssueNumbersSequentialConcurrent`: 10 goroutines each issue their own draft. Expect the numbers to be exactly `{1..10}`.
  - `TestIssueRollbackLeavesNoGap`: issue in a transaction and roll it back, then issue another. Expect number 1.
  - `TestPartialThenFullPayment`: total 10000. A payment of 4000 leaves it `open`. A further 6000 makes it `paid`. A further 500 is accepted, the status stays `paid`, and the sum is 10500.
  - `TestRecordPaymentIdempotentProviderRef`: the same `ProviderRef` twice gives 1 row and `created == false` the second time.
  - `TestCrossOrgInvoice404`: through the HTTP handler with a real session.
  - `TestRentInvoiceNeedsTwoPayOptions`: an org with no bank, no PayID and no provider returns `422` on issue. The same org with bank details plus a provider returns `200`.
  - `TestIssueNonDraftInvalid`.
- [ ] **Step 2: Run `task test SUITE=integration` and confirm it FAILS.**
- [ ] **Step 3: Implement the core functions and routes.** Each handler runs in its own transaction, which it commits or rolls back.
- [ ] **Step 4: Run it. Expected: PASS.**
- [ ] **Step 5: Commit:** `feat: add contacts, gap-free invoices and payment recording`

### Task 7: Payment provider interface, Stripe and webhooks

**Files:**
- Create: `internal/payments/provider.go`, `internal/payments/fake.go`, `internal/payments/stripe/stripe.go`, `internal/payments/stripe/stripe_test.go`, `internal/httpapi/webhooks.go`, `internal/httpapi/webhooks_integration_test.go` (tag `integration`), `internal/payments/stripe/testdata/*.json`
- Modify: `internal/httpapi/core_routes.go`. After issuing, the issue route calls the provider. `internal/httpapi/server.go` gains `Deps.Payments`.

**Interfaces:**
- Produces:

```go
type Provider interface {
	EnsureCustomer(ctx context.Context, c core.Contact) (ref string, err error)
	SendInvoice(ctx context.Context, inv core.Invoice, lines []core.InvoiceLine, customerRef string) (ref, hostedURL string, err error)
	VoidInvoice(ctx context.Context, ref string) error
	ParseWebhook(payload []byte, sigHeader string) (Event, error) // ErrBadSignature on mismatch
}
type Event struct {
	ID         string // provider event id
	Kind       string // "invoice.paid" | "ignored"
	InvoiceRef string
	PaymentRef string // charge / payment-intent id, used as payments.provider_ref
	Amount     core.Cents
	PaidOn     time.Time
}
```

- `payments.Fake` stores calls in memory. `ParseWebhook` accepts JSON signed with HMAC-SHA256 over `payload`, using a test secret and header `t=…,v1=…`, the same scheme as Stripe. That way the webhook tests cover the real verification path.
- `stripe.New(secretKey, webhookSecret string) payments.Provider`:
  - `EnsureCustomer` creates a customer with name and email, with `metadata.shigoto_contact_id`.
  - `SendInvoice`:
    - Creates a draft invoice: `collection_method=send_invoice`, `days_until_due` taken from the due date, `currency=aud`, `payment_settings.payment_method_types=[card, au_becs_debit]`, `metadata.shigoto_invoice_id`, and `number` left to Stripe. The shigoto number goes in `description` or `custom_fields` as `Invoice No. <n>` with reference `<payment_reference>`.
    - Adds one invoice item per line, with amount equal to `amount + gst_amount`, because we own the GST maths. GST appears as a line description suffix, and the Stripe Tax product is not used.
    - Finalizes, then sends.
  - `ParseWebhook` uses `webhook.ConstructEvent`. It maps `invoice.paid` to `Kind="invoice.paid"`, the payment intent ID to `PaymentRef`, `amount_paid` to `Amount`, and `status_transitions.paid_at` to `PaidOn` in `Australia/Brisbane`. Every other event type becomes `Kind="ignored"`.
- `httpapi.Deps.Payments(orgSlug)` builds `stripe.New` from `config.StripeFor`. It returns `ErrNoProvider` when the org has no keys. In that case issuing still works, there is no `provider_url`, and the invoice relies on the bank and PayID options.
- Route `POST /webhooks/stripe/{org}`, with no session:
  - Unknown org returns `404`.
  - A bad signature returns `400`.
  - Otherwise, in one transaction:
    1. `INSERT INTO webhook_events ... ON CONFLICT DO NOTHING`. If the event already exists, return `200` and do nothing more.
    2. For `invoice.paid`, find the invoice by `provider_ref` and call `RecordPayment` with method `stripe` and `ProviderRef=PaymentRef`.
    3. Commit and return `200`.
  - A matching invoice that cannot be found returns `200` and writes an audit entry with action `orphan_event`.
- The issue route:
  1. Issues the invoice inside the transaction and commits.
  2. Calls `EnsureCustomer`, storing the result in `provider_customer_ref`, then calls `SendInvoice`.
  3. Stores `provider_ref` and `provider_url`.
  - If sending fails, the invoice stays `open` with its number and no `provider_ref`. `POST .../invoices/{id}/send` retries it. This avoids burning numbers.

- [ ] **Step 1: Write failing tests.**
  - `stripe_test.go`: `TestParseWebhookInvoicePaid` uses a fixture JSON signed with `webhook.GenerateTestSignedPayload` and expects a correct `Event`. `TestParseWebhookBadSignature` expects `ErrBadSignature`.
  - Integration tests using `payments.Fake`:
    - `TestWebhookPaysInvoice`: the invoice becomes `paid` with 1 payment.
    - `TestWebhookDuplicateDelivery`: the same event is posted twice, sequentially and then concurrently. Expect 1 payment and 1 `webhook_events` row, and `200` both times.
    - `TestWebhookBadSignature400NoChange`.
    - `TestWebhookUnknownOrg404`.
    - `TestIssueSendFailureKeepsNumberAndRetries`: the fake returns an error once. Expect the invoice to be open with number 1 and no ref. `/send` succeeds and the ref is set.
- [ ] **Step 2: Run both suites and confirm they FAIL.**
- [ ] **Step 3: Implement the provider, the fake, Stripe, the webhook route and the issue/send flow.**
- [ ] **Step 4: Run both suites. Expected: PASS.**
- [ ] **Step 5: Manual check (optional, needs Stripe test keys).**
  1. Run `stripe listen --forward-to localhost:8080/webhooks/stripe/<slug>`.
  2. Issue an invoice and pay it on the hosted page with test card `4242…`.
  3. Expect `GET .../invoices/{id}` to show `paid`.
- [ ] **Step 6: Commit:** `feat: send invoices via stripe and record payments from verified webhooks`

### Task 8: Container image, CI and README

**Files:**
- Create: `Dockerfile`, `.dockerignore`, `.github/workflows/validate.yml`, `deploy/initdb/01-test.sql` (if it was not already created in Task 1)
- Modify: `README.md`. Cover quickstart, env vars, the rounding rule and `admin create-owner`.

**Interfaces:**
- Consumes: `cmd/shigoto`.
- Dockerfile: follow the pattern in `apps/monitoring/Dockerfile`:
  - Build stage `golang:1.27.<patch>-bookworm`, using `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`.
  - Collect licences.
  - Final stage `FROM scratch` with CA certs, `USER 65532:65532` and `ENTRYPOINT ["/shigoto"]`.
- CI: run `task lint`, `task test` and `task test SUITE=integration`, using a Postgres service container with the same pinned tag. Then run `docker build`.

- [ ] **Step 1: Run `docker compose up -d --build`, then `docker compose exec app /shigoto admin create-owner --org-slug me --org-name "Me" --email me@example.com` with the password on stdin.**
- [ ] **Step 2: Run `curl -c j -X POST localhost:8080/api/v1/auth/login -d '{"email":"me@example.com","password":"…"}'`, then `curl -b j localhost:8080/api/v1/orgs/me/contacts`. Expected: `200 []`.**
- [ ] **Step 3: Run `task lint && task test && task test SUITE=integration`. Expected: all PASS.**
- [ ] **Step 4: Commit:** `build: add container image, CI workflow and README`

---

## Done for Plan 1

- `task lint`, `task test` and `task test SUITE=integration` pass, with Go, Docker and go-task as the only requirements.
- `docker compose up` serves the API. An owner can log in, create a contact, draft an invoice, issue it, and see its pay options. The invoice can be paid by Stripe test-mode webhook or by a manual bank-transfer or PayID payment.
- Plans 2–5 are written once this lands, each against the interfaces above.
