# shigoto Plan 6: Identity, Staff Management and the Tenant/Client Portal

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:**
- Staff can sign in with OIDC (Authentik in production) as well as local passwords.
- Owners can invite staff and manage roles.
- Logins are rate limited, and an expired session can be re-authenticated without losing the page.
- Tenants and clients get a portal that they sign into by emailed magic link. Each sees only their own leases, invoices and payment details.

**Architecture:**
- **`internal/mail`:** sends email over SMTP using `net/smtp`. The sender is a plain function field on a struct, so tests can capture messages. This adds no new interface.
- **`internal/auth`:**
  - Rate limiting.
  - Invites.
  - Magic links.
  - An OIDC authorization-code flow with PKCE (`github.com/coreos/go-oidc/v3`, `golang.org/x/oauth2`). Its state is stored in Postgres, so no new secret is needed.
- **`internal/portal`:** contact-scoped read endpoints under `/api/v1/portal/...`.
- **SPA:** gains a members page, an SSO button, a re-login dialog, and a separate `/portal` area with its own minimal layout.
- **Dev and test:** `mailpit` is added to `docker-compose.yml` for local and e2e email.

**Tech Stack:**
- Go 1.27, pgx/v5.
- New Go dependencies: `github.com/coreos/go-oidc/v3` and `golang.org/x/oauth2`. Use exact current versions and record them in the report.
- SvelteKit 3 / Svelte 5.
- Playwright (e2e only).
- `axllent/mailpit` with an exact image tag, used in compose only.

**Spec:** `docs/design.md` (login decision: staff use generic OIDC plus local login; tenants and clients use an email magic link, with no Authentik), `docs/superpowers/plans/2026-11-05-01-followups.md` ("Plan 6" and "Plan 5 leftovers").

## Global Constraints

- Everything from Plans 1–5 still applies:
  - Integer cents.
  - `org_id` on every query.
  - New migrations only (the next one is `0012_`).
  - `module.WriteCoreError` for errors.
  - Strict full-replace PUTs.
  - Audit every write.
  - CSRF header on unsafe `/api` requests.
  - Pinned dependencies.
  - The SPA patterns: errors shown inside dialogs, refetches that keep content mounted, stale-response guards.
  - Run `task lint`, `task test`, `task test SUITE=integration`, `task web:test`, `task web:build`, `task web:e2e`.
- **Tokens:**
  - Every invite, magic-link and OIDC-state token is 32 random bytes from `crypto/rand`, encoded base64url.
  - Only its SHA-256 is stored.
  - Each is single use, consumed with `UPDATE ... SET used_at = now() WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now() RETURNING ...`.
  - Lifetimes: magic link 15 minutes, staff invite 7 days, OIDC state 10 minutes.
- **No account enumeration:** `POST /auth/magic` and failed logins return the same response whether or not the email exists.
- **OIDC never auto-provisions:**
  - The ID token's `email_verified` must be `true`.
  - A user is found by `oidc_subject`. Failing that, by email among existing users, whose `oidc_subject` is then set, but only if it is NULL.
  - An unknown email is rejected with "No shigoto account for this email — ask an owner to invite you."
  - OIDC sign-in is for staff roles only. A user whose only memberships are tenant or client cannot use OIDC.
- **Portal isolation:**
  - Every portal query is scoped by `org_id` and by the membership's `contact_id`.
  - Tenants see only leases where their contact is in `lease_tenants`, and invoices addressed to their contact.
  - Clients see only invoices addressed to their contact.
  - Draft and void invoices are never shown in the portal.
  - Staff endpoints keep rejecting tenant/client roles, since those roles hold no staff permissions.
- **Email:**
  - Configured with `SHIGOTO_SMTP_HOST`, `SHIGOTO_SMTP_PORT`, `SHIGOTO_SMTP_USERNAME`, `SHIGOTO_SMTP_PASSWORD`, `SHIGOTO_SMTP_FROM` and `SHIGOTO_SMTP_TLS`.
  - `SHIGOTO_SMTP_TLS` is one of `starttls` (the default), `tls` or `none`. Use `none` only for a host on loopback or the compose network. Validate that at config load: `none` is rejected unless the host is `localhost`, `127.0.0.1`, `::1` or `mailpit`.
  - If `SHIGOTO_SMTP_HOST` is unset, email is disabled. Invite and magic-link endpoints then return 422 "email is not configured", and the invite link is still returned to the owner so it can be shared by hand.
- **Rate limits:**
  - At most 5 failed logins per email per 15 minutes.
  - At most 20 failed logins per client IP per 15 minutes.
  - At most 5 magic-link requests per email per hour.
  - Requests over a limit get 429 with `Retry-After`.
  - The limiter is in-memory. Mark it with `// ponytail: in-memory limiter, per instance; move to Postgres if shigoto ever runs >1 replica`.
  - The client IP is `r.RemoteAddr`. If `SHIGOTO_TRUSTED_PROXY` is set to a CIDR, the right-most untrusted `X-Forwarded-For` hop is used instead.

## Review Focus

1. **A tenant guesses another tenant's invoice or lease ID in the portal URL.** The portal returns 404 and leaks nothing. The same applies to another org's IDs. Test in Task 5.
2. **A magic link is clicked twice, or after 15 minutes.** The second or late click lands on the login page with "This link has expired — request a new one", and no session is created. Test in Task 5.
3. **An OIDC callback arrives with a replayed or foreign `state`, a mismatched nonce, `email_verified=false`, or an email nobody invited.** No session is created and a clear error is shown. Test in Task 4.
4. **The last owner is demoted or removed.** The request gets 409 "an organisation needs at least one owner". Test in Task 3.
5. **Brute-force login.** The 6th failure for one email within 15 minutes gets 429. After the window passes, a correct password works. A successful login does not reset the per-IP count. Test in Task 2.

---

### Task 1: Email delivery and config

**Files:**
- Create: `internal/mail/mail.go`, `internal/mail/mail_test.go`
- Modify: `internal/config/config.go` (+ `config_test.go`), `docker-compose.yml` (add `mailpit` with an exact image tag, ports 1025/8025 on loopback only), `README.md` (SMTP variables and mailpit for development)

**Interfaces:**
- Produces:
  - `type Message struct{ To, Subject, Text, HTML string }`.
  - `type Mailer struct{ From string; Send func(ctx context.Context, m Message) error }`.
  - `func New(cfg config.SMTP) *Mailer` returns nil when SMTP isn't configured.
  - The SMTP send uses `net/smtp` with a 15-second deadline, honouring the TLS mode.
  - Messages are built as `multipart/alternative` with CRLF line endings.
  - Header values are sanitised: CR and LF are stripped from `To` and `Subject`.
  - `config.SMTP{Host string; Port int; Username, Password, From string; TLS string}`, plus validation per the Global Constraints.

- [ ] **Step 1: Write failing tests.**
  - Config: the default TLS mode is starttls; `none` with host `smtp.example.com` is an error; `none` with `mailpit` is OK; a missing `From` while `Host` is set is an error.
  - Mail: a built message contains both text and HTML parts. A `To` containing `\r\nBcc:` is sanitised.
  - Integration (tag `integration`): sending through compose mailpit makes the message appear via `GET http://localhost:8025/api/v1/messages`.
- [ ] **Step 2: Run them and confirm they FAIL.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run all suites. Expected: PASS.**
- [ ] **Step 5: Commit:** `feat: smtp mailer and config with mailpit for development`

### Task 2: Login rate limiting and re-authentication

**Files:**
- Create: `internal/auth/ratelimit.go` and `ratelimit_test.go`, `web/src/lib/components/ReloginDialog.svelte` and `ReloginDialog.test.ts`
- Modify:
  - `internal/auth/middleware.go`: login handler uses the limiter.
  - `internal/config/config.go`: `SHIGOTO_TRUSTED_PROXY`.
  - `web/src/lib/api.ts`: on a 401 outside `/login`, open the re-login dialog and retry the request once it succeeds, instead of navigating away.
  - `web/src/routes/+layout.svelte`: mount the dialog.

**Interfaces:**
- Produces:
  - `type Limiter struct{...}`, `func NewLimiter(now func() time.Time) *Limiter`.
  - `func (l *Limiter) Allow(key string, max int, window time.Duration) (ok bool, retryAfter time.Duration)`.
  - `func (l *Limiter) Fail(key string, window time.Duration)`.
  - A sliding window per key, behind a mutex, with expired keys pruned on access.
  - `func ClientIP(r *http.Request, trusted *net.IPNet) string`.
- Login checks both keys, `email:<lowercased>` and `ip:<addr>`, before verifying the password. A failure calls `Fail` on both keys. Success does not reset either count.
- Client side:
  - `api.ts` accepts an `onUnauthorized` that returns `Promise<boolean>`. The default opens `ReloginDialog`, which posts to `/auth/login` with the same email.
  - On success, `api.ts` retries the original request once.
  - Cancelling falls back to the old redirect to `/login?next=`.
  - Concurrent 401s share one dialog and one promise.

- [ ] **Step 1: Write failing tests.**
  - Go:
    - `TestLimiterSlidingWindow`, using a fake clock.
    - `TestLoginRateLimitedByEmail` (integration): 5 failures followed by any attempt gives 429 with `Retry-After`. After advancing the fake clock past 15 minutes, a correct login succeeds. Inject the clock through `auth.Auth`.
    - `TestLoginRateLimitedByIP`.
    - `TestClientIPTrustedProxy`: XFF is ignored unless `RemoteAddr` is inside the trusted CIDR.
  - Web:
    - `api.test.ts`: two concurrent 401s, then dialog success, then both requests retried once.
    - Cancelling the dialog gives the redirect.
    - `ReloginDialog.test.ts` shows the server's error inside the dialog.
- [ ] **Step 2: Run them and confirm they FAIL.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run all suites. Expected: PASS.**
- [ ] **Step 5: Commit:** `feat: login rate limiting and in-place re-login on session expiry`

### Task 3: Staff invites and member management

**Files:**
- Create: `internal/db/migrations/0012_invites.sql`, `internal/auth/invites.go`, `internal/httpapi/members.go`, `internal/httpapi/members_integration_test.go`, `web/src/routes/[org]/settings/members/+page.svelte`, `web/src/routes/invite/[token]/+page.svelte`, `web/src/lib/modules/core/members/*` (with a test)
- Modify: `web/src/lib/modules/core/manifest.ts` (nav entry "Members", perm `org_admin`)

**`0012_invites.sql`:**
- `invites`:
  - `id`, `org_id NOT NULL REFERENCES orgs`, `email citext NOT NULL`, `role text NOT NULL CHECK (role IN ('owner','staff','bookkeeper'))`.
  - `token_hash bytea NOT NULL UNIQUE`, `invited_by bigint REFERENCES users`, `expires_at timestamptz NOT NULL`, `used_at timestamptz NULL`, `created_at`.
- Add a partial unique index on `(org_id, email) WHERE used_at IS NULL`, so an outstanding invite is replaced rather than duplicated.

**Interfaces:**
- **Routes, `PermOrgAdmin` unless noted:**
  - `GET /api/v1/orgs/{org}/members` returns `[{user_id, name, email, role, contact_id?, sso_linked}]`.
  - `POST /api/v1/orgs/{org}/invites` takes `{email, role}`. It creates the invite, replacing any outstanding one for that email, and emails `<BaseURL>/invite/<token>` if mail is configured. The response is `{invite_id, link}`, where `link` is always returned to the owner.
  - `GET /api/v1/orgs/{org}/invites` lists outstanding invites without tokens.
  - `DELETE /invites/{id}` revokes an invite.
  - `PUT /api/v1/orgs/{org}/members/{user_id}` takes `{role}`.
  - `DELETE /api/v1/orgs/{org}/members/{user_id}`.
  - Demoting or removing the last owner returns 409 "an organisation needs at least one owner". The check runs under a lock on the org's membership rows: `SELECT ... FOR UPDATE` over the owners.
- **Public:**
  - `GET /api/v1/invites/{token}` returns `{org_name, email, role, expired: bool}`.
  - `POST /api/v1/invites/{token}/accept`:
    - Logged in with a matching email: adds the membership.
    - Logged in with a different email: 409.
    - Not logged in: the body `{name, password}` creates the user with a bcrypt password, minimum 12 characters, plus the membership, and logs them in.
    - The token is consumed atomically in the same transaction.
    - If the user already exists and is not logged in: 409 "sign in first, then open the link again".
- **SPA:**
  - Members page: a table with role select and remove; an invite dialog that shows the link (with copy) when email is disabled; a list of outstanding invites with revoke.
  - `/invite/[token]` page: shows the org and role, then offers accept-while-logged-in or a create-account form.

- [ ] **Step 1: Write failing tests.**
  - Go integration:
    - `TestInviteAcceptNewUser`.
    - `TestInviteSingleUse`: the second accept gives 410 or 422.
    - `TestInviteExpired`.
    - `TestInviteEmailMismatchWhenLoggedIn409`.
    - `TestLastOwnerProtected`: covers both demote and remove.
    - `TestMembersCrossOrg404`.
    - `TestInviteEmailSent`: a capture mailer receives a message containing the link.
    - `TestStaffCannotInvite403`.
  - Web: the members page role change sends a PUT. A 409 shows inline.
- [ ] **Step 2: Run them and confirm they FAIL.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run all suites. Expected: PASS.**
- [ ] **Step 5: Commit:** `feat: staff invites and member role management`

### Task 4: OIDC staff sign-in

**Files:**
- Create: `internal/db/migrations/0013_oidc_states.sql`, `internal/auth/oidc.go`, `internal/auth/oidc_integration_test.go`, `internal/auth/oidctest/` (a fake IdP for tests: discovery, JWKS and token endpoint, signing RS256 with a generated key)
- Modify:
  - `internal/config/config.go`: `SHIGOTO_OIDC_ISSUER`, `SHIGOTO_OIDC_CLIENT_ID`, `SHIGOTO_OIDC_CLIENT_SECRET`, `SHIGOTO_OIDC_SCOPES`, default `openid email profile`. All three of issuer, client ID and secret are required together, or none of them.
  - `internal/httpapi/server.go`: mount the routes; `/api/v1/meta` gains `"sso": true|false`.
  - `web/src/routes/login/+page.svelte`: "Sign in with SSO" button when `sso` is true, and display of an `error` query parameter.

**`0013_oidc_states.sql`:**
- `oidc_states`: `state_hash bytea PRIMARY KEY`, `nonce text NOT NULL`, `code_verifier text NOT NULL`, `next text NOT NULL`, `expires_at timestamptz NOT NULL`, `used_at timestamptz NULL`.
- The scheduler's prune job also deletes expired `oidc_states`, and expired or used magic links from Task 5.

**Interfaces:**
- **`GET /api/v1/auth/oidc/start?next=/acme`:**
  - Generates the state, nonce and PKCE verifier, and stores the hashed state.
  - Redirects (302) to the IdP.
  - `next` is validated: it must be a path starting with `/` but not `//` or `/\`, otherwise `/`.
- **`GET /api/v1/auth/oidc/callback?code&state`:**
  1. Consume the state atomically. If it is missing, used or expired, redirect to `/login?error=sso_expired`.
  2. Exchange the code with the verifier.
  3. Verify the ID token: issuer, audience, expiry and nonce.
  4. Require `email_verified`.
  5. Resolve the user per the Global Constraints. Reject a user who has no staff membership.
  6. Create the session cookie, exactly as local login does, and redirect to `next`.
  7. Every failure redirects to `/login?error=<code>` with a fixed message map: no IdP error text is reflected back, and nothing is logged beyond the code.
- **Ordering:** the OIDC provider discovery happens lazily on first use, with a 10-second timeout, and is cached. A discovery failure gives `/login?error=sso_unavailable`, never a server crash at startup.

- [ ] **Step 1: Write failing integration tests against the fake IdP.**
  - The happy path links `oidc_subject`, sets the cookie, and redirects to `next`.
  - A replayed state is rejected.
  - A foreign state is rejected.
  - A nonce mismatch is rejected.
  - `email_verified=false` is rejected.
  - An unknown email gives `not_invited`.
  - A tenant-only user gives `not_staff`.
  - A subject already linked to a different user is rejected.
  - `next=//evil.com` falls back to `/`.
  - `/meta` reports `sso`.
- [ ] **Step 2: Run them and confirm they FAIL.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run all suites. Expected: PASS.**
- [ ] **Step 5: Commit:** `feat: oidc staff sign-in with pkce and invite-only account linking`

### Task 5: Portal backend: magic links and contact-scoped API

**Files:**
- Create: `internal/db/migrations/0014_magic_links.sql`, `internal/auth/magic.go`, `internal/portal/portal.go`, `internal/portal/routes.go`, `internal/portal/portal_integration_test.go`
- Modify:
  - `internal/httpapi/server.go`: mount the portal routes, and `POST /api/v1/orgs/{org}/contacts/{id}/portal-invite` (`PermContactsWrite`).
  - `/api/v1/me`: tenant and client memberships already appear with empty perms. Add `"portal": true` for those orgs.

**`0014_magic_links.sql`:**
- `magic_links`: `token_hash bytea PRIMARY KEY`, `user_id bigint NOT NULL REFERENCES users`, `expires_at timestamptz NOT NULL`, `used_at timestamptz NULL`, `created_at`.

**Interfaces:**
- **`POST /api/v1/orgs/{org}/contacts/{id}/portal-invite`:**
  - The contact must have an email, and its kind must be `tenant` or `client`.
  - It finds or creates a user with that email (no password) and adds the membership with role = contact kind and `contact_id` = the contact. An existing membership in this org with a different contact gives 409.
  - Sends a magic link. Returns `{link}` only when email is disabled.
- **`POST /api/v1/auth/magic`** takes `{email}`. It always returns 202 `{}`.
  - If a user with that email has at least one tenant or client membership, it creates a token and emails `<BaseURL>/auth/magic?token=`.
  - Rate limited per email.
  - Staff-only users get nothing, and must use password or SSO.
- **`GET /auth/magic?token=`** is served by the SPA page, which POSTs `/api/v1/auth/magic/verify` with `{token}`.
  - Consumes the token atomically and creates the session.
  - Returns `{redirect:"/portal"}`.
  - An expired or used token gives 410 "This link has expired — request a new one".
- **Portal API** (session required, membership role tenant or client in `{org}`, otherwise 404):
  - `GET /api/v1/portal/orgs` returns `[{slug, name, role}]`.
  - `GET /api/v1/portal/{org}/invoices` returns invoices with `contact_id` = the membership contact and status open, paid or uncollectible. Uses the same `billing.InvoiceResponse` shape.
  - `GET /api/v1/portal/{org}/invoices/{id}`:
    - The single invoice, with `pay_options`, the org's bank BSB, account and PayID, `payment_reference`, `provider_url`, and its payments.
    - Org bank details come from settings, and appear only for the options listed in `pay_options`.
  - `GET /api/v1/portal/{org}/leases` (tenant only):
    - Leases where the contact is in `lease_tenants`.
    - Each has property name and address, rent amount and frequency, next due period, and a ledger summary with arrears and `invoiced_not_due`.
    - Reuses `property.GetLedger`.

- [ ] **Step 1: Write failing integration tests.**
  - `TestPortalInvoiceScoping`: tenant A cannot see tenant B's invoice (list excludes it; detail gives 404), nor another org's.
  - `TestPortalHidesDraftAndVoid`.
  - `TestPortalLeasesTenantOnly`: a client gets 404.
  - `TestMagicLinkSingleUseAndExpiry`.
  - `TestMagicRequestNoEnumeration`: the response is identical for an unknown email, and no mail is sent.
  - `TestMagicRateLimit`.
  - `TestPortalInviteCreatesMembership`, plus 409 on a contact conflict.
  - `TestStaffEndpointsRejectPortalUser`: `GET /orgs/{org}/invoices` as a tenant gives 403.
- [ ] **Step 2: Run them and confirm they FAIL.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run all suites. Expected: PASS.**
- [ ] **Step 5: Commit:** `feat: magic-link portal sign-in and contact-scoped portal api`

### Task 6: Portal UI and end-to-end

**Files:**
- Create:
  - `web/src/routes/portal/+layout.svelte` (minimal shell: org name, Invoices, Leases for tenants only, Logout, theme).
  - `web/src/routes/portal/+page.svelte` (picks the org).
  - `web/src/routes/portal/[org]/invoices/+page.svelte` and `[id]/+page.svelte`.
  - `web/src/routes/portal/[org]/leases/+page.svelte`.
  - `web/src/routes/auth/magic/+page.svelte`.
  - Tests.
  - `web/e2e/portal.spec.ts`.
- Modify:
  - `web/src/routes/login/+page.svelte`: an "Email me a sign-in link" panel.
  - `web/src/routes/+page.svelte` / root routing: a user with only portal memberships goes to `/portal`. A user with both kinds goes to their first staff org, and the shell's user menu gets a "Portal" link.
  - The staff contact UI does not exist yet, so add an "Invite to portal" action in the Members page dialog: pick a tenant or client contact from `GET /contacts`.

**Interfaces:**
- Portal invoice detail:
  - Status, number, amounts and due date.
  - "Pay by card" button linking to `provider_url`, shown only when that option exists.
  - Bank transfer and PayID details, with copy buttons and the payment reference shown prominently.
  - Payment history.
- Leases:
  - Property address, rent and frequency.
  - Arrears ("You're $X behind"), shown only when arrears are above 0, with the text as well as the colour.
  - Next due.
- No staff navigation or data ever renders under `/portal`.
- **e2e (`portal.spec.ts`)**, using compose mailpit:
  1. The owner logs in, creates a tenant contact with an email, and invites it to the portal.
  2. Fetch the magic link through the mailpit API.
  3. Open it and land on `/portal`.
  4. See the issued invoice with the payment reference.
  5. A second tenant's invoice ID typed into the URL gives "Not found".
  6. Reusing the link shows the expired message.

- [ ] **Step 1: Write failing tests.**
  - Component tests: portal invoice detail hides "Pay by card" without `provider_url`, and shows the reference. The magic page renders a 410 as the expired message.
  - Write `portal.spec.ts` as above.
- [ ] **Step 2: Run them and confirm they FAIL.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run all suites, including `task web:e2e` for both specs, and expect them to PASS.** Capture 360px and 1280px light and dark screenshots of the portal invoice and leases pages for the report.
- [ ] **Step 5: Commit:** `feat: tenant and client portal ui with magic-link e2e`

---

## Done for Plan 6

- All suites pass, including both e2e specs.
- Staff can sign in with SSO through a dev or fake IdP, or with a password. Owners invite staff and manage roles.
- Tenants and clients sign in by magic link and see only their own data.
- Login is rate limited, and an expired session re-authenticates in place.
- Update the follow-ups doc. The next plan covers the remaining property, timesheets and reports screens, then homelab deployment (Ansible role, Traefik, Authentik app, backups).
