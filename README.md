# shigoto

A small self-hosted app for property management, timesheets and invoicing,
shipped as one Go binary plus Postgres. See `docs/design.md` for the design.

## Requirements

Docker plus [go-task](https://taskfile.dev), with Go on the host. Node 24 is
only needed to build or develop the web UI (`task web:*`); `go build`/`go
test` work without it against the committed placeholder in
`internal/webui/dist`.

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
curl -c j -X POST localhost:8080/api/v1/auth/login \
  -d '{"email":"me@example.com","password":"local-dev-password"}'
curl -b j localhost:8080/api/v1/orgs/me/contacts
```

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
`/webhooks/` or `/healthz`. `go build`/`go test` don't need Node: a
placeholder `internal/webui/dist/index.html` is committed so the embed
always has something to build with; `task web:build` overwrites it with the
real build (harmless locally - that file is otherwise gitignored).

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

`<ORGSLUG>` is the org slug upper-cased with `-` replaced by `_`. Point each
org's Stripe webhook at `https://<SHIGOTO_BASE_URL>/webhooks/stripe/<org-slug>`
(the plain, lower-cased slug, not the env var form).

## Commands

```bash
bin/shigoto serve                     # start the server (default)
bin/shigoto admin create-owner \
  --org-slug <slug> --org-name <name> --email <email>  # password read from stdin
```

## Money and GST rounding

All money is represented in cents as `int64`. GST is calculated per invoice or expense line and rounded half away from zero to the cent. The rounding rule for n/d is: for n≥0, use (2n+d)/(2d); for n<0, negate, compute, and negate the result. This ensures that 0.5 cents rounds away from zero in both directions.

Rent, charge and `opening_arrears` amounts on a lease are entered GST-exclusive. Residential rent is input-taxed (`INP`), so no GST is added. For non-residential property, GST is added on top when the org is GST-registered (`GST`); otherwise the line is GST-free (`FRE`). A non-residential rent of $500.00 therefore bills $550.00 for a registered org.
