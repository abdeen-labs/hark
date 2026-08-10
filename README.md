# Hark

Hark turns webhooks and agent API calls into iOS push notifications, Live
Activities, and approval prompts you can answer from the Lock Screen. The whole
server is one Go binary over PostgreSQL: the API, the admin dashboard and the
published contract all come out of a single process, with no build step and no
assets to deploy alongside it.

This repository is a private, single-user deployment: one account, seeded at
boot, with no sign-up surface, no billing, and no analytics of any kind.

* **API contract:** [`docs/api.md`](docs/api.md) — the document iOS and CLI
  clients are built from. It is compiled into the binary and served as a
  searchable page at [`/docs`](#the-published-contract), which needs no
  credential. Machines get the same contract as raw Markdown at `/docs.md`, as
  OpenAPI 3.1 at [`/openapi.json`](docs/openapi.json), and through the compact
  [`/llms.txt`](docs/llms.txt) discovery index. It also documents the embedded
  [dashboard](docs/api.md#dashboard).

---

## Requirements

* Go 1.26
* PostgreSQL 17

Or just Docker, via Compose.

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

The admin UI is compiled into the binary and served on the site root: open
<http://localhost:8080/> and sign in with the account below. It is a single-user
admin surface — the account's devices, its API tokens, what has reached the
account recently, a form that sends a test push, and the screen at
`/cli/authorize` where a command-line client is let in. Everything it does, the
API does too; see [`docs/api.md`](docs/api.md#dashboard).

### The published contract

<http://localhost:8080/docs> renders [`docs/api.md`](docs/api.md) with search,
an endpoint outline, and copyable examples. The machine-facing forms are:

| URL | Format | Best for |
| --- | --- | --- |
| <http://localhost:8080/docs.md> | Markdown | Agents and source ingestion |
| <http://localhost:8080/openapi.json> | OpenAPI 3.1 JSON | Client generation, discovery, validation |
| <http://localhost:8080/llms.txt> | Plain text | Agent discovery |

All four need no credential at all — the routes are mounted outside the
server's authentication middleware, so nothing is read off the request. The
files are embedded at build time and the HTML is rendered once at startup, so
every representation and the binary serving it always ship together.

### Locally, against your own PostgreSQL

```sh
export DATABASE_URL='postgres://hark:hark@localhost:5432/hark?sslmode=disable'
export HARK_SECRET_KEY="$(openssl rand -base64 48)"
export HARK_ADMIN_PASSWORD='choose-something-long'

go run ./cmd/harkd
```

Migrations run automatically at startup. A database that is unreachable, or a
configuration that does not validate, fails the process immediately with a
single diagnostic line on stderr — the server never starts half-configured.

Shutdown is graceful: `SIGINT` or `SIGTERM` stops accepting connections, lets
in-flight requests finish within `HARK_SHUTDOWN_TIMEOUT`, then closes the pool.
The one background worker — the outbound answer callback, described in
[`docs/api.md`](docs/api.md#the-answer-callback) — stops on the same signal.
Whatever it had not sent keeps its row and goes out after the next boot.

---

## The account

Hark is single-user. **There is no sign-up endpoint**, and there never will be:
the guard lives in the `INSERT` itself, so the second account cannot be created
by any code path, racing process, or future bug in a route table.

The account comes into being one of two ways.

### Seeded at boot

Set `HARK_ADMIN_PASSWORD` (and optionally `HARK_ADMIN_USERNAME`,
`HARK_ADMIN_EMAIL`) and start the server. If the user table is empty, the
account is created; if it is not, nothing happens. This is what makes
`docker compose up` work out of the box. Seeding never fails the boot: a server
that cannot seed still serves, it just has nobody who can sign in, and it says
so in the log.

### Created from the command line

```sh
printf '%s' 'correct horse battery staple' | harkd create-user -username admin
```

| Flag | Default | Notes |
| --- | --- | --- |
| `-username` | `HARK_ADMIN_USERNAME`, else `admin` | 3–30 characters of letters, digits, `_`, `.`. Lowercased. |
| `-email` | `HARK_ADMIN_EMAIL`, else `<username>@hark.local` | Display only. Hark sends no mail. |
| `-display-name` | the username | The human-facing name. |

The password is read from **standard input** when something is piped in, and
from `HARK_ADMIN_PASSWORD` otherwise. Piping wins because it is the more
specific instruction, and because it keeps the password out of your shell
history and out of an `argv` any process on the box can read. It must be 12–256
characters; there are no composition rules.

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
                      device grant. Knows nothing about HTTP.
internal/callbacks/   The one piece of background work: posting a question's
                      answer back to the webhook caller that asked to be told.
internal/config/      Environment parsing and validation. No global state.
internal/db/          pgx pool, migration runner, and the typed store: one file
                      per domain, plus keyset pagination and error helpers.
internal/db/migrations/   Ordered .sql files, compiled into the binary.
internal/dashboard/   The embedded admin UI and the /docs page: html/template,
                      two stylesheets, no build step.
internal/httpapi/     Route table, middleware chain, JSON and error envelope.
internal/id/          UUIDv7 generation and validation.
docs/                 The Markdown, OpenAPI and llms.txt contracts, and the
                      embed directive that compiles them into the binary.
```

Dependencies are deliberately few: the standard library, `jackc/pgx/v5` for
PostgreSQL, `golang.org/x/crypto` for Argon2id, `golang.org/x/text` for the
Unicode normalization a password goes through before it is hashed, and
`yuin/goldmark` to render the contract page. Routing is `net/http`'s `ServeMux`
with method patterns — there is no web framework and no ORM.

---

## Credentials

The full contract is in [`docs/api.md`](docs/api.md#authentication); the design
in one page:

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
  shown exactly once. They carry scopes, and they can never mint or widen a
  credential — token management and device approval require a session.
* **Device pairing** is an OAuth 2.0 device grant (RFC 8628) in this API's own
  dress, so a CLI can obtain a scoped token with the owner's approval without
  ever touching their password.
* Every digest is domain-separated by credential kind, so a value read out of
  one table cannot be replayed against another.

---

## Configuration

Every setting comes from the environment. All Hark-specific variables use the
`HARK_` prefix; `DATABASE_URL` keeps its conventional name.

Invalid configuration is reported in full — every problem at once, not one per
restart — and the process exits non-zero.

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

Hark is single-user. The one account is seeded at boot when the user table is
empty; there is no sign-up endpoint.

| Variable | Default | Notes |
| --- | --- | --- |
| `HARK_ADMIN_USERNAME` | `admin` | 3–30 characters of letters, digits, `_`, `.`. |
| `HARK_ADMIN_PASSWORD` | *(unset)* | 12–256 characters. **Without it no account is created and nobody can sign in** — the server warns at boot and keeps running. Also supplies the password to `harkd create-user` and `harkd set-password` when nothing is piped into them. |
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

Set all three credentials or none. With none, the server runs normally and
records every send as a provider-not-configured failure. Setting only some is a
configuration error, and so is a key that is present but unusable: a deployment
that plainly meant pushes to work does not start pretending otherwise.

| Variable | Default | Notes |
| --- | --- | --- |
| `HARK_APNS_KEY_ID` | *(unset)* | The 10-character key id of the `.p8` auth key. |
| `HARK_APNS_TEAM_ID` | *(unset)* | Apple Developer team id. |
| `HARK_APNS_PRIVATE_KEY` | *(unset)* | The ES256 key: PEM text, PEM whose newlines are escaped as `\n`, or base64 of either. Validated at boot. |
| `HARK_APNS_PRIVATE_KEY_FILE` | *(unset)* | Alternative to the above: a path to the `.p8` file. Setting both is an error. |
| `HARK_APNS_BUNDLE_ID` | `dev.abdeen.hark` | Base of the APNs topic. Live Activities are sent to `<bundle id>.push-type.liveactivity`. |
| `HARK_APNS_ENVIRONMENT` | `sandbox` | `sandbox` or `production`. One host per process: a Live Activity token minted in the other environment is refused rather than routed. |

The connection is HTTP/2 with provider-token (JWT) authentication; one token is
cached process-wide and re-minted every 50 minutes. Nothing is retried, with one
exception that cannot duplicate a delivery: a `403 ExpiredProviderToken` means
Apple rejected the JWT rather than the notification, so the token is re-minted
and that push is sent again exactly once. See
[Push payloads](docs/api.md#push-payloads) in the API contract for what the
phone receives and what a client can rely on.

### Limits

| Variable | Default | Notes |
| --- | --- | --- |
| `HARK_TRUSTED_CLIENT_IP_HEADER` | *(unset)* | Name of the single header a trusted reverse proxy overwrites with the real client address. **Leave unset behind no proxy:** per-client rate limiting is then disabled entirely rather than keyed off a value the client can forge. Behind a controlled edge, set it — without it, sign-in and device-pairing limits are global, so one abusive caller shares a bucket with you. |
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
gofmt -l .          # must print nothing
go build ./...
go vet ./...
go test ./...
```

Tests that need a live PostgreSQL skip unless `TEST_DATABASE_URL` points at a
**scratch** database. They drop and recreate the schemas they use, so never aim
it at anything worth keeping:

```sh
TEST_DATABASE_URL='postgres://hark:hark@localhost:5432/hark_test' go test ./...
```

`HARK_TEST_DATABASE_URL` is accepted as an alias. With neither set, the
database-backed tests skip and the rest of the suite still runs, so
`go test ./...` is always a valid thing to type.

Packages that need PostgreSQL each work inside a schema of their own —
`internal/db` in `public`, `internal/auth` in `hark_auth_test`, `internal/httpapi`
in `hark_api_test`, `internal/callbacks` in `hark_callbacks_test`. `go test ./...`
runs packages in parallel, so a package that shared a schema would keep pulling
the tables out from under another. A new package that needs a database claims a
new name.

Every endpoint added or changed must be documented in [`docs/api.md`](docs/api.md)
in the same change.
