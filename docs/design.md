# Design: shigoto — small-business app (property + timesheets + invoicing + recon)

> Name chosen by the user: shigoto (仕事, "work").

## Context

The user owns rental properties and does consulting work. Until now a property manager handled the properties through Ailo, and the user only saw owner statements. The user now wants to **self-manage**, replacing the property manager, and run the consulting business from the same place.

At tax time they need one place that shows all money coming in and going out, reconciled and split by property and by project, ready for an Australian BAS.

This document is the brainstormed design. After approval it is copied to `apps/shigoto/docs/design.md` in the new repo, and `superpowers:writing-plans` produces the implementation plan.

## Intent

One small self-hosted app, shipped as one Go binary plus Postgres, that runs a micro business end to end:

1. Leases and tenants generate rent invoices.
2. Logged hours generate client invoices.
3. Stripe sends the invoices and collects payment.
4. Bank CSV imports are matched against invoices and expenses.
5. The app produces a P&L and a BAS per property and per project.

## Goals (v1: all three flows, thin, end-to-end)

- **Rent cycle:**
  - Property, tenant and lease records.
  - Recurring rent periods.
  - A Stripe invoice for each period. Tenants can pay by card or AU BECS direct debit.
  - Payments recorded from the Stripe webhook.
  - Arrears visible.
- **Hours to invoice:**
  - Client, project and time-entry records, with an hourly rate per project.
  - One invoice per client per period from unbilled entries. Billed entries are locked.
  - A Stripe invoice and paid status.
- **Tax-time reconciliation:**
  - Bank CSV import with a column mapping for each bank account.
  - Imported lines are deduplicated.
  - Each line is matched to a payment or invoice, or categorised as an expense (category, GST code, property or project).
  - Receipts can be attached to expenses.
  - Reports:
    - P&L per property, per project and per organisation.
    - BAS summary for each quarter or month, on a cash basis, covering G1, G2/G3 where relevant, G10/G11, 1A and 1B.
    - CSV export.
- **Portal:**
  - Tenants see their own leases, invoices and payment history.
  - Clients see their own invoices.
  - Both pay through Stripe-hosted invoice pages.
- **RBAC from day one**, even though the user is the only staff member today.
- **Modules** that can be switched on or off in config: `property`, `timesheets`, `reconciliation`. Core is always on: orgs, users and auth, contacts, invoices, ledger, reports.

## Non-goals (v1)

- Double-entry accounting, a chart of accounts or a balance sheet. The ledger is categorised only. The accountant does the rest.
- BAS lodgement with the ATO, and payroll.
- Native mobile apps. The API is designed so one can be added later.
- Live bank feeds such as Basiq. The import interface leaves room for them.
- Payment providers other than Stripe. The interface leaves room for them.
- Wiring into Ansible or Traefik. That is a separate follow-up in the homelab repo, done once the image exists.
- Any code or schema files copied from MicroRealEstate (see Constraints).

## Constraints

- **Independent of the homelab repo.** The app lives at `apps/shigoto/` as its **own git repo**. It starts as a local repo and becomes a submodule later, the same way `apps/monitoring` points to `keiryo.git`. It has its own `go.mod`, `Taskfile.yml`, `Dockerfile`, `docker-compose.yml` and `.github/workflows/`. It needs nothing from `ansible/`, so all tests run locally.
- **Runtime:** one Go binary serves `/api/v1` (JSON) and the embedded SPA. Postgres is the only other runtime dependency. There is no Redis, queue or worker process.
- **Money:** integer minor units (cents) plus a currency code. Never floats. GST is computed per line, with the rounding rule documented.
- **AU tax rules:**
  - Financial year runs 1 July to 30 June.
  - Every income or expense line carries a GST code: `GST` (taxable 10%), `FRE` (GST-free), `INP` (input-taxed) or `N-T` (not reportable).
  - **Residential rent is input-taxed.** Rent invoices carry no GST, and expenses tagged to a residential property claim no GST credits.
  - Consulting invoices are `GST` only when the org is GST-registered.
  - Each org records its ABN, GST registration, and BAS frequency (quarterly or monthly).
- **QLD tenancy rules** (all properties are in QLD; rules are kept per property so other states can be added later):
  - **Payment methods:** each rent and bond invoice offers at least 2 ways to pay, one of them free of fees. The ways are Stripe (card or BECS direct debit), bank transfer, and PayID. Bank transfer and PayID details, plus a payment reference, are printed on the invoice. Those payments arrive through bank CSV recon, not through Stripe.
  - **Rent in advance:** invoice no further ahead than allowed, which is 2 weeks for weekly or fortnightly rent and 1 month for monthly rent.
  - **Proration:** partial periods are prorated daily, using weekly rent ÷ 7, or monthly rent × 12 ÷ 365.
  - **Rent increases:** effective from a date and stored as `lease_rent_changes`. The QLD limit of one increase per 12 months is a warning, not a block.
  - **Bond:** collected through the app using any of the same payment methods. The invoice source is `bond`, the GST code is `N-T`, and the bond is **excluded from P&L**. It is tracked as held until recon matches the transfer to the QLD RTA and the lodgement reference is recorded. The app shows a warning if the bond is not lodged within 10 days.
- **No Ailo history.** The app starts clean. Existing leases are entered with a paid-to date and opening arrears, so rent periods start part-way through a lease. Reports for the first financial year are partial by design, and the accountant combines them with the Ailo statements.
- **Stripe, one account per org:** each org has its own API key and webhook secret in config. Webhooks are routed by `/webhooks/stripe/{org}`. `stripe_customer_id` is unique per org.
- **Stripe fees and payouts:** on a cash basis, income counts on the payment date. When a bank line matches a Stripe payout, the app pulls that payout's balance transactions. It links each payment to the payout, and records Stripe fees as expense lines with GST code `GST`, so the GST credit flows into 1B.
- **Multi-org data model, single org at start.** The user hasn't decided whether property and consulting are separate legal entities. Every business row carries `org_id`, and reports are per org.
- **MicroRealEstate (MRE) licence:** since Feb 2026 MRE uses the Sustainable Use License. Use it as a *conceptual* reference only. Write our own relational schema and code.
- **Stripe is not the source of truth.** Invoice numbers, amounts and status live in Postgres. Stripe IDs are foreign references. Webhooks are verified and idempotent, keyed on event ID.

## Decisions made with the user

| Topic | Decision |
|---|---|
| Role | Replaces the property manager. The user self-manages. |
| Users | The user now, a few clients. RBAC open for adding staff later. |
| Bank data | CSV import first, behind a pluggable import-source interface. |
| Login | Staff use generic OIDC (Authentik in prod) **plus** local login. Tenants and clients use an email magic link or password against the app, with no Authentik. |
| Portal | Yes, for tenants and clients. |
| Mobile | Responsive web now. Native later, so the API comes first. |
| DB | Postgres. |
| UI | JSON API plus a JS SPA, built and embedded in the binary. |
| Tax | Australia, BAS-ready reports. |
| Entities | Not sure yet, so the data model supports multiple orgs. |
| Ledger | Categorised, not double-entry. |
| V1 scope | All three flows, thin. |
| Stack | Go. Matches `apps/monitoring`. |
| History | Start clean. No Ailo import. |
| State | QLD. |
| Payment methods | At least 2 per invoice: Stripe, bank transfer, PayID. Applies to rent and bond. |

**Calls made by Claude (check these):**
- One Stripe account per org.
- Fees are recorded from payout balance transactions.
- Cash basis uses the payment date.
- Co-tenants are supported through a join table.
- Expenses can be split into lines.
- Tooling is Go, Docker and go-task.
- The bond-not-lodged warning fires at 10 days.

## Architecture

```
apps/shigoto/                      (own local repo; submodule later)
  cmd/shigoto/main.go              flags/env config, migrations, serve
  internal/
    config/                      env + file config, module toggles
    db/                          pgx pool, embedded SQL migrations
    auth/                        OIDC (staff), local password, magic link, sessions, RBAC
    core/                        orgs, users, contacts, invoices, ledger, reports
    property/                    module: properties, tenants, leases, rent periods
    timesheets/                  module: clients/projects, time entries, billing runs
    recon/                       module: bank accounts, imports, matching, expenses, receipts
    payments/                    Provider interface + stripe/ implementation, webhooks
    importers/                   Source interface + csv/ implementation
    scheduler/                   in-process ticker: generate due rent periods/invoices
    httpapi/                     router, middleware, module route registration
  web/                           SPA source; build output embedded via go:embed
  migrations/                    *.sql
  tests/integration/             against real Postgres (compose / testcontainers)
  Dockerfile, docker-compose.yml, Taskfile.yml, .github/workflows/validate.yml
```

- **Modules:** each module registers its routes, migrations and permissions at startup. A disabled module registers nothing and the SPA hides it, because `/api/v1/meta` lists the enabled modules. Migrations for every module always run, so turning a module back on never leaves the schema half-migrated.
- **The only two interfaces:** `payments.Provider` (create customer, create/send/void invoice, verify and parse webhook) and `importers.Source` (yield normalised bank lines). Per the user's ask, everything else is concrete. The one accepted third is `core.Querier`, the small pool-or-tx interface that lets core functions run on a `pgxpool.Pool` or inside a caller's `pgx.Tx`.
- **Scheduler:** a ticker inside the process runs daily. It creates due rent periods, then drafts their invoices and sends them automatically if the org allows it. Each step is idempotent, so a missed day catches up on the next run.
- **Files** (receipts, lease PDFs) are stored on a local volume path set in config. Postgres keeps only the metadata.
- **Email:** SMTP config is used for magic links and notices. Stripe emails the invoices itself.

## Data model (relational, MRE-inspired)

- **Core:**
  - `orgs`: name, ABN, gst_registered, bas_frequency, fy_start, currency.
  - `users`: email, name, password_hash nullable, oidc_subject nullable.
  - `memberships`: user, org, role.
  - `contacts`: org, kind = tenant | client | supplier, name, email, phone, company fields, `stripe_customer_id`.
  - `invoices`:
    - Identity: org, contact, number (sequential per org), source = rent | bond | timesheet | manual, payment_reference (used to match bank transfers and PayID payments).
    - Dates: issue_date, due_date.
    - Status: draft | open | paid | void | uncollectible.
    - `provider_ref`.
  - `invoice_lines`: description, qty, unit_amount, gst_code, gst_amount, plus optional property_id or project_id.
  - `payments`: invoice, amount, date, method, provider_ref.
  - `categories`: org, name, type income | expense, default gst_code.
  - `audit_log`: actor, org, entity, action, diff, time.
- **Property** (mirrors MRE's Realm → Property/Lease/Tenant, but normalised):
  - `properties`: org, name, address, type, residential bool.
  - `leases`:
    - Where: org, property. Tenants are linked through `lease_tenants` (lease, contact, primary bool), so co-tenants each get portal access. The primary tenant receives the invoices.
    - Term: start, end, termination_date.
    - Rent: frequency (weekly | fortnightly | monthly), due_day, paid_to_date, opening_arrears. The amount is held in `lease_rent_changes` (effective_date, amount).
    - Bond: bond_amount, bond_status (due | held | lodged | refunded), rta_lodgement_ref.
  - `lease_charges`: recurring extra charges with their own dates.
  - `rent_periods`: lease, period_start, period_end, amount_due, invoice_id. There is one row per period, which replaces MRE's embedded `rents` blob. Arrears are amount_due minus the payments linked through the invoice.
- **Timesheets:**
  - `projects`: org, client contact, name, hourly_rate, gst_code, active.
  - `time_entries`: user, project, date, minutes, note, invoice_line_id nullable. A non-null value means billed and locked.
- **Reconciliation:**
  - `bank_accounts`: org, name, csv_mapping JSON.
  - `bank_imports`: file hash, row count.
  - `bank_lines`: account, date, amount, description, `dedupe_key` UNIQUE, status unmatched | matched | ignored.
  - `matches`: bank_line linked to a payment or an expense.
  - `expenses`: org, date, supplier contact, receipt file, plus `expense_lines`. Each line has a category, amount, gst_code, gst_amount, and property_id | project_id | none (none means org overhead). Splitting one cost across properties, or a mixed-GST receipt, becomes several lines.
- **Roles (v1):**
  - `owner`: everything.
  - `staff`: operational CRUD and own time entries, but not org settings or role changes.
  - `bookkeeper`: read all, reconcile, reports.
  - `tenant` and `client`: portal only, and only rows linked to their own contact. This is checked in the query layer, not just the UI.

## Key flows

1. **Rent:**
   1. The scheduler creates a `rent_period` and a draft invoice (one rent line, plus any charges, GST code `INP` for a residential property).
   2. The app calls `Provider.CreateInvoice`, and Stripe sends it.
   3. The `invoice.paid` webhook creates a `payment`.
   4. The portal shows the invoice as paid.
2. **Hours:**
   1. Staff log time entries.
   2. A billing run for each client and date range groups unbilled entries into lines and creates the invoice.
   3. The entries are locked.
   4. The invoice goes to Stripe, as in the rent flow.
3. **Recon:**
   1. Upload a CSV and select the bank account. The app parses it with that account's mapping and skips lines it has already imported.
   2. The app suggests auto-matches: Stripe payouts and payments by amount and date, then exact amounts against open invoices.
   3. The user categorises the rest as expenses, or ignores them (for example transfers).
   4. Reports read only from invoices, payments and expenses, so every BAS number traces back to rows.

## Testing

- `task test`: Go unit tests (GST maths, rent period generation, CSV parsing and dedupe, RBAC scoping) plus SPA unit tests.
- `task test SUITE=integration`: Postgres in docker compose. Covers migrations, the full flows, and Stripe through a fake `Provider` plus signed webhook fixtures.
- Optional manual check: `stripe listen` against Stripe test mode.
- `task dev`: compose up Postgres and run the binary with hot SPA rebuild.

## Left to the builder

- Go router and libraries (stdlib `net/http` is preferred), SQL access (`pgx` plus hand-written SQL or `sqlc`), the migration tool, the SPA framework (React or Svelte) and component kit.
- The magic link and password policy details, and how sessions are stored (a Postgres table is preferred).
- The exact auto-match heuristics and CSV mapping UI.
- The invoice number format, provided it is sequential per org with no gaps.
- How the BAS report is laid out.

## Rollout steps after approval

1. Run `git init` in `apps/shigoto` to make it a **local repo only**, with nothing pushed. Add `/apps/shigoto/` to the homelab `.gitignore` so the parent repo never picks it up as an embedded repo by accident. Later, once the user asks, create the GitHub repo, push, and turn it into a submodule like `keiryo`, removing the ignore line.
2. Commit this design as `apps/shigoto/docs/design.md`, in the shigoto repo.
3. Run `superpowers:writing-plans` to produce the implementation plan, then implement.

## Verification (definition of done for v1)

- `task test` and `task test SUITE=integration` pass locally in `apps/shigoto`. Requirements are the same as `apps/monitoring`: Docker plus go-task, with Go and Node on the host. Postgres runs in Docker.
- A rent invoice shows Stripe, bank transfer and PayID options. A bank-transfer payment imported through CSV marks the invoice paid.
- A bond paid in the app is held, then marked lodged when the RTA transfer is matched, and never appears in the P&L.
- `docker compose up` gives a working app at `localhost`, where:
  - Creating a lease produces a rent invoice in Stripe test mode, and paying it marks it paid through the webhook.
  - Logging hours, then a billing run, produces a client invoice.
  - Importing a sample CSV matches the payout, and categorising the rest produces a BAS summary whose 1A and 1B figures tie back to the listed rows.
  - A tenant login sees only its own lease and invoices.
