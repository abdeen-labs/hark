# Hark HTTP API

The living contract for the Hark backend. Every endpoint the server exposes is
documented here, in the same change that adds or modifies it. Clients — the iOS
app, the web dashboard, `harkctl` — are built from this document.

> **Status.** Every endpoint listed under [Endpoints](#endpoints) exists;
> anything not listed here returns `404 not_found`. Pushes reach a phone as
> soon as APNs credentials are configured — what lands there is documented
> under [Push payloads](#push-payloads). Without credentials the server still
> runs and reports every send honestly: `accepted` counts stay `0` and the
> delivery log says `ProviderNotConfigured`.

---

## Start here

Hark has two credentials because it separates people from software: a
**session** represents the account owner, while an **API token** represents a
CLI, agent or automation with an explicit set of scopes. Sign in once to mint a
token; use that token for normal API work.

### Send your first notification

Set the origin of your deployment, then exchange the owner's password for a
session:

```sh
export HARK_URL='http://localhost:8080'

curl --fail-with-body "$HARK_URL/v1/auth/login" \
  --header 'Content-Type: application/json' \
  --data '{"username":"admin","password":"correct horse battery staple"}'
```

Copy the response's `token` value. Use that session to create a narrowly scoped
API token:

```sh
export HARK_SESSION='harksess_…'

curl --fail-with-body "$HARK_URL/v1/tokens" \
  --header "Authorization: Bearer $HARK_SESSION" \
  --header 'Content-Type: application/json' \
  --data '{"name":"quickstart","scopes":["notifications:send"]}'
```

The `secret` in this response is shown only once. Save it and send:

```sh
export HARK_TOKEN='hark_…'

curl --fail-with-body "$HARK_URL/v1/notifications" \
  --header "Authorization: Bearer $HARK_TOKEN" \
  --header 'Content-Type: application/json' \
  --header 'Idempotency-Key: first-notification' \
  --data '{"title":"Hark is ready","body":"Your first API notification arrived."}'
```

A successful request is `201 Created`. Read `notification.accepted_count` and
`message` in the response rather than treating HTTP success as proof that APNs
accepted a push: a server with no registered phone or no APNs credentials still
records the request honestly.

### Pick the right entry point

| You are building | Start with | Credential |
| --- | --- | --- |
| An agent, CLI or CI job | [`POST /v1/notifications`](#post-v1notifications), then [interactions](#interactions) or [Live Activities](#live-activities) | Scoped API token |
| A service that can only call a URL | [Create a service](#post-v1services), then call its [`/v1/hooks/{token}`](#post-v1hookstoken) URL | Webhook token in the URL |
| An iOS client | [Session authentication](#session), then [register the device](#post-v1devices) | Session |
| A headless client that needs owner approval | [Device authorization](#post-v1authdevicecode) | Device grant, then API token |

For generated clients and validators use the [OpenAPI 3.1 document](/openapi.json).
For agents and other text-oriented tools use the [raw Markdown](/docs.md) or
the discovery index at [`/llms.txt`](/llms.txt). All formats ship in the same
binary as this page.

---

## Conventions

### Base URL and versioning

All API endpoints live under the `/v1` prefix:

```
https://hark.example.com/v1/…
```

Three things sit outside `/v1` and are not versioned: the readiness probe
[`/healthz`](#get-healthz); this document, published as a page at
[`/docs`](#get-docs); and the [dashboard](#dashboard) — the admin UI embedded
in the server, mounted on `/`, `/dashboard` and `/cli/authorize`. The last two
serve HTML to a browser rather than JSON to a client; they are documented here
so nobody building a client mistakes those paths for API surface.

The version is bumped only for a breaking change to the whole surface.
Individual endpoints are added and extended in place; clients must ignore JSON
fields they do not recognise, which makes adding a field a compatible change.

### Requests

* Request bodies are JSON. Send `Content-Type: application/json` — a body sent
  with any other media type is rejected with `415 unsupported_media_type`.
* Request bodies are capped at **64 KiB**. A larger body is rejected with
  `413 payload_too_large` before it is read.
* **Unknown request fields are rejected** with `400 bad_request`. A misspelled
  field name is reported rather than silently ignored, so plan for request
  fields to be added over time — send only what an endpoint documents.
* Paths are case-sensitive and have no trailing slash. `/v1/services/` does not
  match `/v1/services`.
* Reaching an existing path with an unsupported method returns
  `405 method_not_allowed` along with an `Allow` header.

### Responses

* Every response body is JSON, sent as `Content-Type: application/json`.
* **JSON field names are `snake_case`**, in both requests and responses.
* Fields that can be absent are sent as explicit `null` rather than omitted,
  unless an endpoint says otherwise.

### Identifiers

Every entity id is a **UUIDv7** string in canonical lowercase hyphenated form:

```
0198f3a1-2b4c-7d8e-9f01-23456789abcd
```

UUIDv7 embeds a millisecond timestamp in its high bits, so ids sort by creation
time. Treat them as opaque: never parse an id to recover a timestamp, and never
assume ids are dense or sequential.

### Timestamps

Every timestamp is an **RFC 3339 string in UTC**, with millisecond precision and
a literal `Z`:

```
2026-08-09T12:34:56.789Z
```

Durations and intervals in request bodies are integer **seconds** unless the
field name says otherwise.

### Pagination

Every list endpoint is paged the same way, with an opaque cursor rather than a
page number. The lists are append-mostly: an offset would silently repeat or
skip rows whenever something arrives between two requests.

**Request** — both parameters are optional:

| Parameter | Default | Notes |
| --- | --- | --- |
| `limit` | `20` | Maximum items to return, clamped to `1…100`. |
| `cursor` | *(absent)* | The `next_cursor` of the previous page. Omit for the first page. |

**Response** — every paged endpoint returns the same envelope, with the items
under a name that suits the endpoint:

```json
{
  "events": [ … ],
  "next_cursor": "MTc1NDc0MzQ5Njc4OS4wMTk4ZjNhMS0yYjRjLTdkOGUtOWYwMS0yMzQ1Njc4OWFiY2Q"
}
```

* `next_cursor` is `null` on the last page. Its presence is the only reliable
  "is there more" signal — a full page can still be the last one.
* Cursors are **opaque**: do not parse, construct, or persist them beyond the
  next request. A cursor from one endpoint is meaningless to another.
* Lists are ordered newest first, and paging is stable: an item created while a
  client is paging appears on a subsequent fresh read, never twice inside one
  walk.
* A cursor that did not come from this API is rejected with `422
  validation_failed` naming the `cursor` field.

Totals are deliberately not returned. Counting the whole history on every page
costs more than it is worth for a client that only ever renders a window of it.

### Shared vocabulary

These values appear across many endpoints and are always the exact lowercase
strings below. They are what the database stores, so an unknown member is
refused rather than coerced.

| Set | Members |
| --- | --- |
| Priority | `normal`, `time_sensitive`, `critical` |
| Interaction kind | `approval`, `yes_no`, `reply` |
| Interaction status | `pending`, `approved`, `denied`, `yes`, `no`, `replied`, `canceled`, `expired` |
| Interaction presentation | `notification`, `live_activity` |
| Notification delivery status | `processing`, `no_devices`, `accepted`, `partial`, `failed` |
| Live Activity status | `starting`, `active`, `partial` (live) · `failed`, `ended`, `expired` (terminal) |
| Live Activity operation | `start`, `update`, `end` |
| APNs environment | `sandbox`, `production` |
| History feed kind | `notification`, `response`, `live_activity` |
| API token scope | `activities:read`, `activities:write`, `devices:read`, `events:read`, `interactions:create`, `interactions:read`, `notifications:send`, `services:read`, `services:write` |

An interaction's `choices` follow from its kind: `approval` →
`["approve","deny"]`, `yes_no` → `["yes","no"]`, `reply` → `["reply"]`.

Every non-`pending` interaction status and every terminal Live Activity status
is final: nothing transitions out of them.

### Request correlation

Every response carries an `X-Request-Id` header. A client may supply its own on
the request — up to 128 characters of `[A-Za-z0-9._-]` — and it will be echoed
back and used in the server's logs; anything else is replaced with a generated
id. Quote this value when reporting a problem.

### Errors

Every error, from every endpoint, uses one envelope:

```json
{
  "error": {
    "code": "not_found",
    "message": "No service matches that id."
  }
}
```

* `code` is a stable, machine-readable string. **Branch on this.**
* `message` is human-readable English intended for logs and for display to the
  operator. It is not stable and may change without notice.

Validation failures add a `fields` array naming each offending input:

```json
{
  "error": {
    "code": "validation_failed",
    "message": "The request body is invalid.",
    "fields": [
      { "field": "title", "message": "must be 1-80 characters" },
      { "field": "image_url", "message": "must be a public HTTPS URL" }
    ]
  }
}
```

#### Error codes

| HTTP | `code` | Meaning |
| --- | --- | --- |
| 400 | `bad_request` | The request is malformed — unparseable JSON, a missing path parameter, a header the endpoint cannot honour. |
| 401 | `unauthorized` | No credential was presented, or it is unknown, expired, or revoked. |
| 403 | `session_required` | The endpoint is the account owner's alone — it manages credentials, or it answers a question — so an API token may not call it however broad its scopes. |
| 403 | `api_token_required` | The endpoint attributes what it creates to a credential, so a session may not call it. See [Who may send](#who-may-send). |
| 403 | `insufficient_scope` | A valid API token is missing a scope the endpoint declares. The message names every scope the endpoint requires. |
| 403 | `origin_not_allowed` | A cookie-authenticated state-changing request arrived from a foreign origin. |
| 404 | `not_found` | No route matches, or the addressed resource does not exist or does not belong to the caller. Credential-authenticated endpoints also answer `404` on a failed credential check, so a caller cannot probe for resource existence. |
| 405 | `method_not_allowed` | The path exists but not for this method. The `Allow` header lists the supported ones. |
| 409 | `conflict` | The request collides with current state: a duplicate idempotency key used with a different payload, an already-answered question, an already-finished Live Activity. |
| 409 | `token_limit_reached` | The account already holds the maximum number of active API tokens. |
| 409 | `activity_conflict` | A Live Activity already holds the device, or the key, this start asked for. Retry with `replace: true`, or with another key. |
| 409 | `sequence_conflict` | The Live Activity moved on since the sequence the caller read. Re-read it and reapply. |
| 409 | `action_digest_mismatch` | The answer refers to a different version of the question than the one stored — the phone is showing something stale. |
| 413 | `payload_too_large` | The request body exceeds 64 KiB. |
| 415 | `unsupported_media_type` | A request body arrived without `Content-Type: application/json`. |
| 422 | `validation_failed` | The body parsed but failed validation. See `fields`. |
| 429 | `rate_limited` | A rate limit was exceeded. `Retry-After` gives the wait in seconds. |
| 500 | `internal_error` | An unhandled server error. The message never contains internal detail; correlate with `X-Request-Id` in the server log. |
| 503 | `service_unavailable` | A dependency the request needs — normally PostgreSQL — is unreachable. |

The device grant adds five more codes — `authorization_pending`, `slow_down`,
`access_denied`, `expired_token` and `invalid_grant` — described with
[`POST /v1/auth/device/token`](#post-v1authdevicetoken).

---

## Authentication

Hark serves exactly **one account**. There is no sign-up endpoint: the account
is provisioned from the command line (`harkd create-user`, see the README) or
seeded at boot from the environment. Every request is authorized by one of two
credentials.

### Session

A session is the account owner signed in with their password. It is issued by
[`POST /v1/auth/login`](#post-v1authlogin) and travels either way:

**As a cookie**, for the dashboard. The server sets it on sign-in and the
browser replays it:

```
Set-Cookie: __Host-hark_session=harksess_…; Path=/; Max-Age=2592000; HttpOnly; Secure; SameSite=Lax
```

The name is `__Host-hark_session` when the server's public URL is `https`, and
`hark_session` on plain HTTP (localhost only — a browser rejects the `__Host-`
prefix without `Secure`). Do not parse the value; it is opaque.

**As a bearer token**, for native and command-line clients:

```
Authorization: Bearer harksess_V3kQ2mZ8bR1tXyLp0aNfCd7eJhSu4WgO7xY2bWv
```

It is the same token either way, returned in the sign-in response body as
`token`. A browser should ignore that field and rely on the cookie.

**Lifetime.** A session expires 30 days after it was last used. Any request
that arrives more than an hour after the previous refresh slides the expiry
forward and, for cookie callers, re-issues the cookie with the new lifetime —
so a client used at least monthly never has to sign in again. A session can
never live longer than 180 days from when it was created, however often it is
used.

### API token

An API token is an agent — a CLI, a script, a CI job — acting for the account
under a fixed set of scopes:

```
Authorization: Bearer hark_c2xLm9JbR1tXyLp0aNfCd7eJhSu4WgO7xY2bWvK
```

Tokens are created by the account owner ([`POST /v1/tokens`](#post-v1tokens))
or issued by the [device grant](#post-v1authdevicecode). The plaintext is shown
exactly once, at creation, and is never recoverable — the server stores only a
digest.

**An API token can never mint or modify a credential.** Token management and
device-grant approval require a session, so a leaked token can act within its
scopes but cannot widen them, cannot create a successor, and cannot approve a
pairing request. The one thing it can do to itself is retire itself, through
[`POST /v1/auth/logout`](#post-v1authlogout).

#### Scopes

A token carries a subset of these nine strings. Anything else is rejected.

```
activities:read     activities:write    devices:read
events:read         interactions:create interactions:read
notifications:send  services:read       services:write
```

Scope lists are stored deduplicated and sorted, so a request for
`["services:read","interactions:read","services:read"]` reads back as
`["interactions:read","services:read"]`.

An endpoint that requires scopes answers `403 insufficient_scope` when the
token lacks any of them, naming all of the endpoint's required scopes in the
message. Scopes constrain API tokens only: a session is the account owner in
person and satisfies every scope check.

### How failures are reported

| Situation | Response |
| --- | --- |
| No credential presented on an endpoint that needs one | `401 unauthorized` with `WWW-Authenticate: Bearer realm="hark"` |
| An `Authorization` header that is malformed, or names an unknown, expired, or revoked credential | `401 unauthorized` |
| A **cookie** that is unknown or expired | Treated as no credential at all, and expired with a `Set-Cookie`. A browser holding a dead session can still reach public endpoints and sign in again. |
| A credential could not be checked (the database is unreachable) | `503 service_unavailable` — never `401`, so a client is not told to re-authenticate when nothing is wrong with its credential |

### Cross-origin requests

A state-changing request (anything other than `GET`, `HEAD`, `OPTIONS`)
authenticated by the **cookie** must carry no `Origin` header or one that
matches the server's public origin exactly; otherwise it is refused with
`403 origin_not_allowed`. This is the CSRF gate, and together with
`SameSite=Lax` it is why no CSRF token is needed.

Requests authenticated by an `Authorization` header skip the check entirely —
nothing is ambient about a header a client had to set — so native and
command-line clients may send any `Origin` they like, or none.

### Rate limits

The endpoints an anonymous caller can reach are capped per minute. Each has a
per-client ceiling and a process-wide one twenty times larger:

| Endpoint | Per client | Process-wide |
| --- | --- | --- |
| `POST /v1/auth/login` | 10 | 200 |
| `POST /v1/auth/device/code` | 20 | 400 |
| `POST /v1/auth/device/token` | 120 | 2400 |

Exceeding either gives `429 rate_limited` with `Retry-After` in seconds.

Per-client buckets exist only when the server is configured with the name of a
header its reverse proxy overwrites with the real client address
(`HARK_TRUSTED_CLIENT_IP_HEADER`). Without it there is no per-client bucket at
all, because trusting a client-supplied forwarding header would let any caller
mint itself a fresh bucket per request. Deployments behind a controlled edge
should set it.

---

## Delivery

Everything that reaches a phone — a notification, a question, a Live Activity —
goes through the same handful of rules. They are described once here rather than
repeated on every endpoint.

### Who may send

A **session** is the account owner in person. It can read everything, manage
credentials and devices, and answer questions.

An **API token** is an agent. It can do what its scopes allow, and it is the
only credential that may *send*: every notification, question and Live Activity
records which credential asked for it, and a person is not a requester. So
[`POST /v1/notifications`](#post-v1notifications),
[`POST /v1/interactions`](#post-v1interactions) and the Live Activity writes
answer `403 api_token_required` to a session. Mint a token for whatever is
doing the sending.

A **webhook token** is a service: the third requester kind, and the one for
systems that can only be handed a URL. It sends through
[`/v1/hooks/{token}`](#post-v1hookstoken) and drives its own Live Activities
through the same envelopes the token surface uses.

### Choosing devices

Every send goes to all of the account's reachable devices unless it names
`device_ids`. A named device that is not registered to the account is
`422 validation_failed` rather than a silent no-op — the caller believes it
addressed something. Devices that are registered but cannot receive *this* kind
of send are dropped quietly:

| Send | Needs |
| --- | --- |
| Notification | an active device |
| Question, as a notification | `interaction_schema_version` |
| Live Activity | a push-to-start token, its environment, and `live_activity_schema_version` |
| Question, on the Lock Screen | the above plus `live_activity_interaction_version` |

An account with no reachable device is a normal state, not an error: the record
is created, `accepted` is `0`, and `message` says why.

### What `accepted` means

`accepted` — and the `accepted_count` / `delivered_count` fields — count the
messages **APNs accepted from this server**. That is not proof that a phone
displayed anything, and no field in this API is. Treat it as "the push was
handed over", nothing more.

When a send fails, the provider's own error text is kept in the account's
delivery log ([`GET /v1/events`](#get-v1events)) and deliberately not returned
to the caller: an APNs error can embed a device token, and the holder of an API
token or a webhook URL is not necessarily the account owner. The response
carries a `message` naming the shape of the failure instead.

A device whose token APNs reports as permanently invalid is marked inactive.
Its row is kept, so history keeps resolving, and registering it again revives
it.

### Idempotency

These endpoints honour an `Idempotency-Key` request header:

* [`POST /v1/notifications`](#post-v1notifications)
* [`POST /v1/interactions`](#post-v1interactions)
* [`POST /v1/activities`](#post-v1activities), [`PATCH`](#patch-v1activitiesidentifier) and [`POST …/end`](#post-v1activitiesidentifierend)
* [`POST /v1/hooks/{token}`](#post-v1hookstoken) and the webhook Live Activity routes

The key is 1–200 characters and is scoped to the credential that used it. The
record is written **before** anything is sent, so a retry that races the
original replays instead of pushing a second copy.

| Situation | Response |
| --- | --- |
| New key | The normal `201`, with `"replayed": false`. |
| Same key, same body | `200` with the stored outcome and `"replayed": true`. Nothing is sent again. |
| Same key, different body | `409 conflict`. The key is supposed to identify the request. |
| Present but empty, or over 200 characters | `400 bad_request`. |

"Same body" means the same request *as the server understood it*: the
comparison is made after defaults are applied, strings are trimmed and
`device_ids` are sorted, so two spellings of one request are one request.

### Delivery limits

Beyond the anonymous limits above, sending is capped per rolling minute: once
per credential (default 300) and once for the whole account (default 1500).
Both are counted from the records that were written rather than from an
in-memory counter, so a restart hands nobody a fresh allowance. Exceeding
either gives `429 rate_limited` with `Retry-After`.

---

## Endpoints

| Method | Path | Auth |
| --- | --- | --- |
| `GET` | [`/healthz`](#get-healthz) | none |
| `GET`·`POST` | [`/` and `/dashboard/…`](#dashboard) | session cookie (HTML, not JSON) |
| `POST` | [`/v1/auth/login`](#post-v1authlogin) | none |
| `POST` | [`/v1/auth/logout`](#post-v1authlogout) | session or API token |
| `GET` | [`/v1/auth/session`](#get-v1authsession) | session or API token |
| `POST` | [`/v1/auth/password`](#post-v1authpassword) | session |
| `POST` | [`/v1/auth/device/code`](#post-v1authdevicecode) | none |
| `POST` | [`/v1/auth/device/token`](#post-v1authdevicetoken) | device code in body |
| `GET` | [`/v1/auth/device/requests/{user_code}`](#get-v1authdevicerequestsuser_code) | session |
| `POST` | [`/v1/auth/device/requests/{user_code}/approve`](#post-v1authdevicerequestsuser_codeapprove) | session |
| `POST` | [`/v1/auth/device/requests/{user_code}/deny`](#post-v1authdevicerequestsuser_codedeny) | session |
| `GET` | [`/v1/tokens`](#get-v1tokens) | session |
| `POST` | [`/v1/tokens`](#post-v1tokens) | session |
| `DELETE` | [`/v1/tokens/{id}`](#delete-v1tokensid) | session |
| `GET` | [`/v1/services`](#get-v1services) | session · token `services:read` |
| `POST` | [`/v1/services`](#post-v1services) | session · token `services:write` |
| `GET` | [`/v1/services/{id}`](#get-v1servicesid) | session · token `services:read` |
| `PATCH` | [`/v1/services/{id}`](#patch-v1servicesid) | session · token `services:write` |
| `DELETE` | [`/v1/services/{id}`](#delete-v1servicesid) | session · token `services:write` |
| `POST` | [`/v1/services/{id}/webhook-token`](#post-v1servicesidwebhook-token) | session |
| `GET` | [`/v1/devices`](#get-v1devices) | session · token `devices:read` |
| `POST` | [`/v1/devices`](#post-v1devices) | session |
| `GET` | [`/v1/devices/{id}`](#get-v1devicesid) | session · token `devices:read` |
| `DELETE` | [`/v1/devices/{id}`](#delete-v1devicesid) | session |
| `PUT` | [`/v1/devices/{id}/push-to-start-token`](#put-v1devicesidpush-to-start-token) | session |
| `PUT` | [`/v1/devices/{id}/activity-update-token`](#put-v1devicesidactivity-update-token) | session |
| `PUT` | [`/v1/activity-deliveries/{id}/update-token`](#put-v1activity-deliveriesidupdate-token) | capability in body |
| `POST` | [`/v1/notifications`](#post-v1notifications) | token `notifications:send` |
| `POST` | [`/v1/interactions`](#post-v1interactions) | token `interactions:create` + `notifications:send` |
| `GET` | [`/v1/interactions`](#get-v1interactions) | session · token `interactions:read` |
| `GET` | [`/v1/interactions/{id}`](#get-v1interactionsid) | session · token `interactions:read` |
| `POST` | [`/v1/interactions/{id}/response`](#post-v1interactionsidresponse) | session · credential in body |
| `POST` | [`/v1/interactions/{id}/cancel`](#post-v1interactionsidcancel) | session · token `interactions:create` |
| `GET` | [`/v1/activities`](#get-v1activities) | session · token `activities:read` |
| `POST` | [`/v1/activities`](#post-v1activities) | token `activities:write` |
| `GET` | [`/v1/activities/{identifier}`](#get-v1activitiesidentifier) | session · token `activities:read` |
| `PATCH` | [`/v1/activities/{identifier}`](#patch-v1activitiesidentifier) | token `activities:write` |
| `POST` | [`/v1/activities/{identifier}/end`](#post-v1activitiesidentifierend) | token `activities:write` |
| `GET` | [`/v1/events`](#get-v1events) | session · token `events:read` |
| `DELETE` | [`/v1/events/{id}`](#delete-v1eventsid) | session |
| `GET` | [`/v1/history`](#get-v1history) | session |
| `DELETE` | [`/v1/history/{id}`](#delete-v1historyid) | session |
| `POST` | [`/v1/hooks/{token}`](#post-v1hookstoken) | webhook token in path |
| `GET` | [`/v1/hooks/{token}/events/{event_id}`](#get-v1hookstokeneventsevent_id) | webhook token in path |
| `POST` | [`/v1/hooks/{token}/events/{event_id}/cancel`](#post-v1hookstokeneventsevent_idcancel) | webhook token in path |
| `POST` | [`/v1/hooks/{token}/activities`](#the-webhook-live-activity-routes) | webhook token in path |
| `GET` | [`/v1/hooks/{token}/activities/{identifier}`](#the-webhook-live-activity-routes) | webhook token in path |
| `PATCH` | [`/v1/hooks/{token}/activities/{identifier}`](#the-webhook-live-activity-routes) | webhook token in path |
| `POST` | [`/v1/hooks/{token}/activities/{identifier}/end`](#the-webhook-live-activity-routes) | webhook token in path |

Hark makes exactly one request of its own, to an address a caller supplies: [the
answer callback](#the-answer-callback). Nothing else here calls out.

### `GET /healthz`

Readiness probe. No authentication.

The handler checks that the PostgreSQL pool can serve a query, so a `200` means
"this instance can handle traffic" and a `503` means "take it out of rotation".
The database check is bounded at 2 seconds.

The response is never cached (`Cache-Control: no-store`).

**200 OK**

```json
{
  "status": "ok",
  "database": "ok",
  "version": "a1b2c3d4e5f6"
}
```

| Field | Type | Notes |
| --- | --- | --- |
| `status` | string | `"ok"`. |
| `database` | string | `"ok"`. |
| `version` | string | Build identity: the short VCS revision, suffixed `-dirty` for an uncommitted build, or `dev`. Omitted when unknown. |

**503 Service Unavailable** — the database is unreachable. The underlying driver
error is logged, never returned.

```json
{
  "error": {
    "code": "service_unavailable",
    "message": "The database is unreachable."
  }
}
```

---

### `POST /v1/auth/login`

Exchanges the account's username and password for a session. **No
authentication.** Rate limited to 10 attempts per minute per client.

The username is matched case-insensitively; the password is not.

**Request**

```http
POST /v1/auth/login
Content-Type: application/json

{ "username": "admin", "password": "correct horse battery staple" }
```

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `username` | string | yes | 3–30 characters of letters, digits, `_`, `.`. Lowercased before lookup. |
| `password` | string | yes | 12–256 characters. |

**200 OK**

```json
{
  "token": "harksess_V3kQ2mZ8bR1tXyLp0aNfCd7eJhSu4WgO7xY2bWv",
  "expires_at": "2026-09-08T09:41:17.882Z",
  "user": {
    "id": "0198f3a1-2b4c-7d8e-9f01-23456789abcd",
    "username": "admin",
    "display_name": "admin",
    "email": "admin@hark.local",
    "created_at": "2026-07-29T10:15:02.431Z"
  },
  "session": {
    "id": "0198f3b0-77c1-7a42-8e19-5f2b0c7d4a83",
    "created_at": "2026-08-09T09:41:17.882Z",
    "expires_at": "2026-09-08T09:41:17.882Z"
  }
}
```

The response also carries the session cookie:

```
Set-Cookie: __Host-hark_session=harksess_…; Path=/; Max-Age=2592000; HttpOnly; Secure; SameSite=Lax
```

`token` and the cookie hold the same value. A browser uses the cookie and
ignores `token`; a native or command-line client stores `token` and sends it as
`Authorization: Bearer`.

**Errors**

| Status | `code` | When |
| --- | --- | --- |
| 401 | `unauthorized` | Unknown username, no password set, or the wrong password. The three are deliberately indistinguishable. |
| 429 | `rate_limited` | Too many attempts. See `Retry-After`. |

---

### `POST /v1/auth/logout`

Retires whichever credential the caller presented. **Session or API token.**

* A session is deleted and its cookie expired.
* An API token is revoked; the next request carrying it gets `401`.

One endpoint covers both because "sign out" means the same thing to a browser
and to a CLI, and a client should not have to know which kind of credential it
holds in order to say it. Idempotent: retiring a credential that is already
gone succeeds.

No request body.

**204 No Content** — always, on success.

---

### `GET /v1/auth/session`

Describes the current principal. **Session or API token.** Never cached
(`Cache-Control: no-store`).

A dashboard calls this to decide whether to render a sign-in form; an agent
calls it to check that its token still works and what it is allowed to do.

**200 OK** — signed in with a session:

```json
{
  "kind": "session",
  "user": {
    "id": "0198f3a1-2b4c-7d8e-9f01-23456789abcd",
    "username": "admin",
    "display_name": "admin",
    "email": "admin@hark.local",
    "created_at": "2026-07-29T10:15:02.431Z"
  },
  "session": {
    "id": "0198f3b0-77c1-7a42-8e19-5f2b0c7d4a83",
    "created_at": "2026-08-09T09:41:17.882Z",
    "expires_at": "2026-09-08T09:41:17.882Z"
  },
  "api_token": null
}
```

**200 OK** — authenticated with an API token:

```json
{
  "kind": "api_token",
  "user": { "…": "as above" },
  "session": null,
  "api_token": {
    "id": "0198f3c2-1a5d-7b90-8c34-6e7f8a9b0c1d",
    "name": "harkctl",
    "prefix": "hark_c2xLm9J",
    "scopes": ["interactions:create", "interactions:read", "notifications:send"],
    "expires_at": "2026-11-07T09:41:17.882Z",
    "last_used_at": "2026-08-09T09:52:03.117Z",
    "revoked_at": null,
    "created_at": "2026-08-09T09:41:17.882Z"
  }
}
```

| Field | Type | Notes |
| --- | --- | --- |
| `kind` | string | `"session"` or `"api_token"`. |
| `user` | object | The account. Always present. |
| `session` | object \| null | Present when `kind` is `session`. |
| `api_token` | object \| null | Present when `kind` is `api_token`. |

**401 `unauthorized`** — no credential, or one that no longer resolves. This is
how a client learns it has been signed out.

---

### `POST /v1/auth/password`

Changes the account's password. **Session only** — an API token may not re-key
the account.

Every *other* session is signed out; the caller's own survives. Changing your
password from the dashboard should sign out the laptop you lost, not the
browser you are typing into. API tokens are unaffected: revoke them explicitly
if the password was compromised.

**Request**

```http
POST /v1/auth/password
Content-Type: application/json

{ "current_password": "correct horse battery staple", "new_password": "a different long passphrase" }
```

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `current_password` | string | yes | The password in force. Required so a stolen session alone cannot lock the owner out. |
| `new_password` | string | yes | 12–256 characters, no control characters. |

**204 No Content** on success.

**Errors**

| Status | `code` | When |
| --- | --- | --- |
| 401 | `unauthorized` | `current_password` is wrong. |
| 403 | `session_required` | Called with an API token. |
| 422 | `validation_failed` | `new_password` fails the policy. `fields` names it. |

There is no password-reset flow — Hark sends no mail. An operator who has lost
the password uses `harkd set-password` on the server; see the README.

---

### `POST /v1/auth/device/code`

Opens a device-authorization request: the flow a headless client uses to obtain
an API token with the account owner's approval, without ever handling their
password. **No authentication** — the client has no credential yet, which is
the point. Rate limited to 20 per minute per client.

The shape is OAuth 2.0's device authorization grant (RFC 8628) in this API's
own dress: snake_case JSON, this API's error envelope, and honest HTTP status
codes. The machine-readable `code` on each error carries the RFC's vocabulary.

**Request**

```http
POST /v1/auth/device/code
Content-Type: application/json

{
  "client_name": "harkctl",
  "scopes": ["notifications:send", "interactions:create", "interactions:read"],
  "token_expires_in_seconds": 7776000
}
```

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `client_name` | string | yes | 1–80 characters, trimmed. Shown to the human on the approval screen and becomes the issued token's `name`. |
| `scopes` | array of scope | yes | 1–9 known scopes. Shown to the human, deduplicated and sorted. |
| `token_expires_in_seconds` | integer \| null | no | Lifetime of the **token this pairing would issue**, not of the request. 3600 – 31 536 000. Default 7 776 000 (90 days). |

**201 Created**

```json
{
  "device_code": "harkdev_kQ2mZ8bR1tXyLp0aNfCd7eJhSu4WgO7xY2bWv",
  "user_code": "K7QM-3XPD",
  "verification_uri": "https://hark.example.com/cli/authorize",
  "verification_uri_complete": "https://hark.example.com/cli/authorize?code=K7QM-3XPD",
  "expires_at": "2026-08-09T09:51:17.882Z",
  "expires_in_seconds": 600,
  "interval_seconds": 5
}
```

| Field | Notes |
| --- | --- |
| `device_code` | The client's half. **Never show it to the human** and never log it. |
| `user_code` | The human's half, in canonical `XXXX-XXXX` form. Print it. |
| `verification_uri` | Where to send the human. |
| `verification_uri_complete` | The same page with the code filled in; open this in a browser when one is available. |
| `expires_in_seconds` | Always 600. The request is approvable for ten minutes. |
| `interval_seconds` | How long to wait between polls. Honour it — polling faster only makes the server raise it. |

**Errors**

| Status | `code` | When |
| --- | --- | --- |
| 422 | `validation_failed` | `client_name` or `scopes` are unusable. |
| 429 | `rate_limited` | Too many requests. |
| 503 | `service_unavailable` | Three generated code pairs collided in a row. Retry. |

The `user_code` alphabet is Crockford's base32 — digits and uppercase letters
minus `I`, `L`, `O` and `U`. Input is forgiving: case, spacing and hyphens are
ignored, and `I`/`L` read as `1` while `O` reads as `0`.

---

### `POST /v1/auth/device/token`

Polls a device-authorization request. **The device code in the body is the
credential.** Rate limited to 120 per minute per client.

**Request**

```http
POST /v1/auth/device/token
Content-Type: application/json

{ "device_code": "harkdev_kQ2mZ8bR1tXyLp0aNfCd7eJhSu4WgO7xY2bWv" }
```

**200 OK** — approved, and this poll minted the token:

```json
{
  "access_token": "hark_c2xLm9JbR1tXyLp0aNfCd7eJhSu4WgO7xY2bWvK",
  "token": {
    "id": "0198f3c2-1a5d-7b90-8c34-6e7f8a9b0c1d",
    "name": "harkctl",
    "prefix": "hark_c2xLm9J",
    "scopes": ["interactions:create", "interactions:read", "notifications:send"],
    "expires_at": "2026-11-07T09:41:17.882Z",
    "last_used_at": null,
    "revoked_at": null,
    "created_at": "2026-08-09T09:43:22.104Z"
  }
}
```

`access_token` is returned exactly once and cannot be recovered. A request
issues at most one token, ever.

**Every other outcome**

| Status | `code` | Meaning | Retry? |
| --- | --- | --- | --- |
| 400 | `authorization_pending` | Nobody has decided yet. | Yes — after `Retry-After`. |
| 429 | `slow_down` | You polled faster than the interval; it has been raised (by 5 s each time, to a 30 s ceiling) and never lowered. | Yes — after `Retry-After`. |
| 403 | `access_denied` | The human refused. | No. Start over. |
| 410 | `expired_token` | Nobody decided within ten minutes. | No. Start over. |
| 409 | `invalid_grant` | This request already issued its token. | No. |
| 409 | `token_limit_reached` | The account holds the maximum number of active tokens, so the approval was cancelled. | No. Revoke a token, then start over. |
| 404 | `not_found` | The device code is unknown or malformed. | No. |

The rule for a client is simply: **retry when `Retry-After` is present, stop
otherwise.**

---

### `GET /v1/auth/device/requests/{user_code}`

Describes a pairing request so an approval screen can render it. **Session
only.** Never cached.

`{user_code}` is what the human typed, in any case or spacing.

**200 OK**

```json
{
  "request": {
    "user_code": "K7QM-3XPD",
    "client_name": "harkctl",
    "scopes": ["interactions:create", "interactions:read", "notifications:send"],
    "status": "pending",
    "expires_at": "2026-08-09T09:51:17.882Z",
    "token_expires_at": "2026-11-07T09:41:17.882Z"
  }
}
```

| Field | Notes |
| --- | --- |
| `status` | `pending`, `approved`, `denied`, `expired`, or `consumed`. Only `pending` can still be decided. |
| `expires_at` | When the request stops being approvable. |
| `token_expires_at` | When the token it would issue expires. Show this: it is what the human is agreeing to. |

The device code is never exposed here.

A request whose time has passed is reported as `expired` on read, so a screen
polling this endpoint sees the state change without any background job.

**404 `not_found`** — no request matches that code (including a code that is
not well formed).

**The page behind it.** `verification_uri` is
[`/cli/authorize`](#get-cliauthorize) on the public origin, served by the
dashboard. It shows the client name, the user code, both expiry times and every
requested scope listed individually, then offers Approve and Deny. This endpoint
is the same request as JSON, for a client that would rather draw the screen
itself.

---

### `POST /v1/auth/device/requests/{user_code}/approve`

Approves a pairing request. **Session only** — approval is what causes a token
to be minted, so an API token cannot approve its own successor.

No request body. The next poll of the matching device code mints the token.

**200 OK** — the same envelope as the `GET`, with `status` now `approved`.

**Errors**

| Status | `code` | When |
| --- | --- | --- |
| 403 | `session_required` | Called with an API token. |
| 409 | `conflict` | The request is not awaiting a decision — unknown, already decided, or expired. The three are one answer because they mean the same thing: there is nothing here left to decide. |

---

### `POST /v1/auth/device/requests/{user_code}/deny`

Refuses a pairing request. **Session only.** No request body.

**200 OK** — the same envelope, with `status` now `denied`. The polling client
receives `403 access_denied`.

Same errors as `approve`.

---

### `GET /v1/tokens`

Lists the account's API tokens, newest first. **Session only.**

Revoked and expired tokens stay listed: a credential's history is worth keeping
visible, and hiding a revoked token makes "did I already revoke that?"
unanswerable.

**200 OK**

```json
{
  "tokens": [
    {
      "id": "0198f3c2-1a5d-7b90-8c34-6e7f8a9b0c1d",
      "name": "harkctl",
      "prefix": "hark_c2xLm9J",
      "scopes": ["interactions:create", "notifications:send"],
      "expires_at": "2026-11-07T09:41:17.882Z",
      "last_used_at": "2026-08-09T09:52:03.117Z",
      "revoked_at": null,
      "created_at": "2026-08-09T09:41:17.882Z"
    }
  ]
}
```

| Field | Type | Notes |
| --- | --- | --- |
| `prefix` | string | The first 13 characters of the secret, for recognising which token a log line is about. Never enough to use. |
| `expires_at` | string \| null | `null` means the token never expires. |
| `last_used_at` | string \| null | Stamped at most once a minute per token, so it is accurate to within a minute and no more. |
| `revoked_at` | string \| null | Non-null means the token is dead. |

The secret is never in this response, or any other after creation.

---

### `POST /v1/tokens`

Mints an API token. **Session only** — this is the boundary that keeps a leaked
token from creating a successor.

**Request**

```http
POST /v1/tokens
Content-Type: application/json

{
  "name": "CI deploy bot",
  "scopes": ["notifications:send"],
  "expires_in_seconds": 7776000
}
```

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `name` | string | yes | 1–80 characters, trimmed. |
| `scopes` | array of scope | yes | 1–9 known scopes; stored deduplicated and sorted. |
| `expires_in_seconds` | integer \| null | no | 3600 – 31 536 000. Absent or `null` mints a token that never expires. |

**201 Created**

```json
{
  "token": {
    "id": "0198f3c2-1a5d-7b90-8c34-6e7f8a9b0c1d",
    "name": "CI deploy bot",
    "prefix": "hark_c2xLm9J",
    "scopes": ["notifications:send"],
    "expires_at": "2026-11-07T09:41:17.882Z",
    "last_used_at": null,
    "revoked_at": null,
    "created_at": "2026-08-09T09:41:17.882Z"
  },
  "secret": "hark_c2xLm9JbR1tXyLp0aNfCd7eJhSu4WgO7xY2bWvK"
}
```

**`secret` is shown exactly once.** It is not stored and cannot be re-displayed;
losing it means minting a new token.

**Errors**

| Status | `code` | When |
| --- | --- | --- |
| 403 | `session_required` | Called with an API token. |
| 409 | `token_limit_reached` | The account already holds 25 active tokens. Revoke one first. |
| 422 | `validation_failed` | `name`, `scopes` or `expires_in_seconds` are unusable. `fields` names which. |

"Active" means not revoked and not expired, so revoking or letting a token
lapse frees a slot.

---

### `DELETE /v1/tokens/{id}`

Revokes one of the account's tokens. **Session only.**

Revocation is immediate: the next request carrying that token gets `401`. The
row is kept, so everything the token created keeps its attribution.

**204 No Content** on success.

**404 `not_found`** — unknown id, or a token that is already revoked. Both mean
the same thing to the caller: there is no token here to revoke.

To retire the token you are *currently using*, call
[`POST /v1/auth/logout`](#post-v1authlogout) with it instead — that needs no
session.

---

## Services

A **service** is a named webhook source: a title, a set of notification
defaults, and one credential embedded in a URL. It exists so that a system which
can only be handed a link can still send a notification that looks like it came
from somewhere.

### `GET /v1/services`

Lists the account's services, newest first. **Session, or a token with
`services:read`.** Not paged: an account has a handful of these.

**200 OK**

```json
{
  "services": [
    {
      "id": "0198f3a1-2b4c-7d8e-9f01-23456789abcd",
      "title": "Deploy bot",
      "image_url": "https://example.com/logo.png",
      "url": "https://example.com/deploys",
      "priority": "normal",
      "webhook_url": "https://hark.example.com/v1/hooks/harkhook_kQ2mZ8bR1tXyLp0aNfCd7eJhSu4WgO7xY2bWv",
      "created_at": "2026-08-01T09:00:00.000Z",
      "updated_at": "2026-08-01T09:00:00.000Z"
    }
  ]
}
```

| Field | Notes |
| --- | --- |
| `image_url` | Avatar shown as the sender. `null` when unset. |
| `url` | Default tap destination. `null` when unset. |
| `priority` | Default priority for this service's notifications. |
| `webhook_url` | The ingest URL, credential and all — **only for a session**. An API token sees `null`, because a read credential must not be able to widen itself into a send credential. |

### `POST /v1/services`

Creates a service and mints its webhook credential. **Session, or a token with
`services:write`.**

**Request**

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `title` | string | yes | 1–80 characters. The default sender name. |
| `image_url` | string \| null | no | Public HTTPS URL, ≤2048 characters. |
| `url` | string \| null | no | Tap destination: any scheme except `about:`, `blob:`, `data:`, `file:` and `javascript:`. |
| `priority` | enum | no | `normal` (default), `time_sensitive`, `critical`. |

**201 Created**

```json
{
  "service": { "…as above…" },
  "webhook_url": "https://hark.example.com/v1/hooks/harkhook_kQ2mZ8bR1tXyLp0aNfCd7eJhSu4WgO7xY2bWv"
}
```

The URL contains the credential. It is returned to whoever created the service —
including an API token — because nobody else can recover it: the stored form is
a ciphertext, and only a session is ever shown the decrypted URL again.

**422 `validation_failed`** names the offending field.

### `GET /v1/services/{id}`

One service. **Session, or a token with `services:read`.** `404 not_found` when
it does not exist.

### `PATCH /v1/services/{id}`

Changes a service's defaults. **Session, or a token with `services:write`.**

Only the fields the request names are written. `null` clears `image_url` or
`url`; `title` and `priority` cannot be cleared. At least one field is required.

```http
PATCH /v1/services/0198f3a1-2b4c-7d8e-9f01-23456789abcd
Content-Type: application/json

{ "title": "Deploys", "image_url": null }
```

**200 OK** — `{ "service": { … } }`.

### `POST /v1/services/{id}/webhook-token`

Rotates the credential. **Session only** — this is credential management.

No request body. **201 Created**, with the same envelope as the create: the new
URL, and the service.

The previous URL stops working immediately. There is no grace period and no
second slot, because rotation is what an owner reaches for when a token has
leaked, and a leaked token that keeps working for an hour is not rotated.

### `DELETE /v1/services/{id}`

**Session, or a token with `services:write`.** **204 No Content.**

This is destructive to history: the service's deliveries go with it, along with
the questions those deliveries asked and every Live Activity it started.

---

## Devices

A **device** is one iOS installation, keyed by its APNs token. iOS reissues that
token whenever it likes, so a phone that has been reinstalled appears as a new
device and the old row lives on until a push to it fails.

### `GET /v1/devices`

Every device on the account, most recently seen first. **Session, or a token
with `devices:read`.** Not paged.

**200 OK**

```json
{
  "devices": [
    {
      "id": "0198f3b0-77c1-7a42-8e19-5f2b0c7d4a83",
      "name": "Ali's iPhone",
      "platform": "ios",
      "active": true,
      "interaction_schema_version": 1,
      "live_activity_interaction_version": 1,
      "live_activity_capable": true,
      "push_to_start_environment": "production",
      "push_to_start_updated_at": "2026-08-09T13:22:00.000Z",
      "created_at": "2026-07-01T10:00:00.000Z",
      "last_seen_at": "2026-08-09T13:22:00.000Z"
    }
  ]
}
```

| Field | Notes |
| --- | --- |
| `active` | `false` once APNs reported the token permanently invalid. Registering again revives it. |
| `interaction_schema_version` | `1` when this installation can answer a question from a notification; `null` when it cannot, and it is then not sent one. |
| `live_activity_interaction_version` | `1` when it can answer from the Lock Screen. |
| `live_activity_capable` | Derived: a push-to-start token, a known environment, and a schema version this server speaks. |

### `POST /v1/devices`

Registers or refreshes this phone. **Session only** — the phone is the account
owner signing in.

**Request**

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `apns_token` | string | yes | 32–400 hexadecimal characters, case-insensitive. |
| `name` | string \| null | no | ≤80 characters, for the device list. |
| `interaction_schema_version` | integer \| null | no | `1` if this build can answer a question from a notification. |
| `live_activity_interaction_version` | integer \| null | no | `1` if it can answer from the Lock Screen. |

**201 Created** — `{ "device": { … } }`.

Two behaviours worth knowing:

* **Omission clears.** The client sends its complete current state every time,
  so a capability it leaves out is one it no longer has, not one to remember.
* **The token is the identity.** Registering the same token twice updates one
  row; registering a reissued token creates a second. The response's `id` is
  what the other device endpoints take, so store it.

The first device an account registers also receives a short welcome sequence.
If that very first push comes back undeliverable, the response reports
`"active": false` — the token the app just handed over is already dead.

### `GET /v1/devices/{id}`

One device. **Session, or a token with `devices:read`.**

### `DELETE /v1/devices/{id}`

Unregisters a phone. **Session only.** **204 No Content**, `404 not_found` for
an unknown id.

The row is deleted rather than deactivated, which takes its Live Activity
deliveries with it *without* sending an end push: an activity can therefore stay
on a screen with no record of it. That is the right trade for "this is not my
phone any more".

### `PUT /v1/devices/{id}/push-to-start-token`

Records the ActivityKit push-to-start token, which is what makes a device
Live-Activity-capable. **Session only.**

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `token` | string | yes | 32–512 hexadecimal characters. |
| `environment` | enum | yes | `sandbox` or `production`. A token minted in one is silently ignored by the other, so it travels with the token. |
| `schema_version` | integer | no | The content-state version this build understands. Must be `1`. |

**204 No Content.** `404 not_found` when the device is unknown or inactive.

`PUT` because it is a replace: iOS reissues the token and the app re-reports it,
and the same value arriving twice must not be an error.

### `PUT /v1/devices/{id}/activity-update-token`

Reports the per-activity update token for a Live Activity that was started by
push. **Session only.**

Nothing can update or end an activity until this token comes back. The phone
cannot say which Hark activity it belongs to — it only knows ActivityKit's own
identifier — so the delivery is inferred: an exact match on a previously
reported native id wins, and failing that the search is narrowed to deliveries
still waiting to be associated.

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `update_token` | string | yes | 32–512 hexadecimal characters. |
| `native_activity_id` | string | no | ActivityKit's `activity.id`, ≤200 characters. |
| `activity_id` | string | no | Hark's id, when the client knows it. Supplying it turns the inference into a lookup. |
| `environment` | enum | yes | `sandbox` or `production`. |
| `schema_version` | integer | no | Must be `1`. |

**200 OK**

```json
{
  "activity_id": "0198f3c2-1a5d-7b90-8c34-6e7f8a9b0c1d",
  "delivery_id": "0198f3c2-1a5d-7b90-8c34-6e7f8a9b0c1e"
}
```

| Status | `code` | When |
| --- | --- | --- |
| 404 | `not_found` | No delivery on this device is waiting for a token. |
| 409 | `conflict` | More than one is. Guessing would attach the token to the wrong activity and silently break both; retry once the other start has resolved. |

### `PUT /v1/activity-deliveries/{id}/update-token`

The same report, from a process that has no session. **The capability in the
body is the credential.**

A Live Activity outlives the app that started it, and the widget extension holds
nothing but the attributes of the push that created it — among them
`token_registration_url` and `registration_token`. That capability is bound to
this delivery, this activity and this deadline, so it grants exactly one thing
and expires on its own.

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `registration_token` | string | yes | The capability from the start push. |
| `native_activity_id` | string | no | ActivityKit's `activity.id`. |
| `update_token` | string | yes | 32–512 hexadecimal characters. |

**204 No Content.**

**404 `not_found`** for everything else — an unknown delivery, one that has
finished, and a capability that does not verify are one answer, so the route
cannot be used to discover which deliveries exist.

---

## Notifications

### `POST /v1/notifications`

Sends a one-shot push. **API token with `notifications:send`.** Honours
[`Idempotency-Key`](#idempotency).

**Request**

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `body` | string | yes | 1–2000 characters. |
| `title` | string | no | 1–80 characters. Defaults to `"Hark"`; it is shown as the sender. |
| `image_url` | string | no | Public HTTPS URL. |
| `url` | string | no | Tap destination. |
| `priority` | enum | no | `normal` (default), `time_sensitive`, `critical`. |
| `device_ids` | array of id | no | 1–50 entries. Absent means every reachable device. |

**201 Created**

```json
{
  "notification": {
    "id": "0198f3d3-9c11-7f52-a0b7-5d8e9f0a1b2c",
    "title": "CI",
    "body": "Build 4821 succeeded",
    "image_url": null,
    "url": null,
    "priority": "normal",
    "accepted_count": 1,
    "created_at": "2026-08-09T13:20:11.000Z"
  },
  "replayed": false,
  "message": null
}
```

`message` is non-null when the count needs explaining: no device is registered,
or nothing was accepted. See [what `accepted` means](#what-accepted-means).

**Errors** — `403 api_token_required` for a session, `409 conflict` for a reused
idempotency key, `422 validation_failed`, `429 rate_limited`.

---

## Interactions

An **interaction** is a question sent to the phone that expects an answer. It is
a notification plus a promise: the answer comes back to whoever asked, either by
polling or through a callback.

Its `kind` decides what may be answered — `approval` → `approve`/`deny`,
`yes_no` → `yes`/`no`, `reply` → `reply` with text — and its `presentation`
decides where it appears: as a notification with action buttons, or as a card on
the Lock Screen that can be answered without unlocking.

Every status other than `pending` is final.

### `POST /v1/interactions`

Asks a question. **API token with both `interactions:create` and
`notifications:send`** — it sends a push *and* creates something the sender can
read the answer to. Honours [`Idempotency-Key`](#idempotency).

**Request**

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `title` | string | yes | 1–80 characters. Who is asking. |
| `prompt` | string | yes | 1–2000 characters; ≤240 for a Lock Screen card. |
| `kind` | enum | yes | `approval`, `yes_no`, `reply`. |
| `presentation` | enum | no | `notification` (default) or `live_activity`. |
| `style` | enum | no | Lock Screen layout: `approval` (default), `shell`, `verdict`, `signal`. Requires `presentation: live_activity`. |
| `primary_label` | string | no | 1–24 characters, single line. Requires `presentation: live_activity`. |
| `secondary_label` | string | no | Same. |
| `image_url` | string | no | Public HTTPS URL. Not available on a Lock Screen card. |
| `url` | string | no | Tap destination. Not available on a Lock Screen card. |
| `priority` | enum | no | `normal` (default), `time_sensitive`, `critical`. |
| `device_ids` | array of id | no | 1–50 entries. |
| `expires_in_seconds` | integer | no | 30 – 86400, default 900. A Lock Screen card is additionally capped at 28800, the eight hours iOS allows. |

A question has to expire: an agent blocked on an answer needs to know when to
give up, and a prompt that lives forever is a prompt nobody answers.

**201 Created**

```json
{
  "interaction": {
    "id": "0198f3e4-0d22-7063-b1c8-6e9f0a1b2c3d",
    "title": "Claude Code",
    "prompt": "Run the migration?",
    "kind": "approval",
    "presentation": "notification",
    "status": "pending",
    "choices": ["approve", "deny"],
    "response": null,
    "url": null,
    "image_url": null,
    "action_digest": "6f1c…",
    "primary_label": null,
    "secondary_label": null,
    "correlation_id": null,
    "accepted_count": 1,
    "responding_device_id": null,
    "expires_at": "2026-08-09T14:15:00.000Z",
    "created_at": "2026-08-09T14:00:00.000Z",
    "responded_at": null,
    "canceled_at": null
  },
  "accepted": 1,
  "activity_id": null,
  "replayed": false,
  "message": null
}
```

| Field | Notes |
| --- | --- |
| `choices` | Derived from `kind`; these are the values `action` may take when answering. |
| `action_digest` | Binds an answer to this exact question. A client sends it back when it answers, which is what stops a phone showing a stale prompt from answering the one that replaced it. |
| `activity_id` | The Live Activity presenting the question, when one could be started. `null` means it went out as a notification instead — including when `presentation` was `live_activity` but no device could show one, which `message` then says. |

**`presentation: live_activity` falls back to a notification.** If no device on
the account can draw an interactive card — none registered a push-to-start token,
or none is new enough for `live_activity_interaction_version` — the question is
delivered as an ordinary question notification rather than dropped. It carries
the same buttons, the same `response_token` and the same answer route; only the
surface is plainer. `activity_id` is `null` and `message` says what happened.
`accepted` is `0`, with a different `message`, when no device can present the
question either way.

**422 `validation_failed`** covers the cross-field rules. A Lock Screen card is a
smaller surface than a notification: two buttons, no free-text reply, no link,
and no more than eight hours. Saying so at creation is better than accepting the
request and quietly dropping half of it.

### `GET /v1/interactions`

The inbox. **Session, or a token with `interactions:read`.**
[Paged](#pagination).

| Parameter | Default | Notes |
| --- | --- | --- |
| `status` | `pending` | `pending` — still awaiting an answer and not past its deadline. `all` — every question, newest first. |

**200 OK** — `{ "interactions": [ … ], "next_cursor": null }`, where each item is
an interaction plus:

| Field | Notes |
| --- | --- |
| `source_name` | Who is asking: the service title, else the token name, else the question's own title. |
| `source_image_url` | The question's image, else the service's. |

A question whose deadline has passed is filtered out rather than expired here: a
phone opening its inbox should not resolve every stale prompt at once.

### `GET /v1/interactions/{id}`

One question. **Session, or a token with `interactions:read`.**

| Parameter | Default | Notes |
| --- | --- | --- |
| `wait_seconds` | `0` | 0–25. Hold the request open until the question is answered, or until the wait runs out. |

The wait is what turns "ask and poll" into "ask and block": an agent that wants
an answer before it continues holds one request open instead of hammering the
endpoint, and gets the answer the moment it lands. It always answers `200` — a
question that is still `pending` when the wait runs out is an answer to "what is
it doing", not an error.

**200 OK** — `{ "interaction": { … } }`.

### `POST /v1/interactions/{id}/response`

Answers a question. **A session, or the `response_token` from the push
payload.**

One route serves all three ways an answer arrives — the app with a session, the
notification-service extension, and the Lock Screen widget — because they are
the same act. The last two hold no session, so they present the one-shot
`response_token` the push carried.

**An API token may not answer, whatever its scopes.** It is the credential that
*asks* questions, and one that could also answer them would make approval a
formality — an agent wanting a "yes" would grant itself one. A token that tries
gets `403 session_required`, decided before the question is even looked up. The
`response_token` is never returned to the caller that created the question; it
exists in plaintext only inside the push that reaches the phone.

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `action` | string | yes | One of the question's `choices`. |
| `text` | string | for `reply` | 1–4000 characters. |
| `device_id` | id | yes | The phone answering. Must be active and on the account. |
| `action_digest` | string | yes | The digest the phone was shown. |
| `response_token` | string | no | The credential from the push. Omit when the request carries a session. |

**200 OK** — `{ "interaction": { … } }` with the new status.

Answering twice with the same action from the same device is also `200`: a
notification action that is tapped twice must not report an error.

| Status | `code` | When |
| --- | --- | --- |
| 401 | `unauthorized` | No credential at all, and no `response_token`. |
| 403 | `session_required` | An API token, with no `response_token` alongside it. An agent cannot answer the questions it asks. |
| 404 | `not_found` | Unknown question, unknown device, or a `response_token` that does not match. The three are one answer. |
| 409 | `action_digest_mismatch` | The digest does not match the stored question. |
| 409 | `conflict` | The question has already been settled differently. |
| 422 | `validation_failed` | The action is not one this question offers, or a reply carried no text. |

### `POST /v1/interactions/{id}/cancel`

Withdraws a question. **Session, or a token with `interactions:create`.** No
request body.

The owner can always withdraw a question addressed to them, whoever asked it: an
agent that crashed after asking should not be able to leave a prompt on the Lock
Screen until it expires.

**200 OK** — `{ "interaction": { … } }` with `status: "canceled"`.
`409 conflict` when it is no longer pending; `404 not_found` when there is no
such question.

---

## Live Activities

A **Live Activity** is a card on the Lock Screen that a requester keeps up to
date: a deploy in progress, a long test run, a question waiting to be answered.

Two constraints shape the whole surface, and both come from iOS rather than from
Hark:

* **A phone shows one ordinary activity at a time.** A start either takes the
  slot, is refused with `409 activity_conflict`, or replaces what is there.
  (A card presenting a question is exempt: it may sit alongside one.)
* **An activity lives at most eight hours.** After that iOS removes it, so an
  activity past its deadline is reported as `expired` on the next read.

An activity can be addressed by its `id` or by the `key` its requester chose. A
key is unique among that requester's *running* activities and is free again as
soon as one ends.

### The state document

`state` is what the phone renders, and it is delivered verbatim.

```json
{
  "schema_version": 1,
  "activity_id": "0198f3c2-1a5d-7b90-8c34-6e7f8a9b0c1d",
  "title": "Deploy",
  "status": "Building",
  "detail": "step 3 of 7",
  "progress": 0.42,
  "updated_at": "2026-08-09T13:31:00.000Z",
  "symbol": "build",
  "privacy_mode": "standard",
  "accent_color": "#E13B3B",
  "style": "standard"
}
```

| Field | Notes |
| --- | --- |
| `title` | 1–80 characters. |
| `status` | 1–60 characters. The one line that changes most. |
| `detail` | 1–240 characters. **Omitted** rather than nulled when absent — the widget lays itself out differently with and without it. |
| `progress` | 0.0–1.0. Same omission rule. |
| `symbol` | `terminal` (default), `code`, `build`, `success`, `warning`. |
| `privacy_mode` | `standard` (default) or `private`. `private` redacts the banner announcing the start; the state itself always carries the real values, and the widget decides what to show. |
| `accent_color` | `#RRGGBB`, default `#E13B3B`. |
| `style` | `standard` (default), `ring`, `hero`, `terminal`, `steps`. The four interactive styles belong to questions and are refused here. |
| `interaction` | Present only on a card that presents a question: its id, kind, prompt, the two button labels and the actions they post, and the answer once there is one. |

### `GET /v1/activities`

The account's activities, newest first. **Session, or a token with
`activities:read`.** [Paged](#pagination).

| Parameter | Default | Notes |
| --- | --- | --- |
| `status` | `live` | `live` — what is on a Lock Screen right now. `all` — including finished ones. |

**200 OK** — `{ "activities": [ … ], "next_cursor": null }`, each item an
activity plus `source_name` and `source_image_url`.

Cards presenting a question are not listed: they are shown as the question.

### `POST /v1/activities`

Starts an activity. **API token with `activities:write`.** Honours
[`Idempotency-Key`](#idempotency).

**Request**

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `key` | string | no | 1–100 characters. A stable handle for this run, reusable once it ends. |
| `replace` | boolean | no | Take over the device slot, or the key, instead of being refused. Default `false`. |
| `title` | string | yes | 1–80 characters. |
| `status` | string | yes | 1–60 characters. |
| `detail` | string | no | 1–240 characters. |
| `progress` | number | no | 0.0–1.0. |
| `symbol` | enum | no | Default `terminal`. |
| `privacy_mode` | enum | no | Default `standard`. |
| `accent_color` | string | no | `#RRGGBB`, default `#E13B3B`. |
| `style` | enum | no | Default `standard`. |
| `device_ids` | array of id | no | 1–50 entries. |
| `expires_in_seconds` | integer | no | 60 – 28800, default 28800. |
| `stale_after_seconds` | integer | no | 0 – 28800, default 14400. When the card should be shown as stale. |

**201 Created**

```json
{
  "activity": {
    "id": "0198f3c2-1a5d-7b90-8c34-6e7f8a9b0c1d",
    "key": "deploy",
    "status": "active",
    "sequence": 0,
    "state": { "…as above…" },
    "accepted_count": 1,
    "failed_count": 0,
    "expires_at": "2026-08-09T21:30:00.000Z",
    "stale_at": "2026-08-09T17:30:00.000Z",
    "created_at": "2026-08-09T13:30:00.000Z",
    "updated_at": "2026-08-09T13:30:00.000Z",
    "ended_at": null
  },
  "accepted": 1,
  "failed": 0,
  "replaced": 0,
  "replayed": false,
  "message": null
}
```

| Field | Notes |
| --- | --- |
| `status` | `starting`, `active`, `partial` while it runs; `failed`, `ended`, `expired` once it is over. |
| `sequence` | The optimistic-concurrency token. Send it back as `if_sequence` to make a change conditional on nothing else having happened. |
| `accepted_count` / `failed_count` | The result of the **most recent** operation, not a lifetime total. |
| `replaced` | Present only when the request set `replace`, and counts the activities that were ended to make room. |
| `message` | The distinct reasons a device was not reached — `MissingPushToStartToken`, `InteractionTerminal`, an APNs reason. |

**409 `activity_conflict`** when a device slot or the key is taken. The blocking
activity's id is named only when this requester started it; another integration
on the same account may legitimately hold the device, and telling this caller
which id holds it would hand it a handle it has no business having.

### `GET /v1/activities/{identifier}`

One activity, by id or by key. **Session, or a token with `activities:read`.**
**200 OK** — `{ "activity": { … } }`.

Because a key can be reused, the lookup prefers a running activity and then the
newest.

### `PATCH /v1/activities/{identifier}`

Changes a running activity and pushes the change. **API token with
`activities:write`.** Honours [`Idempotency-Key`](#idempotency).

Every field is optional; at least one is required. `detail` and `progress`
accept `null`, which removes them.

| Field | Type | Notes |
| --- | --- | --- |
| `title`, `status`, `detail`, `progress`, `symbol`, `privacy_mode`, `accent_color`, `style` | — | As in the start. |
| `stale_after_seconds` | integer | Restarts the staleness window. Omitting it keeps the window the activity already had, measured forward from now — an activity that reports every minute keeps looking fresh. |
| `if_sequence` | integer | Apply only if the activity is still at this sequence. |

**200 OK** — the same envelope as the start, without `replaced`.

An update that reaches no device is still a `200`: the change is recorded and
`message` says why nothing landed. `MissingUpdateToken` is the common one — a
start that APNs accepted is not yet an activity that can be driven, because the
phone has to report its per-activity token first.

| Status | `code` | When |
| --- | --- | --- |
| 404 | `not_found` | No running or finished activity of this requester matches. |
| 409 | `conflict` | It has already finished. |
| 409 | `sequence_conflict` | `if_sequence` did not match, or something changed it between the read and the write. |

### `POST /v1/activities/{identifier}/end`

Finishes an activity and pushes the final state. **API token with
`activities:write`.** Honours [`Idempotency-Key`](#idempotency).

| Field | Type | Notes |
| --- | --- | --- |
| `status` | string | 1–60 characters, default `"Complete"`. |
| `detail`, `progress` | — | As in the update, `null` to remove. |
| `symbol` | enum | Default `success`. |
| `accent_color` | string | Omitting it keeps the current colour. |
| `dismiss_after_seconds` | integer | 0 – 14400, default 0. How long the finished card stays on screen. |
| `if_sequence` | integer | As above. |

**200 OK** — the update envelope, with the activity now `ended`.

The end is a state transition with content, not a deletion — the last thing a
person sees is the card saying how it went — which is why this is a `POST` with
a body rather than a `DELETE`. The record stays in the history.

---

## Push payloads

This section is the other half of the contract: what the server puts on the
wire when something it accepted has to reach a phone. It is written for the iOS
client — the app, its notification-service extension, and its widget extension
— and nothing in it is reachable over HTTP.

Two vocabularies share one payload, and knowing which is which explains every
naming inconsistency in it:

* **`aps` is Apple's.** Hyphenated keys, values that mean what Apple's
  documentation says they mean. `mutable-content`, `interruption-level`,
  `content-state`, `input-push-token` and the rest are decoded by iOS itself.
* **Everything else is Hark's**, and follows this document's conventions:
  `snake_case` keys, RFC 3339 timestamps, UUIDv7 ids. A notification keeps it
  under a single top-level `hark` key; a Live Activity keeps it in
  `aps.attributes` and `aps.content-state`, because those are the two places
  ActivityKit will carry a payload of the app's own design.

Every Hark object carries `schema_version`, currently `1`. A device announces
the versions it understands when it registers
([`POST /v1/devices`](#post-v1devices),
[`PUT …/push-to-start-token`](#put-v1devicesidpush-to-start-token)), and a
device that speaks a different one is [dropped from the
send](#choosing-devices) rather than shown something it cannot draw. Adding a
field is compatible and does not bump the version — **ignore keys you do not
recognise**.

### Notification payload

Sent for a webhook event, an agent notification, and the welcome pair. One
message per device.

```json
{
  "aps": {
    "alert": { "title": "Acme CRM", "body": "New sign-up" },
    "sound": "default",
    "mutable-content": 1,
    "thread-id": "service-0198f3a1-2b4c-7d8e-9f01-000000000001"
  },
  "hark": {
    "schema_version": 1,
    "device_id": "0198f3a1-2b4c-7d8e-9f01-23456789abcd",
    "record_id": "0198f3a1-2b4c-7d8e-9f01-000000000002",
    "thread_key": "service-0198f3a1-2b4c-7d8e-9f01-000000000001",
    "url": "https://acme.example/signups/1042",
    "source": {
      "id": "0198f3a1-2b4c-7d8e-9f01-000000000001",
      "name": "Acme CRM",
      "image_url": "https://acme.example/logo.png"
    }
  }
}
```

**`aps`**

| Key | Rule |
| --- | --- |
| `alert` | Always `{title, body}`. Plain strings — no localisation keys, no subtitle. |
| `sound` | `"default"`, or the critical-alert object for a critical notification. |
| `mutable-content` | Always `1`. The notification-service extension has to run: it downloads `source.image_url` and redraws the notification in the communication style. |
| `interruption-level` | **Omitted** for an ordinary notification, which iOS reads as its default level. Otherwise `"time-sensitive"` or `"critical"`. |
| `category` | Present only on a question. See below. |
| `thread-id` | The conversation, same string as `hark.thread_key`. |
| `badge` | **Never sent.** The badge is the count of unanswered questions, and the client knows it sooner than a push does. |

Priority maps like this, and the `apns-priority` header is `10` in every row —
a notification Hark sends is worth showing now or not at all:

| API `priority` | `aps.sound` | `aps.interruption-level` |
| --- | --- | --- |
| `normal` | `"default"` | *(omitted)* |
| `time_sensitive` | `"default"` | `"time-sensitive"` |
| `critical` | `{"critical": 1, "name": "default", "volume": 1}` | `"critical"` |

A critical notification only actually breaks through a Focus when the app ships
the critical-alert entitlement **and** the owner has granted it. Without both,
iOS silently downgrades it. The server sends the level either way.

**`hark`**

| Field | Presence | Notes |
| --- | --- | --- |
| `schema_version` | always | `1`. |
| `device_id` | always | The phone this copy was addressed to. Both extensions need it to answer a question, and neither can be relied on to have read it from shared storage. |
| `record_id` | always | The row this came from — an event, a notification, or a question — so tapping through opens the right history entry. The welcome pair carries a synthetic id instead, since there is no row behind it. |
| `thread_key` | always | The conversation. Group the inbox by it the way `aps.thread-id` groups the Lock Screen. |
| `url` | omitted when absent | The tap destination. See below. |
| `source.id` / `source.name` | always | The sender: a service, or the API token that sent it. |
| `source.image_url` | omitted when absent | A public HTTPS avatar. |
| `question` | only on a question | Below. |

Nothing in a payload identifies the account. It names the sender and the row,
never the owner, and it never carries a webhook token or any other stored
credential — the one secret it does carry is the single-use `response_token`,
which exists in plaintext nowhere else.

**Tap destinations.** `hark.url` is opened when the notification body is
tapped, and never when an action button is. The server has already checked it:
at most 2048 characters, and not an `about:`, `blob:`, `data:`, `file:` or
`javascript:` URL. Everything else is allowed on purpose — `https:`, universal
links, and custom app schemes such as `things:///show?id=abc` or
`shortcuts://run-shortcut?name=Deploy`, so a notification can hand off to
whatever app the work actually lives in. Re-check the length and the scheme on
the client before opening it; a push has travelled a long way to get there.

### Question payload

A question is a notification with `aps.category` set and a `hark.question`
object alongside. The category is what makes iOS draw the buttons.

```json
{
  "aps": {
    "alert": { "title": "Release", "body": "Deploy production?" },
    "sound": "default",
    "mutable-content": 1,
    "category": "hark.approval.v1",
    "thread-id": "interaction-0198f3a1-2b4c-7d8e-9f01-000000000003"
  },
  "hark": {
    "schema_version": 1,
    "device_id": "0198f3a1-2b4c-7d8e-9f01-23456789abcd",
    "record_id": "0198f3a1-2b4c-7d8e-9f01-000000000003",
    "thread_key": "interaction-0198f3a1-2b4c-7d8e-9f01-000000000003",
    "source": { "id": "0198f3a1-2b4c-7d8e-9f01-000000000004", "name": "deploy-bot" },
    "question": {
      "id": "0198f3a1-2b4c-7d8e-9f01-000000000003",
      "kind": "approval",
      "category": "hark.approval.v1",
      "action_digest": "6f1c…",
      "response_token": "…",
      "primary_label": "Ship it",
      "secondary_label": "Hold",
      "expires_at": "2026-08-09T13:00:00.000Z"
    }
  }
}
```

| Field | Presence | Notes |
| --- | --- | --- |
| `id` | always | Answer at [`POST /v1/interactions/{id}/response`](#post-v1interactionsidresponse). |
| `kind` | always | `approval`, `yes_no` or `reply`. |
| `category` | always | The same identifier as `aps.category`, so a decoder never has to read it off the notification. |
| `action_digest` | always | Echo it back when answering. It binds the answer to the exact question that was displayed, which is what stops a stale notification answering the one that replaced it. |
| `response_token` | omitted when absent | A single-use credential that lets an extension answer with no session at all. Present on every question a webhook or an agent asked. |
| `primary_label` / `secondary_label` | omitted when absent | Override the labels the registered category carries. |
| `expires_at` | always | When answering stops working. Retire the prompt yourself rather than letting someone tap into a 404. |

Categories are registered by the client at launch, and are versioned because
changing a category's actions would relabel notifications already sitting on a
Lock Screen:

| `kind` | Category | Action identifiers | Buttons |
| --- | --- | --- | --- |
| `approval` | `hark.approval.v1` | `hark.action.approve`, `hark.action.deny` | Approve, Deny (destructive) |
| `yes_no` | `hark.yes_no.v1` | `hark.action.yes`, `hark.action.no` | Yes, No (destructive) |
| `reply` | `hark.reply.v1` | `hark.action.reply` | Reply (text input) |

An unrecognised kind is sent as `hark.reply.v1`: free text is the only answer
that can express one nobody anticipated.

The last segment of an action identifier is exactly the `action` string
[the answer endpoint](#post-v1interactionsidresponse) takes, so an extension
strips the `hark.action.` prefix and posts the rest — there is no table to keep
in step. These identifiers are the client's to register; the server sends the
category and never the actions.

### Live Activity payload

One envelope shape serves all three events. Everything outside `content-state`
and `attributes` is Apple's.

**Start** — the only event that can set attributes, because ActivityKit fixes
them for the life of the card:

```json
{
  "aps": {
    "timestamp": 1786000000,
    "event": "start",
    "content-state": {
      "schema_version": 1,
      "activity_id": "0198f3c2-1a5d-7b90-8c34-6e7f8a9b0c1d",
      "title": "Deploy",
      "status": "Building",
      "progress": 0.42,
      "updated_at": "2026-08-09T13:31:00.000Z",
      "symbol": "build",
      "privacy_mode": "standard",
      "accent_color": "#E13B3B",
      "style": "standard"
    },
    "attributes-type": "HarkActivityAttributes",
    "attributes": {
      "schema_version": 1,
      "delivery_id": "0198f3c2-1a5d-7b90-8c34-000000000001",
      "device_id": "0198f3a1-2b4c-7d8e-9f01-23456789abcd",
      "token_registration_url": "https://hark.example.com/v1/activity-deliveries/0198f3c2-1a5d-7b90-8c34-000000000001/update-token",
      "token_registration_token": "…"
    },
    "alert": { "title": "Deploy", "body": "Building" },
    "input-push-token": 1,
    "stale-date": 1786014400
  }
}
```

**Update** — the state and the dates, nothing else:

```json
{
  "aps": {
    "timestamp": 1786000060,
    "event": "update",
    "content-state": { "…": "the same document, with new values" },
    "stale-date": 1786014460
  }
}
```

**End** — adds the moment iOS should take the card away:

```json
{
  "aps": {
    "timestamp": 1786000300,
    "event": "end",
    "content-state": { "…": "the final state" },
    "stale-date": 1786014700,
    "dismissal-date": 1786000300
  }
}
```

| Key | Presence | Notes |
| --- | --- | --- |
| `timestamp` | always | Epoch **seconds**, and strictly increasing across the activity's life. ActivityKit discards a content state that is not newer than the one on screen, so this is a counter rather than a clock. |
| `event` | always | `start`, `update` or `end`. |
| `content-state` | always | [The state document](#the-state-document), delivered verbatim: what a requester wrote is literally what the widget renders. |
| `attributes-type` | start only | Always `HarkActivityAttributes`. ActivityKit reconstructs the client's attributes by this name, so the app must declare exactly `struct HarkActivityAttributes: ActivityAttributes` — a differently named type makes every start push land nowhere, silently. |
| `attributes` | start only | Below. |
| `alert` | start only | The banner announcing the card. Not a separate notification: no sound, no category, no thread. A `private` activity's banner says only that something started; the content state alongside it still carries the real title and status, because what a locked screen shows is the widget's decision. |
| `input-push-token` | start only | `1`. Asks ActivityKit to hand the app a per-activity update token — without it the card can be created and never changed again. |
| `stale-date` | when the activity has one | Epoch seconds. May appear on any event. |
| `dismissal-date` | end only, when set | Epoch seconds. |
| `relevance-score` | — | Never sent. |

**`attributes`** is the widget's only route back to the server. The widget
extension has no session and no access to the app's keychain, so everything a
Lock Screen button needs has to be here:

| Field | Presence | Notes |
| --- | --- | --- |
| `schema_version` | always | `1`. |
| `delivery_id` | always | This card, on this phone. |
| `device_id` | always | Needed to answer a question, and to report a token. |
| `token_registration_url` | always | Where to `PUT` the per-activity update token ActivityKit just handed the app: [`PUT /v1/activity-deliveries/{id}/update-token`](#put-v1activity-deliveriesidupdate-token). **Until this call lands the server can start the card but never update or end it.** |
| `token_registration_token` | always | The capability authorising that call. It is scoped to this delivery and expires with the activity. |
| `question.id` / `question.action_digest` / `question.response_token` | only on a card presenting a question | The same three values a question notification carries, so a Lock Screen button answers through the same endpoint the notification action does. |

### Delivery rules

These are the transport's, and they explain what a client can and cannot rely
on.

| Rule | Notification | Live Activity |
| --- | --- | --- |
| `apns-push-type` | `alert` | `liveactivity` |
| `apns-topic` | the bundle id | the bundle id + `.push-type.liveactivity` |
| `apns-priority` | `10` | `10` |
| `apns-expiration` | `0` — APNs delivers it now or discards it | *(not sent; Apple's default applies)* |
| `apns-collapse-id` | never sent | never sent |

* **Payloads are capped at 4096 bytes**, encoded. The server checks before it
  sends, so an oversized push is recorded as a delivery failure rather than
  spending a round trip to be refused.
* **Nothing is retried.** Not a rate limit, not a provider outage, not a
  timeout. A push APNs did not take is lost on purpose: a duplicate
  notification is worse than a missing one, and the client's own reconciliation
  is what closes the gap — it re-registers its tokens on every foreground, and
  it locally ends any Live Activity the server no longer lists.

  There is one exception, and it is the case where a retry cannot duplicate
  anything: a `403 ExpiredProviderToken` says Apple rejected the *server's* JWT
  rather than the notification, so nothing was delivered. The transport drops
  its cached provider token, mints a fresh one, and sends that push again —
  exactly once. A second `403` is a failure like any other.
* **A dead token retires its device.** `410 Unregistered`, `BadDeviceToken` or
  `ExpiredToken` marks the device inactive; the row stays, so history keeps
  resolving and registering again revives it. A refused *topic* does not: that
  indicts the server's configuration, not the phone.
* **Live Activity tokens are environment-scoped.** A token minted by a debug
  build belongs to `sandbox` and one from a release build to `production`, and
  the server talks to exactly one. Report the environment when you register a
  token; a push to the wrong one is refused before a connection is opened
  rather than failing like a dead token.

---

## Events and history

### `GET /v1/events`

The account's webhook deliveries, newest first. **Session, or a token with
`events:read`.** [Paged](#pagination).

**200 OK**

```json
{
  "events": [
    {
      "id": "0198f3f5-1e33-7174-c2d9-7f0a1b2c3d4e",
      "service_id": "0198f3a1-2b4c-7d8e-9f01-23456789abcd",
      "service_name": "Deploy bot",
      "title": "Deploy bot",
      "body": "Build 4821 succeeded",
      "image_url": null,
      "url": "https://example.com/builds/4821",
      "priority": "normal",
      "status": "accepted",
      "delivered_count": 1,
      "error": null,
      "created_at": "2026-08-09T13:20:11.000Z"
    }
  ],
  "next_cursor": null
}
```

| Field | Notes |
| --- | --- |
| `status` | `processing` (briefly, before the fan-out settles), `no_devices`, `accepted`, `partial`, `failed`. |
| `error` | The provider's own failure text, joined and truncated. This is the one place it appears — it can embed a device token, so it is never returned to the webhook caller. |

### `DELETE /v1/events/{id}`

**Session only.** **204 No Content.**

Deleting a delivery also removes the question it asked: an owner deleting a
notification means "this never happened to me", and leaving an orphaned prompt
behind would contradict that.

### `GET /v1/history`

Everything that has happened to the account, newest first: webhook deliveries,
agent pushes, answered questions and Live Activity changes, in one ordering.
**Session only.** [Paged](#pagination).

| Parameter | Default | Notes |
| --- | --- | --- |
| `kind` | `all` | `all`, `notification`, `response`, `live_activity`. |

**200 OK**

```json
{
  "items": [
    {
      "id": "event:0198f3f5-1e33-7174-c2d9-7f0a1b2c3d4e",
      "kind": "notification",
      "source_name": "Deploy bot",
      "source_image_url": "https://example.com/logo.png",
      "title": "Deploy bot",
      "detail": "Build 4821 succeeded",
      "url": "https://example.com/builds/4821",
      "result": null,
      "status": "accepted",
      "delivered_count": 1,
      "error": null,
      "priority": "normal",
      "created_at": "2026-08-09T13:20:11.000Z"
    }
  ],
  "next_cursor": null
}
```

The four sources have little in common, so most fields are nullable and a client
reads whichever ones its `kind` populates. Two things are worth knowing:

* `id` is `"<source>:<row id>"` — the handle a delete takes, which is why it
  names the source and not just the row. The sources are `event`,
  `notification`, `response` and `live_activity`.
* An answered question is placed by **when it was answered**, not when it was
  asked, because that is where a person looking back expects to find it.
  `result` then reads `approved`, `denied`, `yes`, `no` or `replied`; for a Live
  Activity entry it reads `start`, `update` or `end`.

### `DELETE /v1/history/{id}`

Removes one entry. **Session only.** `{id}` is the composite id above.

**204 No Content**, `404 not_found` for an unknown or unowned entry.

A question still awaiting an answer cannot be deleted: making a live prompt
vanish from a phone by deleting a history row would be a surprising amount of
power for a list view. Answer it, cancel it, or let it expire.

---

## Webhooks

The webhook surface is for systems that can only be handed a URL. Its credential
is the token in the path — that is what a webhook *is* — and everything that
follows from that is deliberate: the token is never written to the access log,
every authentication failure is the same `404 not_found`, and rotating it is one
request away.

Mint a URL with [`POST /v1/services`](#post-v1services).

### `POST /v1/hooks/{token}`

Sends a notification, optionally as a question. Honours
[`Idempotency-Key`](#idempotency).

**Request**

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `body` | string | yes | 1–2000 characters. |
| `title` | string | no | 1–80 characters. Defaults to the service's title. |
| `image_url` | string | no | Public HTTPS URL. Defaults to the service's. |
| `url` | string | no | Tap destination. Defaults to the service's. |
| `priority` | enum | no | Defaults to the service's. |
| `device_ids` | array of id | no | 1–50 entries. |
| `response` | object | no | Turns the notification into a question. |

`response`:

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `kind` | enum | yes | `approval`, `yes_no`, `reply`. |
| `expires_in_seconds` | integer | no | 30 – 86400, default 900. |
| `correlation_id` | string | no | 1–100 characters, echoed back with the answer so a caller can match it to its own work. |
| `callback` | object | no | `{ "url": <public HTTPS URL>, "token": <16–512 characters> }`. The answer is posted there when it arrives, with the token as a bearer credential, so the caller does not have to poll. See [the answer callback](#the-answer-callback). |

Fields the request omits fall back to the service's defaults, so a sender that
can only produce a body still produces a notification with a name, an avatar and
a tap destination.

**201 Created**

```json
{
  "event": {
    "id": "0198f3f5-1e33-7174-c2d9-7f0a1b2c3d4e",
    "status": "accepted",
    "delivered_count": 2,
    "created_at": "2026-08-09T13:20:11.000Z"
  },
  "response": {
    "interaction_id": "0198f3e4-0d22-7063-b1c8-6e9f0a1b2c3d",
    "status": "pending",
    "action": null,
    "text": null,
    "correlation_id": "deploy-4821",
    "expires_at": "2026-08-09T14:15:00.000Z",
    "responded_at": null
  },
  "replayed": false,
  "message": null
}
```

`response` is present only when the request asked a question. The delivery is
always created: `event.status` is what says whether it reached anyone, and
`message` explains a `no_devices` or `failed`. The provider's own error text
stays in the owner's [event log](#get-v1events).

**404 `not_found`** for an unknown or malformed token. **422
`validation_failed`**, **409 `conflict`** for a reused idempotency key, **429
`rate_limited`** as elsewhere.

### The answer callback

This is the push half of the answer contract: the request that asked the
question named a `callback`, and Hark posts the answer there instead of leaving
the caller to poll. It is the one request this server makes *to* you, so it is
documented here as an endpoint you implement.

```http
POST https://ci.example.com/hark
Authorization: Bearer <the token the request supplied>
Content-Type: application/json
User-Agent: Hark-Callbacks/1
```

```json
{
  "type": "interaction.answered",
  "interaction_id": "0198f3e4-0d22-7063-b1c8-6e9f0a1b2c3d",
  "event_id": "0198f3f5-1e33-7174-c2d9-7f0a1b2c3d4e",
  "correlation_id": "deploy-4821",
  "kind": "approval",
  "status": "approved",
  "action": "approve",
  "text": null,
  "responded_at": "2026-08-09T14:03:22.000Z"
}
```

| Field | Notes |
| --- | --- |
| `type` | Always `interaction.answered` today. Branch on it: more may follow. |
| `correlation_id` | Whatever the request supplied, echoed back. `null` when it supplied none. |
| `action` | The choice taken — one of the question's `choices` — or the literal `"reply"` for a reply question. |
| `text` | The reply body for a reply question, `null` for every other kind. |

Rules a receiver can rely on:

* **Only answers are posted.** A question that expired or was canceled produces
  no callback; poll [the event](#get-v1hookstokeneventsevent_id) if you need to
  know about those too.
* **2xx means delivered.** Anything else — including a redirect, which is never
  followed, because a bearer token belongs only to the host it was issued for —
  counts as a failure.
* **Retries are bounded.** Four retries, at 30 s, 2 min, 10 min and 1 h after
  the attempt that failed. Still failing after that, the callback is abandoned
  and the answer remains readable by polling.
* **At-least-once, not exactly-once.** A receiver that answers slowly enough to
  time out (10 s) and then succeeds anyway will be posted to again. Treat
  `interaction_id` as the idempotency key.
* **Nothing about the account or the phone is sent**: not the prompt, not the
  device, not the owner.

### `GET /v1/hooks/{token}/events/{event_id}`

Reports what became of one delivery, and how its question was answered. This is
the polling half of the answer contract, for a caller that cannot receive a
callback.

**200 OK** — `{ "event": { … }, "response": { … } | null }`, the same two
objects as above.

A question whose deadline has passed is expired on read, so a poller sees the
state change without any background job.

### `POST /v1/hooks/{token}/events/{event_id}/cancel`

Withdraws the question a delivery asked. No request body.

**200 OK** — `{ "interaction": { … } }` with `status: "canceled"`.
**404 `not_found`** when there is no question here still awaiting an answer.

Unlike the owner's cancel, this does not require the question to be unexpired: a
service withdrawing its own request should succeed even when the deadline has
just passed. The outcome is the same for the phone either way.

### The webhook Live Activity routes

Identical to the [Live Activity endpoints](#live-activities) — same request
fields, same envelopes, same conflicts — with the service as the requester
instead of an API token. An integration that can only hold a URL drives a card
exactly the way an agent does.

A service can only see and drive the activities it started: keys are unique per
requester, so the same key means different things to different senders.

#### `POST /v1/hooks/{token}/activities`

Starts a service-owned Live Activity. Same request and response as
[`POST /v1/activities`](#post-v1activities). Honours `Idempotency-Key`.

#### `GET /v1/hooks/{token}/activities/{identifier}`

Reads one Live Activity started by this service. Same response as
[`GET /v1/activities/{identifier}`](#get-v1activitiesidentifier).

#### `PATCH /v1/hooks/{token}/activities/{identifier}`

Updates one Live Activity started by this service. Same request and response as
[`PATCH /v1/activities/{identifier}`](#patch-v1activitiesidentifier). Honours
`Idempotency-Key`.

#### `POST /v1/hooks/{token}/activities/{identifier}/end`

Ends one Live Activity started by this service. Same request and response as
[`POST /v1/activities/{identifier}/end`](#post-v1activitiesidentifierend).
Honours `Idempotency-Key`.

---

## Dashboard

The server embeds a small admin UI for the account owner. It is compiled into
the binary — templates, two stylesheets and two small scripts, via
`embed.FS` — and mounted outside `/v1`:

| Method | Path | What it is |
| --- | --- | --- |
| `GET` | `/` | Redirects to `/dashboard` (`302`). |
| `GET` | `/dashboard` | Overview: counts, running Live Activities, recent deliveries. Keeps itself current by polling its own fragment. |
| `GET` | `/dashboard/live/overview` | That fragment: the overview's dynamic half, rendered bare. |
| `GET` | `/dashboard/history` | The full archive, paged, filterable by kind (`?kind=`, `?after=`). |
| `GET` | `/dashboard/login` | Sign-in form. The only page reachable signed out. |
| `POST` | `/dashboard/login` | Signs in and sets the session cookie. |
| `POST` | `/dashboard/logout` | Retires the session and clears the cookie. |
| `GET` | `/dashboard/services` | Webhook services, and the form that creates one. |
| `POST` | `/dashboard/services` | Creates a service. |
| `GET` | `/dashboard/services/{id}` | One service: its webhook URL, defaults, recent deliveries. |
| `POST` | `/dashboard/services/{id}` | Saves the defaults. |
| `POST` | `/dashboard/services/{id}/rotate` | Replaces the webhook credential immediately. |
| `POST` | `/dashboard/services/{id}/delete` | Deletes the service and everything it delivered. |
| `GET` | `/dashboard/devices` | Registered phones. |
| `POST` | `/dashboard/devices/{id}/delete` | Unregisters one. |
| `GET` | `/dashboard/tokens` | API tokens, and the form that mints them. |
| `POST` | `/dashboard/tokens` | Mints a token and shows its secret once. |
| `POST` | `/dashboard/tokens/{id}/revoke` | Revokes one. |
| `GET` | `/dashboard/test` | The test-notification form. |
| `POST` | `/dashboard/test` | Sends one notification and reports what APNs said. |
| `GET` | `/cli/authorize` | The [device-grant approval screen](#get-cliauthorize). |
| `POST` | `/cli/authorize` | Records Approve or Deny, or looks up a typed code. |
| `GET` | `/docs` | This document, [rendered](#get-docs). The one page with no credential. |
| `GET` | `/docs.md` | This contract as raw Markdown. Public. |
| `GET` | `/openapi.json` | The OpenAPI 3.1 contract. Public. |
| `GET` | `/llms.txt` | A compact discovery index for agents. Public. |
| `GET` | `/dashboard/assets/{file}` | The stylesheets and script, at content-hashed URLs. |

**This is not client surface.** Every page answers in HTML, including its
errors — a `404` under `/dashboard` is a rendered page, not the JSON envelope.
A client that finds itself parsing one of these responses is pointed at the
wrong prefix. Anything the dashboard can do, the API can do too, and the API is
where a client should do it. The paths above may change without a `/v1` bump.

### Authentication

The dashboard signs in through the same [session](#session) the API issues: the
`POST /dashboard/login` form calls the same credential service as
[`POST /v1/auth/login`](#post-v1authlogin) and sets the identical cookie. One
session works across both surfaces — sign in on the dashboard and
`GET /v1/auth/session` from the same browser answers `"kind": "session"`.

Every page except sign-in requires that session. An **API token is refused**,
exactly as it is by the endpoints behind
[`session_required`](#error-codes): a token is a credential for an
agent, and this is the surface that mints credentials. A request carrying one
is redirected to the sign-in page rather than served.

Sign-in is rate limited per client and per process, on the same ceilings as
`POST /v1/auth/login`. The buckets are separate, so the two doors each allow
their ceiling rather than sharing one.

### CSRF

Mutating form posts carry a **double-submit token**: a random 32-byte value in
a `HttpOnly`, `SameSite=Lax` cookie (`__Host-hark_csrf` over HTTPS,
`hark_csrf` on plain HTTP), which the server writes into a hidden `csrf_token`
field on every rendered form and compares in constant time on submit. A
mismatch is `403` with a rendered page. The token is reissued on sign-in and
after a rejected submission.

That is the second of two checks. The first is the API's own: a
cookie-authenticated unsafe method whose `Origin` is not this deployment's
public origin is refused with
[`origin_not_allowed`](#error-codes) before it reaches any handler. The
double-submit token exists because that check has one gap — **sign-in**, where
the browser holds no session cookie yet and so nothing triggers the origin
gate. A forged sign-in would log the owner into someone else's account.

Because both checks apply, `HARK_PUBLIC_URL` must be the origin the browser
actually uses. A mismatch (`http://localhost` configured, `http://127.0.0.1` in
the address bar) makes every dashboard form fail with the JSON
`origin_not_allowed` error.

### Responses

* Pages are `Cache-Control: no-store`. They show token prefixes, device
  identifiers, and — once — a token secret. [`/docs`](#get-docs) is the
  exception: it holds nothing about the account, so it is publicly cacheable.
* Assets are served at content-hashed URLs (`app-<digest>.css`) with
  `Cache-Control: public, max-age=31536000, immutable` and an `ETag`.
* Every response carries a content security policy that allows scripts and
  styles from this origin only, plus Google Fonts for the brand typeface, and
  no framing.

### The test notification

`POST /dashboard/test` sends one alert through the same push transport as
[`POST /v1/notifications`](#post-v1notifications), to one device or to all of
them, and reports the accepted count and any provider failures on the page.

It writes **nothing to the history**. A notification row is attributed to the
API token that asked for it, and a session is a person rather than a requester;
this is a diagnostic — "can this account reach that phone" — and its answer
belongs on the page rather than in the account's record. To send a notification
that *is* recorded, mint a token and call the API.

### `GET /cli/authorize`

The [device-grant](#post-v1authdevicecode) approval screen: where a headless
client sends the account owner to be let in. It is a dashboard page in every way
that matters — same session, same CSRF token, same shell — and it sits outside
`/dashboard` only because its URL is one a CLI prints into a terminal for a
person to read out or click.

| Parameter | Notes |
| --- | --- |
| `code` | The `user_code`. Optional: without it the page offers a field to type one into, for an owner who walked to another machine with nothing but the code. |

Signed out, the page redirects to sign-in with the code preserved, so the detour
lands back on the same request rather than on an empty form. An API token is
turned away like any other dashboard page: approval is what mints a token, and a
token that could approve its own successor would stop being a bounded credential.

The page shows the client name, the code, both expiry times and every requested
scope listed individually, then offers **Approve** and **Deny**. Both go through
the same service calls as
[`POST /v1/auth/device/requests/{user_code}/approve`](#post-v1authdevicerequestsuser_codeapprove)
and its `deny` counterpart, so the two surfaces cannot drift. A request that is
no longer awaiting a decision — unknown, already decided, or expired — renders
`409` saying so; an unknown code renders `404` with the field, so the next move
is to try again.

`POST /cli/authorize` takes `code`, `csrf_token` and an optional
`decision` of `approve` or `deny`; without a decision it is the lookup the
field submits. It answers `303` back to this page.

### `GET /docs`

This document, rendered.

**No authentication, and none is even resolved**: the route is mounted outside
the server's credential middleware, so no cookie is read, no `Authorization`
header is honoured and no session expiry is slid forward. A contract that can
only be read by someone who already has an account is a contract nobody can
write a client against.

`docs/api.md` is compiled into the binary and converted to HTML once at
startup, so the page and the build serving it are always the same document.
The response is `text/html; charset=utf-8` with
`Cache-Control: public, max-age=300` and an `ETag`; a conditional `GET` gets
`304`.

The same build publishes three machine-facing representations with the same
public caching and authentication behavior:

| Path | Content type | Use |
| --- | --- | --- |
| `/docs.md` | `text/markdown` | The complete canonical prose and examples without HTML chrome. |
| `/openapi.json` | `application/vnd.oai.openapi+json` | OpenAPI 3.1 operations, security requirements, request schemas and stable error envelope. Hark-specific authorization details use `x-hark-auth` and `x-hark-scopes`. |
| `/llms.txt` | `text/plain` | A small discovery document pointing an agent at the appropriate contract. |

The OpenAPI document and Markdown are complementary: OpenAPI is for discovery,
generation and validation; this contract remains authoritative for behavioral
rules such as delivery semantics, idempotency, callbacks and push payloads.
