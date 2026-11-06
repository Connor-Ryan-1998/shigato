# shigoto

A small self-hosted app for property management, timesheets and invoicing,
shipped as one Go binary plus Postgres. See `docs/design.md` for the design.

## Requirements

Docker plus [go-task](https://taskfile.dev), with Go on the host. Node 24 is
only needed to build or develop the web UI (`task web:*`); `go build`/`go
test` work without it (`internal/webui` serves a built-in placeholder page
when `internal/webui/dist` has no build in it).

On a machine where Docker build traffic is TLS-intercepted (a corporate
laptop CA), pass it as a build secret:

```bash
docker build --secret id=laptop_ca,src=/opt/homebrew/etc/ca-certificates/cert.pem -t shigoto .
```

## Quickstart

```bash
docker compose up -d --build

# create the first org and its owner (password is read from stdin)
echo "local-dev-password" | docker compose exec -T app /shigoto admin create-owner \
  --org-slug me --org-name "Me" --email me@example.com

# log in and call the API with the session cookie
curl -c j -X POST localhost:8080/api/v1/auth/login -H 'X-Shigoto-CSRF: 1' \
  -d '{"email":"me@example.com","password":"local-dev-password"}'
curl -b j localhost:8080/api/v1/orgs/me/contacts
```

### API notes

- Every unsafe request (`POST`, `PUT`, `PATCH`, `DELETE`) to `/api/` must
  send `X-Shigoto-CSRF: 1`, or it is rejected with 403. If the request has
  an `Origin` header, it must match `SHIGOTO_BASE_URL`'s origin. `/webhooks/`
  is exempt. A browser form on another site can't set custom headers, so
  this blocks cross-site request forgery.
- Every error body is `{"error": "<message>"}`.
- Paged list endpoints return the page plus the total match count (ignoring
  `limit`/`offset`): `GET /api/v1/orgs/{org}/invoices` returns
  `{"invoices": [...], "total": n}` and `GET /api/v1/orgs/{org}/bank-lines`
  returns `{"lines": [...], "total": n}`. `limit` defaults to 50, max 200.

## Usage

```bash
task test                    # unit tests (SUITE=unit, default)
task test SUITE=integration  # brings up db via docker compose, then integration tests
task build                   # builds bin/shigoto (includes task web:build)
task dev                     # runs db in docker compose, then the server locally
task lint                    # gofmt -l, go vet, and task web:check

task web:install              # npm ci in web/
task web:build                # builds the SPA into internal/webui/dist
task web:test                 # svelte-check plus vitest (--run)
task web:dev                  # vite dev server, proxying /api and /webhooks to task dev's backend
task web:e2e                  # Playwright smoke test against a real binary + Postgres
```

### Web UI dev workflow

Run the backend and the web dev server side by side:

```bash
task dev       # terminal 1: Postgres + the Go server on :8080
task web:dev   # terminal 2: Vite on :5173, proxying /api and /webhooks to :8080
```

Open `http://localhost:5173`. The dev proxy rewrites the proxied request's
`Origin` header to the backend's own origin, so the CSRF check in
`internal/httpapi/csrf.go` still passes even though the browser's real
origin is Vite's port, not the backend's.

In production the SPA is built (`task web:build`) into
`internal/webui/dist` and embedded in the Go binary
(`internal/webui/webui.go`), served for any path that isn't `/api/`,
`/webhooks/` or `/healthz`. `go build`/`go test` don't need Node: only
`internal/webui/dist/.gitkeep` is committed (everything else under `dist`
is gitignored, since `task web:build` empties the directory before writing
to it), and `webui.Handler()` serves a built-in "Web UI not built" page
when `index.html` is missing.

## Configuration

The server is configured by environment variables, all prefixed `SHIGOTO_`:

| Variable | Required | Default | Description |
|---|---|---|---|
| `SHIGOTO_LISTEN` | no | `:8080` | Address to listen on. |
| `SHIGOTO_DATABASE_URL` | yes | | Postgres connection URL. |
| `SHIGOTO_MODULES` | no | all of `property,timesheets,reconciliation` | Comma list of modules to enable. |
| `SHIGOTO_FILES_DIR` | no | `./data/files` | Directory for uploaded files. |
| `SHIGOTO_BASE_URL` | yes | | Public base URL, used for webhooks and links. |
| `SHIGOTO_TIMEZONE` | no | `Australia/Brisbane` | Timezone invoice dates (issue_date, due_date) are calculated in. |
| `SHIGOTO_SCHEDULER` | no | `on` | `on` or `off`. Enables the in-process hourly scheduler (rent runs and session pruning). |
| `SHIGOTO_STRIPE_<ORGSLUG>_SECRET_KEY` | no | | Stripe secret key for the org identified by slug. |
| `SHIGOTO_STRIPE_<ORGSLUG>_WEBHOOK_SECRET` | no | | Stripe webhook signing secret for the org. |
| `SHIGOTO_SMTP_HOST` | no | | SMTP server host. Unset disables email: invite/magic-link endpoints return 422 and hand the link back for manual sharing. |
| `SHIGOTO_SMTP_PORT` | no | `587`/`465`/`1025` by `SHIGOTO_SMTP_TLS` | SMTP server port. |
| `SHIGOTO_SMTP_USERNAME` | no | | SMTP auth username. |
| `SHIGOTO_SMTP_PASSWORD` | no | | SMTP auth password. |
| `SHIGOTO_SMTP_FROM` | yes, if `SHIGOTO_SMTP_HOST` is set | | `From` address for outgoing mail. |
| `SHIGOTO_SMTP_TLS` | no | `starttls` | `starttls`, `tls` (implicit TLS, e.g. port 465) or `none`. `none` is only accepted for host `localhost`, `127.0.0.1`, `::1` or `mailpit`. |
| `SHIGOTO_TRUSTED_PROXY` | no | | CIDR of a reverse proxy to trust. When set and a request's `RemoteAddr` is inside it, the right-most `X-Forwarded-For` entry that isn't itself inside the CIDR is used as the client IP for login rate limiting. |

`<ORGSLUG>` is the org slug upper-cased with `-` replaced by `_`. Point each
org's Stripe webhook at `https://<SHIGOTO_BASE_URL>/webhooks/stripe/<org-slug>`
(the plain, lower-cased slug, not the env var form).

### Email in development

`docker compose up` also starts [mailpit](https://mailpit.axllent.org/) as a
local SMTP catcher, with its SMTP port (1025) and web UI (8025) bound to
`127.0.0.1` only. The `app` service is pre-configured to send through it
(`SHIGOTO_SMTP_HOST=mailpit`, `SHIGOTO_SMTP_TLS=none`); open
`http://localhost:8025` to see mail sent by the running app. `task dev` runs
the Go server on the host rather than in compose, so point it at mailpit the
same way if you need outgoing mail locally:

```bash
docker compose up -d --wait db mailpit
SHIGOTO_SMTP_HOST=localhost SHIGOTO_SMTP_PORT=1025 \
  SHIGOTO_SMTP_FROM=noreply@shigoto.local SHIGOTO_SMTP_TLS=none \
  task dev
```

## Commands

```bash
bin/shigoto serve                     # start the server (default)
bin/shigoto admin create-owner \
  --org-slug <slug> --org-name <name> --email <email>  # password read from stdin
```

## Money and GST rounding

All money is represented in cents as `int64`. GST is calculated per invoice or expense line and rounded half away from zero to the cent. The rounding rule for n/d is: for n≥0, use (2n+d)/(2d); for n<0, negate, compute, and negate the result. This ensures that 0.5 cents rounds away from zero in both directions.

Rent, charge and `opening_arrears` amounts on a lease are entered GST-exclusive. Residential rent is input-taxed (`INP`), so no GST is added. For non-residential property, GST is added on top when the org is GST-registered (`GST`); otherwise the line is GST-free (`FRE`). A non-residential rent of $500.00 therefore bills $550.00 for a registered org.
