# Local sandbox population

## Intent

Provide realistic, persistent data for people manually testing Shigoto locally with one command:

```bash
docker compose exec -T app /shigoto admin populate-sbx
```

## Approved approach

Seed the configured development database rather than running a separate sandbox service. A separate database offers stronger separation but slows the requested development feedback loop. Disposable browser fixtures do not support persistent manual exploration.

Repeat runs replace only owned sandbox data, including tester edits. Preserve unrelated organisations, users and records. Refuse name/email collisions rather than adopting existing records. Never truncate the database or reset global sequences.

## Dataset

Create two recognisable sandbox organisations, with a shared owner for organisation switching. Include staff and bookkeeper accounts, populated tenant/client portal accounts, and empty portal accounts. Provide another tenant in the same organisation and a tenant in the other organisation for isolation checks.

Seed residential and commercial properties, active and ended leases, rent history, charges, projects/tasks, billed and unbilled time, expenses, bank accounts and matched/unmatched bank lines. Include draft, open, overdue, partially paid, paid and void invoices. Use at least 60 invoices in the main organisation to exercise the default invoice page size of 50. Dates are relative to the population date in the configured timezone.

Use existing domain functions for validation, money calculations and invoice/payment transitions. Do not call external payment providers, send mail during population, or bypass authentication. Tenant/client users have no local password and continue using normal magic-link login through local Mailpit.

## Ownership and safety

Record organisation and user ownership explicitly in a small database registry, created by a normal migration. Serialise concurrent population commands. Validate collisions and external references before deleting anything. Reset and population run in one transaction, so failure preserves the previous dataset. Refuse reset if sandbox users or records have become dependencies of unrelated data.

Generate staff passwords at runtime and print them only after successful commit. Never persist generated secrets in repository files or test snapshots. Print organisation URLs, account emails, the Mailpit URL and normal magic-link login instructions. Warn that rerunning replaces sandbox edits. Local Compose is the intended environment; this command is not permission to run against an operator database.

## Scope and proof

No new screens, login shortcuts, configurable dataset profiles, infrastructure deployment or broad test-harness refactor. Existing uncommitted UI changes must remain untouched.

Test populated relationships and financial states, real portal visibility, collision refusal, atomic rollback, concurrent execution, repeat-run replacement and preservation of unrelated data. Run repository Taskfile checks sequentially through `uv run --locked`. Do not populate the user's running development database as part of automated verification.
