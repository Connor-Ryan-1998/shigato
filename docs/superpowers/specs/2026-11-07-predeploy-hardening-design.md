# Pre-deployment hardening

## Intent

Harden and test shigoto before deployment. The user approved a risk-first audit,
confirmed-defect fixes with regression tests, critical workflow tests, and a
repeatable release gate. Deployment is explicitly excluded.

## Approaches

- Backlog-only cleanup: smaller investigation, but misses undiscovered defects.
- Risk-first audit and fixes: selected in conversation. Verify old findings
  against current code, prioritize security and financial integrity, then test
  critical user workflows. Do not treat stale backlog entries as current defects.

## Constraints and success

- Assume one application instance, a small staff team, multiple organisations,
  and tenant/client portal users. Concurrent requests and hostile internet input
  still matter at this scale.
- Preserve existing permissions, contact isolation, session rotation, token
  consumption, provider idempotency, and financial rounding contracts.
- Every defect fix needs a regression test observed failing first.
- Use existing Go, Postgres, Svelte, Vitest and Playwright tooling. No new runtime
  dependencies or broad refactors.
- Deployment, new product features, credit notes/refunds, cosmetic cleanup and
  speculative performance work are non-goals.
- No live Stripe, SMTP or identity-provider credentials; use isolated test data.
- Passing existing tests alone does not establish release readiness. Resolve or
  explicitly record all confirmed high-impact findings and remaining acceptance
  checks; never label an incomplete audit as a security certification.

## Builder decisions

Keep security, money calculations, billing state transitions, workflow tests and
release automation as independently verifiable tasks. Preserve allocation policy
while removing arithmetic overflow; changing allocation policy requires separate
approval. Keep historical audit-log cleanup separate from redacting future writes:
rewriting existing audit history is not authorized.
