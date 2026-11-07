# Pre-deployment Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix confirmed security and financial defects, exercise critical workflows, and provide a reproducible pre-deployment test gate.

**Architecture:** Keep existing handlers, services and test helpers. Fix validation/state checks at their shared source, add real-Postgres regressions for transactions and concurrency, and use the existing embedded-app Playwright harness for browser flows.

**Tech Stack:** Go, pgx/Postgres, Svelte, Vitest, Playwright, go-task.

**Spec:** `docs/superpowers/specs/2026-11-07-predeploy-hardening-design.md` (approved scope captured from conversation; builder decisions called out separately).

## Execution result (2026-11-07)

Tasks 1–6 implemented. Independent whole-change review found two Important gaps:
partial payments still allowed a full-amount provider send, and the access matrix
lacked real foreign resource IDs under an authorized org. Both were addressed:
the partial-payment regression failed before its fix and passed afterwards;
foreign-contact/invoice write tests pass against the existing scoping safeguards.
The final `COMPOSE_PROJECT_NAME=shigoto go-task verify` passed after that fix,
including build/lint, Go race/integration suites, 124 frontend tests and 7 Chromium
workflows. Review fixes were verified by tests, not a second review pass.

`go-task audit` remains intentionally failing on GHSA-gcqx-w48m-w2h4; Go reports
no reachable advisory. No dependency downgrade, vulnerability exception, merge,
push or deployment was performed. This is a completed hardening pass, **not** a
release-readiness/security certification. Current risks and remaining acceptance
work are recorded in README and the follow-up ledger.

Decisions retained: reuse the local Compose test stack to avoid port conflict
(containers can be recreated); leave the advisory gate blocking without approval;
keep aggregate/extreme-input validation separate (bad extreme totals may remain);
preserve rounding/remainder policy (cent distribution unchanged); defer provider
idempotency/commit recovery (external/local balances may disagree); leave historical
audit records untouched (old bank details can remain); and do not infer live
OIDC/Stripe, abuse/load, fault-injection or image-scan acceptance from isolated tests.

## Global Constraints

- Deployment is explicitly excluded.
- Every defect fix needs a regression test observed failing first.
- No new runtime dependencies or broad refactors.
- No live Stripe, SMTP or identity-provider credentials; use isolated test data.
- Preserve existing permissions, contact isolation, session rotation, token consumption, provider idempotency, and financial rounding contracts.
- Use `go-task` on this host; `task` is the equivalent binary on CI.
- Create an isolated worktree before implementation: current checkout is clean `main`.
- Do not rewrite historical audit records, push, merge or deploy.

## Baseline and evidence (2026-11-07)

- `go-task test`: PASS, Go race detector enabled.
- `go-task web:test`: PASS, 28 files / 124 tests; type checker clean.
- `go-task lint`: PASS.
- `go-task test SUITE=integration`: PASS on full background rerun; slowest
  package was `internal/httpapi` at 215.993 seconds. Initial foreground run hit
  the tool's 120-second cutoff, not a failing assertion.
- `go-task web:e2e`: PASS on sequential rerun, all three specs in 9.7 seconds,
  including frontend and Go build. Initial attempt lacked Chromium; installed it.
  A concurrent rerun had an invite setup timeout; the sequential run did not
  reproduce it. Root cause of that timeout is not established; keep the final gate
  sequential and do not classify it as an invite application defect.
- `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`: no reachable
  vulnerabilities; one module-level advisory not called by application code.
- Full `npm audit --json`: four low-severity package entries sharing one
  `deepmerge` prototype-poisoning advisory, GHSA-gcqx-w48m-w2h4, in build tooling.
  `--omit=dev` reports zero, but is not sufficient for an embedded frontend whose
  build dependencies are all marked development dependencies. Do not use the
  suggested `npm audit fix --force` downgrades.
- A Go overlay regression reproduces report overflow: two lines with exclusive
  amount 3,000,000,000 cents and GST 300,000,000 cents, paid in full at
  6,600,000,000 cents, allocate inclusive amounts 505,038,777 and 6,094,961,223
  instead of 3,300,000,000 each. Scratch test:
  `/tmp/opencode/shigoto-audit-allocate_test.go`.
- A real-Postgres overlay regression interleaves a committed `Void` after `Send`
  reads the invoice but before it locks it. `Send` returns success and stores
  `in_fake_2` on a void invoice. Scratch test:
  `/tmp/opencode/shigoto-audit-send_test.go`.
- Source confirms settings PUT passes the complete bank BSB, account and PayID
  to `core.Audit` (`internal/httpapi/settings_routes.go:86`). Login short-circuits
  its IP counter when email is blocked (`internal/auth/middleware.go:119-125`).
- Stale backlog item: frontend `safeNext` already rejects backslashes and control
  characters. Do not reimplement it.

## Initial risk-ranked audit

**What this repo does:** One Go binary and Postgres manage property, timesheets,
invoices, reconciliation and Australian tax reports, with staff and contact-scoped
portal access. Assumed load is one instance and a small team, not a public SaaS;
concurrent requests and hostile input remain in scope.

### Must fix

1. **A void invoice can become payable externally** (`internal/billing/billing.go:253-285`).
   **What this is:** Sending an issued invoice creates its provider-side payment link.
   **Problem:** A committed void between initial read and row lock is ignored;
   the overlay test produces a void invoice with a fresh provider reference.
   **Fix:** Recheck current open status under the existing row lock before any
   provider side effect. **If skipped:** A tenant/client can be charged for a
   locally cancelled invoice. Also test payment racing send, not just void.

2. **Valid large payments corrupt report allocations** (`internal/reports/allocate.go:52-58`).
   **What this is:** Payments are divided among invoice lines for P&L and BAS.
   **Problem:** Intermediate multiplication and subsequent rounding overflow int64;
   the full-payment reproduction above allocates materially wrong amounts.
   **Fix:** Use exact standard-library wide arithmetic for multiplication and
   rounding, without changing distribution policy. **If skipped:** Reports can
   silently misclassify income and GST for accepted financial inputs.

### Should fix

3. **Payment details are duplicated into audit history** (`internal/httpapi/settings_routes.go:86`).
   **What this is:** Each settings update stores an audit diff.
   **Problem:** The diff includes raw BSB, account and PayID, retaining extra copies
   even after settings change. No separate audit endpoint exposure was established.
   **Fix:** Redact future audit copies, not actual payment instructions; leave
   historical cleanup for an explicitly approved operation. **If skipped:** Audit
   exports/backups unnecessarily retain sensitive payment details.

4. **Email-blocked attempts bypass IP accounting** (`internal/auth/middleware.go:119-125`).
   **What this is:** Login is limited by both email and client IP.
   **Problem:** Returning on the email limit means those requests never consume the
   IP allowance. This does not by itself bypass the email's credential-check cap.
   **Fix:** Evaluate both counters before returning 429. **If skipped:** IP-level
   abuse accounting is incomplete and behavior diverges from the documented policy.

5. **Real-browser tests omit core financial writes** (`web/e2e/`).
   **What this is:** Three specs cover login/org switching, a staff invite, and a
   tenant's magic-link invoice view.
   **Problem:** No existing browser scenario creates/issues an invoice, bills time,
   creates a lease or reconciles a payment. API/unit tests do not exercise the
   complete browser-to-database flow. **Fix:** Add focused persisted-state workflow
   checks using the existing harness. **If skipped:** Broken financial form wiring
   can ship while all current browser checks pass.

**Verdict:** Fix the provider-send race and financial overflow before treating the
application as a deployment candidate; then close the security/test gaps.

**Not checked:** This initial pass read auth/session/OIDC/invite/magic flows,
HTTP wiring, settings, billing/provider calls, payments/webhooks, portal queries,
allocation/CSV reporting, receipt storage/download, representative property/time
math, and build/CI/test harnesses. It is not a full line-by-line review of every
property, reconciliation or frontend component. Live identity-provider behavior,
live Stripe, sustained load, file/DB failure injection and container-image scanning
were not exercised. Existing tests passing is not a security certification.

## Review Focus

1. A void or payment committed between a send's initial read and row lock must
   prevent any provider invoice creation (Task 4).
2. Large valid invoice/payment values must retain exact inclusive, GST and
   exclusive allocations without integer wraparound (Task 3).
3. Missing permission or foreign organisation/contact IDs must neither reveal
   financial data nor perform writes (Task 5).
4. Expired sessions during a financial form must not discard staff input or
   expose staff UI to portal users (Task 5).
5. Redacting audit data must not redact the actual payment instructions returned
   by authorized settings and portal endpoints (Task 1).

---

### Task 1: Stop copying payment credentials into new audit records

**Files:** `internal/httpapi/settings_routes.go`, `internal/httpapi/settings_integration_test.go`.

**Interfaces:** Keep `putSettings(w http.ResponseWriter, r *http.Request, d Deps)` and API shapes unchanged. Audit a copy with `bank_bsb`, `bank_account`, and `payid` replaced by a fixed redaction marker, including clears; never mutate the request used by `UpdateSettings`.

- [ ] Add `TestSettingsAuditRedactsPaymentDetails`: PUT valid full settings with BSB `062-000`, account `12345678`, PayID `audit@example.com`; query the committed `audit_log.diff`; assert none of those values are present and non-sensitive fields remain. GET settings must still return original payment instructions. Repeat with empty payment fields; stored settings must clear, audit must remain redacted.
- [ ] Run `go-task test SUITE=integration`. Expected: new audit test FAILS because raw payment values are stored.
- [ ] Redact the copy passed to `core.Audit`, retaining transaction rollback on audit failure. No data migration.
- [ ] Rerun integration and unit suites. Expected: PASS, including existing portal payment-instruction tests.
- [ ] Commit only Task 1 files: `fix: redact payment details in settings audit records`.

### Task 2: Count blocked login attempts against both rate-limit keys

**Files:** `internal/auth/middleware.go`, `internal/auth/auth_integration_test.go`.

**Interfaces:** Keep login limits at 5 per email / 20 per IP / 15 minutes, with the existing 429 and `Retry-After` contract. Both limit checks must run before credential work, even when either fails. Release successful reservations as before; do not reset earlier failures.

- [ ] Add `TestLoginEmailBlockedAttemptsCountAgainstIP`: saturate an email key, send 20 requests from one different IP against that blocked email, then try a fresh email from that IP. Expect 429 without credential verification. Use the existing fake clock and limiter constants; no sleeps.
- [ ] Run integration suite. Expected: new test FAILS because the fresh email reaches credential verification and returns 401.
- [ ] Evaluate both limit checks before deciding whether to reject; when both fail, use the larger retry interval so advertised retries satisfy both restrictions.
- [ ] Run unit/integration suites. Expected: PASS, including case-insensitive email, IPv6, trusted-proxy and successful-login accounting tests.
- [ ] Commit: `fix: count email-blocked login attempts against ip limits`.

### Task 3: Remove intermediate overflow from report allocation

**Files:** `internal/reports/allocate.go`, `internal/reports/allocate_test.go`, `internal/reports/reports_integration_test.go`.

**Interfaces:** Keep `Allocate(payment core.Cents, lines []AllocLine) []Share` unchanged. Preserve per-line half-away-from-zero rounding and final-line remainder policy. Use Go standard-library wide arithmetic for multiply/divide; do not add a dependency or change unrelated `core.DivRound` callers.

- [ ] Promote the scratch allocation regression into `TestAllocateLargePayment`; assert each full-payment share is exactly `Incl=3300000000`, `GST=300000000`, `Ex=3000000000` for the two-line fixture. Add a single GST line with `Amount=100000000000`, `GST=10000000000`, full payment `110000000000`, expecting that exact GST share. Add two GST-free lines of `2000000000` cents and payment `3000000000`, expecting `1500000000` per line: its product fits int64 but `DivRound`'s doubling does not.
- [ ] Run `go-task test`. Expected: new arithmetic cases FAIL with incorrect amounts.
- [ ] Implement overflow-safe multiplication and rounding inside the reports package, retaining current output for existing small fixtures.
- [ ] Add a real invoice/payment report test proving P&L/BAS use the corrected shares and preserve exact payment totals. Run unit/integration suites. Expected: PASS.
- [ ] Commit: `fix: prevent overflow in report payment allocation`.

### Task 4: Revalidate payable state while holding invoice lock

**Files:** `internal/billing/billing.go`, `internal/billing/billing_integration_test.go`.

**Interfaces:** Keep `Service.Send`, `Service.Issue` and provider interfaces unchanged. `sendToProvider` must inspect current status as well as provider reference under its existing `FOR UPDATE` lock; only an open, unsent invoice may reach `EnsureCustomer`/`SendInvoice`.

- [ ] Promote scratch reproduction as `TestServiceSendRejectsVoidedAfterInitialRead`. Assert `core.ErrInvalidState`, no new provider reference, and no provider invoice/customer creation. Use the injected provider resolver to interleave the committed void deterministically, not timing or sleeps.
- [ ] Add equivalent `TestServiceSendRejectsPaidAfterInitialRead`, recording a full real payment in the interleave; confirm the paid invoice remains paid and unsent.
- [ ] Run integration suite. Expected: both new cases FAIL because send only rechecks provider reference.
- [ ] Read status in the locked query and reject non-open state before any provider side effects. Keep provider idempotency, detached timeout and transaction behavior unchanged.
- [ ] Run unit/integration suites. Expected: PASS, including existing issue, retry, void-failure and webhook tests.
- [ ] Commit: `fix: reject stale invoice sends after void or payment`.

### Task 5: Strengthen adversarial API and real-browser workflow coverage

**Files:** new `internal/httpapi/security_integration_test.go`, existing integration tests under `internal/portal/` and `internal/timesheets/`, new `web/e2e/financial-workflows.spec.ts`; reuse `web/e2e/smoke.spec.ts` fixture patterns. Change production handlers only when a new regression demonstrates a defect; ledger additional decisions.

**Interfaces:** Existing HTTP APIs and role permissions only; no new feature endpoints. New browser fixture owns a dedicated `shigoto_e2e_financial` database and local server with scheduler off; it must not target any operator database.

- [ ] Add a table-driven HTTP test using owner/staff/bookkeeper/tenant/client/anonymous sessions. Exercise settings PUT, invoices create/issue, payments, reports CSV, receipt download and time-entry mutations; assert existing permission policy, foreign-org denial, unchanged row counts on denied writes, and no foreign identifiers/data in responses. Reuse existing tests where a case is already covered rather than duplicating it.
- [ ] Add contact-isolation regressions for two tenants in one org and the same user in two orgs: invoice lists/details and leases expose only the membership's own contact. Test revoked membership and expired-session denial.
- [ ] Add Playwright flows that create and issue a manual invoice, log time and bill it once, create a property/lease and read its ledger, match a bank credit to an invoice and inspect a report/CSV. Assert persisted amounts and statuses through HTTP, not just toast text. Use local fakes/no provider; never live payments.
- [ ] Add a mid-invoice-form staff session expiry case: expire only that fixture's session in Postgres, submit, reauthenticate, and verify input remains. Portal expiry must return to login, not the staff re-login dialog.
- [ ] Run unit, integration, frontend and browser suites. Expected: PASS. Existing-behavior tests may be green immediately; any discovered defect requires a separate observed RED-to-GREEN fix before completion.
- [ ] Commit: `test: cover financial workflows and access boundaries`.

### Task 6: Make release checks reproducible and record remaining risks

**Files:** `Taskfile.yml`, `.github/workflows/validate.yml`, `README.md`, `docs/superpowers/plans/2026-11-05-01-followups.md`; change dependency lockfiles only for an independently verified advisory fix.

**Interfaces:** Add `go-task verify` for lint, unit/race, integration, frontend tests, build and browser tests, executed sequentially where build/test databases require it. Add `go-task audit` for full npm audit and a pinned govulncheck version selected from the completed baseline scan (v1.9.0); do not mix advisory checks with deterministic offline checks or suppress audit failures silently.

- [ ] Verify individual baseline commands and identify all current environmental/test failures before adding wrappers. Missing browsers need documented `npx playwright install chromium`, not a skipped test. Bound integration tests with a realistic Go test timeout and retain diagnostic logs for hangs.
- [ ] Wire `verify` from existing tasks without duplicating implementations. Run it from the worktree. Expected: PASS; verify a failing subtask propagates a nonzero exit.
- [ ] Add the separate advisory task and CI invocation. Triage the deepmerge advisory's actual build-input exposure; apply a compatible upstream patch only if available and tested. If no safe patch exists, record a narrowly scoped advisory exception with rationale and review condition, requiring user approval before accepting that exception. No `npm audit fix --force` or unreviewed downgrade.
- [ ] Update README test prerequisites and follow-up entries only for verified fixes. Record unresolved financial edge policies, upload failure cleanup, resource-exhaustion limits, incomplete browser/OIDC acceptance, and any unreviewed subsystems explicitly; this plan does not imply those are solved.
- [ ] Run final `go-task verify`, `go-task audit`, inspect diff and obtain one independent whole-change review before merge. Fix important review findings with failing regressions; report remaining advisory/acceptance blockers accurately.
- [ ] Commit: `test: add reproducible predeployment verification gate`.

## Execution order and handoff

Tasks 1–4 are independent fixes. Task 5 uses their secured behavior; Task 6 composes
all checks. Execute inline with one final independent review to minimize repeated
repository exploration. Review this plan before implementation; no application
source has been changed during the audit.
