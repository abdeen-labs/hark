# Hark

Hark turns webhooks and API client requests into iOS push notifications, Live
Activities, and approval prompts you can answer from the Lock Screen. It runs as
one Go binary backed by PostgreSQL. The binary includes the API, admin dashboard,
and API documentation.

Hark is free, open source, and self-hosted. Each deployment supports one account.
There is no sign-up flow, billing, or analytics. Abdeen Labs does not provide a
hosted Hark service.

* **API reference:** [`docs/api.md`](docs/api.md) is the authoritative guide to
  API behavior and client development. Hark also serves it as searchable HTML at
  `/docs`, raw Markdown at `/docs.md`, OpenAPI 3.1 at
  [`/openapi.json`](docs/openapi.json), and a discovery index at
  [`/llms.txt`](docs/llms.txt). These routes are public. The contract also covers
  the embedded [dashboard](docs/api.md#dashboard).

---

## Requirements

* Go 1.26
* PostgreSQL 17

If you use Docker Compose, you do not need to install Go or PostgreSQL locally.

---

## Running

### Docker Compose

```sh
cp .env.example .env
# Set at least HARK_SECRET_KEY and HARK_ADMIN_PASSWORD in .env:
#   openssl rand -base64 48
docker compose up --build
```

The stack is PostgreSQL 17 plus `harkd`. PostgreSQL keeps its data in the named
volume `postgres-data` and the app waits for its healthcheck before starting.

```sh
curl -s localhost:8080/healthz
# {"status":"ok","database":"ok","version":"dev"}
```

Open <http://localhost:8080/> to use the admin dashboard. It includes the current
delivery status, history, webhook services, registered devices, API tokens, test
notifications, and command-line client authorization. See the
[dashboard reference](docs/api.md#dashboard) for its routes and behavior.

### The published contract

<http://localhost:8080/docs> renders [`docs/api.md`](docs/api.md) with search,
an endpoint outline, and copyable examples. The machine-facing forms are:

| URL | Format | Best for |
| --- | --- | --- |
| <http://localhost:8080/docs.md> | Markdown | Raw reference and text-based tools |
| <http://localhost:8080/openapi.json> | OpenAPI 3.1 JSON | Client generation, discovery, validation |
| <http://localhost:8080/llms.txt> | Plain text | Documentation discovery |

All four documentation routes are public and require no authentication. Their
content is embedded at build time, so each binary serves the documentation for
that build.

### Locally, against your own PostgreSQL

```sh
export DATABASE_URL='postgres://hark:hark@localhost:5432/hark?sslmode=disable'
export HARK_SECRET_KEY="$(openssl rand -base64 48)"
export HARK_ADMIN_PASSWORD='choose-something-long'

go run ./cmd/harkd
```

For dashboard development, run `sh scripts/dev.sh`. It starts a temporary
PostgreSQL container on port 54318, seeds sample data with
[`scripts/demo-seed.py`](scripts/demo-seed.py), and creates the development
account `admin` / `hark-dev-password`.

Migrations run automatically at startup. Hark exits immediately if the database
is unreachable or the configuration is invalid.

On `SIGINT` or `SIGTERM`, Hark stops accepting connections, waits up to
`HARK_SHUTDOWN_TIMEOUT` for active requests, and closes the database pool.
Pending [answer callbacks](docs/api.md#the-answer-callback) remain in the
database and resume after restart.

---

## The account

Hark is single-user. **There is no sign-up endpoint.** The database enforces the
one-account limit, including when multiple processes try to create an account at
the same time.

Create the account in one of two ways.

### Seeded at boot

Set `HARK_ADMIN_PASSWORD` and, optionally, `HARK_ADMIN_USERNAME` and
`HARK_ADMIN_EMAIL`. At startup, Hark creates the account only when the user table
is empty. If account creation fails, the server continues running, logs the
error, and has no account that can sign in.

### Created from the command line

```sh
printf '%s' 'correct horse battery staple' | harkd create-user -username admin
```

| Flag | Default | Notes |
| --- | --- | --- |
| `-username` | `HARK_ADMIN_USERNAME`, else `admin` | 3–30 characters of letters, digits, `_`, `.`. Lowercased. |
| `-email` | `HARK_ADMIN_EMAIL`, else `<username>@hark.local` | Display only. Hark sends no mail. |
| `-display-name` | the username | Name shown in the interface. |

When input is piped, the command reads the password from **standard input**.
Otherwise it uses `HARK_ADMIN_PASSWORD`. Standard input keeps the password out
of shell history and process arguments. Passwords must be 12–256 characters;
there are no composition rules.

Running it a second time fails: the account already exists.

Under Compose the same command is:

```sh
printf '%s' 'correct horse battery staple' | docker compose run --rm -T harkd create-user -username admin
```

### Recovering a lost password

There is no password-reset flow — no mail is ever sent. The API can only change
a password when the current one is known, so the remaining path is the server
itself:

```sh
printf '%s' 'a new passphrase' | harkd set-password -username admin
```

This signs out every session on the account. API tokens are unaffected; revoke
them from the dashboard if the old password was compromised.

---

## Layout

```
cmd/harkd/            The server binary and the account CLI: wiring, signals,
                      graceful shutdown.
internal/auth/        Credentials: password hashing, sessions, API tokens, the
                      device authorization flow. Contains no HTTP handlers.
internal/callbacks/   Delivers interaction answers to webhook callback URLs.
internal/config/      Environment parsing and validation. No global state.
internal/db/          pgx pool, migration runner, and the typed store: one file
                      per domain, plus keyset pagination and error helpers.
internal/db/migrations/   Ordered .sql files, compiled into the binary.
internal/dashboard/   The embedded admin UI and the /docs page: html/template,
                      two stylesheets, no build step.
internal/httpapi/     Route table, middleware chain, JSON and error responses.
internal/id/          UUIDv7 generation and validation.
docs/                 The Markdown, OpenAPI and llms.txt contracts, and the
                      embed directive that compiles them into the binary.
```

The main dependencies are `jackc/pgx/v5` for PostgreSQL,
`golang.org/x/crypto` for Argon2id, `golang.org/x/text` for password
normalization, and `yuin/goldmark` for rendering the API documentation. Routing
uses the standard library's `net/http` package. Hark does not use a web framework
or ORM.

---

## Credentials

See [`docs/api.md`](docs/api.md#authentication) for the full authentication
contract. In summary:

* **Passwords** are Argon2id (64 MiB, three passes, four lanes — RFC 9106's
  second recommended configuration), stored as PHC strings that carry their own
  parameters. Raising the cost later re-hashes each password on its next
  successful sign-in rather than requiring a reset. Passwords are normalized
  per RFC 8265 before hashing, so the same passphrase typed on a Mac and on iOS
  produces the same hash.
* **Sessions** are 256-bit random tokens, stored only as SHA-256 digests. The
  same token works as an `HttpOnly; Secure; SameSite=Lax` cookie (named
  `__Host-hark_session` over HTTPS) or as `Authorization: Bearer`. They expire
  30 days after last use, sliding forward at most once an hour, and can never
  outlive 180 days from creation.
* **API tokens** are `hark_` plus 43 base62 characters, stored as digests and
  shown exactly once. They cannot create credentials or increase their own
  permissions; token management and device approval require a session.
* **Device pairing** follows the OAuth 2.0 device authorization grant (RFC 8628).
  It lets a CLI obtain a scoped token with the owner's approval without handling
  the owner's password.
* Every digest is domain-separated by credential kind, so a value read out of
  one table cannot be replayed against another.

---

## Configuration

Every setting comes from the environment. All Hark-specific variables use the
`HARK_` prefix; `DATABASE_URL` keeps its conventional name.

Hark reports all detected configuration errors before exiting with a non-zero
status.

### Required

| Variable | Notes |
| --- | --- |
| `DATABASE_URL` | PostgreSQL connection URL, `postgres://` or `postgresql://`. |
| `HARK_SECRET_KEY` | Root secret for every derived encryption and MAC key. **At least 32 characters.** There is no default: a deployment cannot accidentally run on a well-known key. Rotating it invalidates every stored ciphertext and every outstanding credential. |

### Server

| Variable | Default | Notes |
| --- | --- | --- |
| `HARK_ENV` | `development` | `development` or `production`. Affects logging defaults only; never API behaviour. |
| `HARK_LISTEN_ADDR` | `:8080` | Go listen address, e.g. `127.0.0.1:8080`. |
| `HARK_PUBLIC_URL` | `http://localhost:8080` | Public origin. Composed into webhook URLs, device-pairing approval links, and push payload links, and it decides the session cookie's name and `Secure` flag and which origin may make cookie-authenticated writes. **Set it to the real `https://` origin in production.** Trailing slashes are stripped. |
| `HARK_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `HARK_LOG_FORMAT` | `text` (`json` in production) | `text` or `json`. |
| `HARK_SHUTDOWN_TIMEOUT` | `20s` | Grace period for in-flight requests. |
| `HARK_MAX_REQUEST_BYTES` | `65536` | Request body cap; larger bodies get `413`. |

### Account

Hark is single-user. The account is seeded at boot when the user table is
empty; there is no sign-up endpoint.

| Variable | Default | Notes |
| --- | --- | --- |
| `HARK_ADMIN_USERNAME` | `admin` | 3–30 characters of letters, digits, `_`, `.`. |
| `HARK_ADMIN_PASSWORD` | *(unset)* | 12–256 characters. **Without it no account is created and sign-in is unavailable** — the server warns at boot and keeps running. Also supplies the password to `harkd create-user` and `harkd set-password` when no password is piped into them. |
| `HARK_ADMIN_EMAIL` | `<username>@hark.local` | Internal only; no mail is ever sent. |

### Database pool

| Variable | Default | Notes |
| --- | --- | --- |
| `HARK_DB_MAX_CONNS` | `10` | Maximum pooled connections. |
| `HARK_DB_MIN_CONNS` | `0` | Minimum idle connections to keep warm. |
| `HARK_DB_CONNECT_TIMEOUT` | `10s` | Also bounds the boot-time reachability check. |
| `HARK_DB_MAX_CONN_LIFETIME` | `1h` | |
| `HARK_DB_MAX_CONN_IDLE_TIME` | `30m` | |
| `HARK_DB_AUTO_MIGRATE` | `true` | Set `false` to start without applying pending migrations. |

### Apple Push Notification service

Set all three APNs credentials or leave all three unset. Without them, Hark runs
but records every send as `ProviderNotConfigured`. A partial or invalid APNs
configuration prevents startup.

| Variable | Default | Notes |
| --- | --- | --- |
| `HARK_APNS_KEY_ID` | *(unset)* | The 10-character key id of the `.p8` auth key. |
| `HARK_APNS_TEAM_ID` | *(unset)* | Apple Developer team id. |
| `HARK_APNS_PRIVATE_KEY` | *(unset)* | The ES256 key: PEM text, PEM whose newlines are escaped as `\n`, or base64 of either. Validated at boot. |
| `HARK_APNS_PRIVATE_KEY_FILE` | *(unset)* | Alternative to the above: a path to the `.p8` file. Setting both is an error. |
| `HARK_APNS_BUNDLE_ID` | `dev.abdeen.hark` | Base of the APNs topic. Live Activities are sent to `<bundle id>.push-type.liveactivity`. |
| `HARK_APNS_ENVIRONMENT` | `sandbox` | `sandbox` or `production`. One host per process: a Live Activity token issued for the other environment is refused rather than routed. |
| `HARK_APNS_ATTEMPT_RETENTION_DAYS` | `30` | How many days of APNs delivery-attempt rows to keep. These diagnostic rows are not used while serving requests. A background worker deletes older rows at boot and then daily. 1–3650; notification and event history are untouched. |

The connection is HTTP/2 with provider-token (JWT) authentication; one token is
cached process-wide and refreshed every 50 minutes. Hark does not retry failed
pushes, with one exception that cannot duplicate a delivery: a `403
ExpiredProviderToken` means Apple rejected the JWT rather than the notification,
so Hark refreshes the token and sends that push again once. See
[Push payloads](docs/api.md#push-payloads) in the API contract for what the
phone receives and what a client can rely on.

### Limits

| Variable | Default | Notes |
| --- | --- | --- |
| `HARK_TRUSTED_CLIENT_IP_HEADER` | *(unset)* | Header set by a trusted reverse proxy with the real client address. **Leave unset when there is no proxy.** Without this setting, sign-in and device-pairing limits apply globally instead of per client. |
| `HARK_RATE_LIMIT_REQUESTER_PER_MINUTE` | `300` | Per webhook service or API token. |
| `HARK_RATE_LIMIT_ACCOUNT_PER_MINUTE` | `1500` | Across the whole account. |

Secrets never reach the logs: the startup line that echoes the configuration
redacts the database password and omits the secret key and admin password
entirely.

---

## Database migrations

Migrations are plain SQL files in `internal/db/migrations`, named
`<version>_<snake_case_name>.sql` — for example `0001_initial_schema.sql`. They
are embedded into the binary with `embed.FS` and applied in ascending version
order at startup, each inside its own transaction together with its ledger row.

There is no third-party migration framework and no `down` migrations: rolling
back means writing a new forward migration.

* The `schema_migrations` ledger is created by the runner, so no migration has
  to define it.
* A PostgreSQL advisory lock is held for the run, so concurrently starting
  replicas do not race.
* Every applied file's SHA-256 is recorded. **Editing a migration that has
  already been applied is fatal at startup** — write a new one instead.

To add one, drop a new numbered file into `internal/db/migrations` and restart.

---

## Development

```sh
gofmt -l .          # no output when files are formatted
go build ./...
go vet ./...
go test ./...
```

Tests that need PostgreSQL run only when `TEST_DATABASE_URL` points to a test
database. **These tests drop and recreate schemas. Never use a database that
contains data you need.**

```sh
TEST_DATABASE_URL='postgres://hark:hark@localhost:5432/hark_test' go test ./...
```

`HARK_TEST_DATABASE_URL` is accepted as an alias. If neither variable is set,
database-backed tests are skipped and the rest of the suite still runs.

Each package that needs PostgreSQL uses its own schema:
`internal/db` in `public`, `internal/auth` in `hark_auth_test`, `internal/httpapi`
in `hark_api_test`, and `internal/callbacks` in `hark_callbacks_test`. This keeps
parallel package tests isolated. Add a new schema name for any new package that
uses the database.

Every endpoint added or changed must be documented in [`docs/api.md`](docs/api.md)
in the same change.

The dashboard's two third-party assets, htmx and idiomorph, are vendored — no
npm, no build step. Each one's exact version and digests are pinned in
`internal/dashboard/assets/<name>.provenance.json`;
`sh scripts/vendor-assets.sh --verify` checks the checked-in files against
those pins offline, and `--refresh` re-fetches the pinned tarballs. See
[`third_party/htmx/README.md`](third_party/htmx/README.md) and
[`third_party/idiomorph/README.md`](third_party/idiomorph/README.md).

---

## License

[MIT](LICENSE).
