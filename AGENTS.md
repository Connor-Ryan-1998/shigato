# Shigoto contributor guide

Read [Interface standards](docs/ui-standards.md) before adding or changing any
screen, including settings, authentication, and the customer portal.

- Reuse the existing Svelte/Tailwind/DaisyUI components and task-specific layouts.
- Use dialogs for short editors and dedicated routes for complex forms; do not
  embed permanently open creation forms below lists.
- Keep API calls in the existing `lib/api.ts` client and domain behavior in its
  owning module. Preserve organisation/request guards, permissions, financial
  warnings, and portal isolation.
- Every editor needs failed-input preservation, duplicate-submit protection,
  and explicit cancellation. Preserve dirty edits and block busy dismissal.
- Test changed behavior; update the standards' screen ledger for new routes.
- Run Taskfile validation sequentially through `uv run --locked`; see README
  and inherited repository instructions. Never use production/operator databases
  for browser tests or commit credentials, session cookies, or generated secrets.

These app-specific rules supplement, not replace, the parent repository guide.
