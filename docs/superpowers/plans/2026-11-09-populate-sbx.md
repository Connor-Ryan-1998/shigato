# Populate Sandbox Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Populate and safely replace realistic local manual-testing data with `/shigoto admin populate-sbx`.

**Architecture:** Add an admin entry point backed by `internal/sandbox`, using existing domain functions and one database transaction. A small ownership registry distinguishes disposable sandbox entities from existing development data; explicit child-first deletion avoids broad cascading deletion.

**Tech Stack:** Go, pgx/Postgres, existing core/property/timesheets/reconciliation packages, Docker Compose, Go Task.

**Spec:** `docs/superpowers/specs/2026-11-09-populate-sbx-design.md`

**Status:** Completed on the existing branch, uncommitted. Full Go unit and integration tasks, lint, build and whitespace checks pass. Independent review found an external expense-line cascade hazard; its regression now passes after adding parent locks and external-child refusal. Reset tests also cover invitations, sessions, magic links, receipt references, payouts and unrelated counterparts. Browser E2E and population of the running development database were not performed.

## Global Constraints

- Existing development database; two sandbox organisations; repeated invocation replaces only explicitly owned data.
- No authentication bypass, external provider calls, generated-secret files, database truncation or global sequence resets.
- Tenant/client authentication remains passwordless through normal Mailpit-captured magic links.
- At least 60 invoices in the primary organisation; dates use the configured timezone.
- Preserve existing uncommitted work. No new screens or infrastructure changes.
- Run Taskfile validation sequentially through `uv run --locked`.

## Review Focus

- Existing organisation slug or case-insensitive account email: refuse without modifying any existing rows.
- Sandbox user reused by another organisation: refuse reset rather than deleting shared identity or changing its credentials.
- Failure after old records are deleted: transaction rollback restores the previous complete sandbox.
- Concurrent population: transaction advisory lock serialises runs; no duplicate data or partial account output.
- Tester-created child records, sessions, invitations and file references: reset handles owned dependencies, preserves unrelated records, and never removes unrelated filesystem data.

---

### Task 1: Transactional ownership and replacement

**Files:**
- Create: `internal/db/migrations/0016_sandbox.sql` (confirm next migration number before implementation).
- Create: `internal/sandbox/populate.go`.
- Create: `internal/sandbox/reset.go`.
- Create: `internal/sandbox/populate_integration_test.go`.

**Interfaces:**
- Consumes: `core.CreateOrg`, `core.Querier`, and schema-isolated `db.TestPool(t *testing.T) *pgxpool.Pool` from `internal/db/testdb.go`.
- Produces: `sandbox.Populate(ctx context.Context, pool *pgxpool.Pool, today time.Time) (sandbox.Result, error)`; `Result` contains the committed organisation slugs and staff credentials for CLI output.
- Internal orchestration owns begin/commit/rollback and calls `populate(ctx context.Context, tx pgx.Tx, today time.Time) (Result, error)`.

- [ ] Read all migrations and enumerate FK dependencies, including tables without `org_id`, before defining reset order.
- [ ] Write `TestPopulateCollision`, `TestPopulatePreservesUnrelatedData`, `TestPopulateRefusesSharedUser`, `TestPopulateRollback`, and `TestPopulateConcurrent`. Assert case-insensitive email collision refusal, unchanged unrelated rows, restored old data after a transactional failure, and exactly one final dataset after concurrent runs.
- [ ] Run `uv run --locked task test SUITE=integration` from the application directory; confirm new tests fail because implementation is missing.
- [ ] Add a fixed-purpose ownership registry for organisation/user IDs. Use fixed slugs `sbx-property` and `sbx-consulting` and emails under `example.test`; never infer ownership from a slug or email alone.
- [ ] Implement transaction-scoped advisory locking, collision/external-reference checks and explicit child-first cleanup. Follow the existing FK schema; do not add cascading deletion to business tables. Remove sessions/magic links/invites for owned identities only after verifying no unrelated dependencies.
- [ ] Retain ownership during regeneration and record every newly owned entity atomically. Generate fresh staff passwords with `crypto/rand`, hash with `auth.HashPassword`, and never return successful credentials before commit.
- [ ] Run the integration task and confirm ownership/reset tests pass before proceeding.

### Task 2: Curated linked scenarios and portal proof

**Files:**
- Create: `internal/sandbox/scenarios.go`.
- Create: `internal/sandbox/scenarios_integration_test.go`.
- Modify: `internal/sandbox/populate.go`.

**Interfaces:**
- Consumes: Task 1 transaction and result types; `core.CreateContact`, `core.CreateDraftInvoice`, `core.IssueInvoice`, `core.RecordPayment`, `core.VoidInvoice`; property/timesheets/reconciliation domain functions.
- Produces: `seedScenarios(ctx context.Context, tx pgx.Tx, today time.Time, orgs []core.Org, users map[string]int64) error`.

- [ ] Write `TestPopulateScenarios`: assert two organisations, all five roles, linked active/ended leases, rent changes and charges, billed/unbilled time, matched/unmatched bank lines, expenses, and at least 60 main-org invoices. Assert draft/open/overdue/partial/paid/void states and payment balances using stored totals rather than display labels.
- [ ] Write `TestPopulatePortalVisibility` using the real HTTP handler and normal auth/magic-link functions. Assert populated and empty tenant/client accounts, exclusion of another tenant's same-org invoices and leases, cross-org denial, and denial of staff endpoints.
- [ ] Write `TestPopulateReplacesTesterEdits` including a newly created sandbox child record and session. Assert reset removes owned edits without changing another organisation's records or identities. Include an externally linked record that must cause safe refusal.
- [ ] Run the integration task and confirm these scenario assertions fail before adding scenarios.
- [ ] Seed owner/staff/bookkeeper accounts with generated passwords; seed tenant/client memberships linked to contacts without passwords. Shared owner belongs to both organisations; portal users exercise same-org and cross-org isolation.
- [ ] Build realistic linked records with existing domain operations. Supply historical issue dates to `core.IssueInvoice` rather than rewriting calculated financial states. Anchor dates to local midnight. Leave auto-send disabled and never construct a real provider.
- [ ] Where an existing helper owns its own transaction, use its lower-level domain operation or narrowly scoped fixture SQL inside the outer transaction. Do not refactor production transaction ownership solely for seeding. Keep financial calculations in existing domain helpers.
- [ ] Run integration task; confirm scenario, isolation, reset and financial assertions pass.

### Task 3: Admin command, instructions and verification

**Files:**
- Modify: `cmd/shigoto/main.go`.
- Create: `cmd/shigoto/sandbox.go`.
- Create: `cmd/shigoto/sandbox_test.go`.
- Modify: `README.md` (preserve current uncommitted changes).

**Interfaces:**
- Consumes: `sandbox.Populate`, existing `config.Load`, `db.Open`, and `db.Migrate` patterns.
- Produces: `adminPopulateSandbox(args []string) error`, dispatched from `admin` as `populate-sbx`.

- [ ] Write argument/output tests: reject unsupported flags and positional arguments before DB access; successful output names both organisations, staff credentials, portal emails, Mailpit instructions and destructive rerun warning. Never print a database URL, existing secret, or portal password.
- [ ] Run `uv run --locked task test`; confirm command tests fail before implementation.
- [ ] Add command dispatch and implementation with no required arguments. Load existing config, migrate, derive population day using configured timezone, populate transactionally, then print committed access details and Mailpit instructions.
- [ ] Update README command/usage sections with the exact Docker command, build prerequisite, record summary, password/magic-link workflow, and warning that reruns discard owned sandbox edits. Explain that it must only target development databases.
- [ ] Run `uv run --locked task test`, `uv run --locked task test SUITE=integration`, and `uv run --locked task lint` sequentially. Run `uv run --locked task build` to prove binary integration. Do not run population against the operator's development database.
- [ ] Review only this feature's diff for credentials, reset scope, missing dependencies, accidental UI changes and rollback safety. Report executed checks and any blocked checks accurately; leave changes uncommitted unless asked.
