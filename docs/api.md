# Hark HTTP API

This document defines the Hark HTTP API, including authentication, request and
response formats, delivery behavior, push payloads, and dashboard routes. It is
the API reference used by the iOS app, web dashboard, and command-line clients.

> Without APNs credentials, Hark still records requests, but `accepted` remains
> `0` and the delivery log reports `ProviderNotConfigured`.

---

## Start here

Hark uses a **session** for the account owner and scoped **API tokens** for CLIs,
applications, and automation. Sign in with the owner account to create an API token,
then use that token for normal API requests.

### Send your first notification

Set the origin of your deployment, then exchange the owner's password for a
session:

```sh
export HARK_URL='http://localhost:8080'

curl --fail-with-body "$HARK_URL/auth/login" \
  --header 'Content-Type: application/json' \
  --data '{"username":"admin","password":"correct horse battery staple"}'
```

Copy the response's `token` value. Use that session to create a narrowly scoped
API token:

```sh
export HARK_SESSION='harksess_…'

curl --fail-with-body "$HARK_URL/tokens" \
  --header "Authorization: Bearer $HARK_SESSION" \
  --header 'Content-Type: application/json' \
  --data '{"name":"quickstart","scopes":["notifications:send"]}'
```

The `secret` in this response is shown only once. Save it and send:

```sh
export HARK_TOKEN='hark_…'

curl --fail-with-body "$HARK_URL/notifications" \
  --header "Authorization: Bearer $HARK_TOKEN" \
  --header 'Content-Type: application/json' \
  --header 'Idempotency-Key: first-notification' \
  --data '{"title":"Hark is ready","body":"Your first API notification arrived."}'
```

A successful request returns `201 Created`, which means Hark recorded and
attempted the notification. Check `notification.accepted_count` and `message` to
see whether APNs accepted it.

### Pick the right entry point

| You are building | Start with | Credential |
| --- | --- | --- |
| An API client, CLI, or CI job | [`POST /notifications`](#post-notifications), then [interactions](#interactions) or [Live Activities](#live-activities) | Scoped API token |
| A service that can only call a URL | [Create a service](#post-services), then call its [`/hooks/{token}`](#post-hookstoken) URL | Webhook token in the URL |
| An iOS client | [Session authentication](#session), then [register the device](#post-devices) | Session |
| A headless client that needs owner approval | [Device authorization](#post-authdevicecode) | Device grant, then API token |

Use the [OpenAPI 3.1 document](/openapi.json) for generated clients and
validation. Text-oriented tools can use the [raw Markdown](/docs.md) or the
[`/llms.txt`](/llms.txt) discovery index. Each build serves matching versions of
all formats.

---

## Conventions

### Base URL

API endpoints are rooted directly at the deployment origin; there is no
version prefix:

```
https://hark.example.com/notifications
```

The readiness probe ([`/healthz`](#get-healthz)), documentation
([`/docs`](#get-docs)), and [dashboard](#dashboard) reserve their own top-level
paths. Dashboard and documentation routes return HTML rather than the API's
JSON responses. Endpoints may gain new JSON fields, so clients must ignore
fields they do not recognize.

### Requests

* Request bodies are JSON. Send `Content-Type: application/json` — a body sent
  with any other media type is rejected with `415 unsupported_media_type`.
* Request bodies are capped at **64 KiB**. A larger body is rejected with
  `413 payload_too_large` before it is read.
* **Unknown request fields are rejected** with `400 bad_request`. Send only the
  fields documented for that endpoint.
* Paths are case-sensitive and have no trailing slash. `/services/` does not
  match `/services`.
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

List endpoints use opaque cursors instead of page numbers. This keeps pagination
stable when new records arrive between requests.

**Request** — both parameters are optional:

| Parameter | Default | Notes |
| --- | --- | --- |
| `limit` | `20` | Maximum items to return, clamped to `1…100`. |
| `cursor` | *(absent)* | The `next_cursor` of the previous page. Omit for the first page. |

**Response** — every paged endpoint returns the same object, with the items
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

List responses do not include a total count.

### Shared vocabulary

These values appear across multiple endpoints. Send the exact lowercase strings
shown below; unknown values are rejected.

| Set | Members |
| --- | --- |
| Priority | `normal`, `time_sensitive`, `critical`. Regular service and API-token request fields accept the first two; [critical service](#critical-services) webhooks accept all three. |
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
is final.

### Request correlation

Every response includes `X-Request-Id`. A request may provide its own value using
up to 128 characters from `[A-Za-z0-9._-]`. Invalid or missing values are
replaced with a generated id. Include this id when reporting a problem.

### Errors

Every endpoint uses the same error format:

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
| 403 | `session_required` | The endpoint requires an owner session. API tokens cannot call it. |
| 403 | `api_token_required` | The endpoint requires an API token. Owner sessions cannot call it. See [Who may send](#who-may-send). |
| 403 | `insufficient_scope` | A valid API token is missing a scope the endpoint declares. The message names every scope the endpoint requires. |
| 403 | `origin_not_allowed` | A cookie-authenticated state-changing request arrived from a foreign origin. |
| 404 | `not_found` | No route matches, or the resource does not exist or is outside the caller's access. Some authenticated routes use the same response for invalid credentials to prevent resource discovery. |
| 405 | `method_not_allowed` | The path exists but not for this method. The `Allow` header lists the supported ones. |
| 409 | `conflict` | The request collides with current state: a duplicate idempotency key used with a different payload, an already-answered question, an already-finished Live Activity. |
| 409 | `token_limit_reached` | The account already holds the maximum number of active API tokens. |
| 409 | `activity_conflict` | A Live Activity already holds the device, or the key, this start asked for. Retry with `replace: true`, or with another key. |
| 409 | `sequence_conflict` | The Live Activity moved on since the sequence the caller read. Re-read it and reapply. |
| 409 | `action_digest_mismatch` | The answer refers to an older version of the interaction. |
| 413 | `payload_too_large` | The request body exceeds 64 KiB. |
| 415 | `unsupported_media_type` | A request body arrived without `Content-Type: application/json`. |
| 422 | `validation_failed` | The body parsed but failed validation. See `fields`. |
| 429 | `rate_limited` | A rate limit was exceeded. `Retry-After` gives the wait in seconds. |
| 500 | `internal_error` | An unhandled server error. The message never contains internal detail; correlate with `X-Request-Id` in the server log. |
| 503 | `service_unavailable` | A dependency the request needs — normally PostgreSQL — is unreachable. |

The device grant adds five more codes — `authorization_pending`, `slow_down`,
`access_denied`, `expired_token` and `invalid_grant` — described with
[`POST /auth/device/token`](#post-authdevicetoken).

---

## Authentication

Hark supports exactly **one account**. Create it with `harkd create-user` or seed
it at startup through environment variables. There is no sign-up endpoint.

### Session

A session is the account owner signed in with their password. It is issued by
[`POST /auth/login`](#post-authlogin) and travels either way:

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

**Lifetime.** A session expires after 30 days without use. Hark refreshes the
expiry at most once per hour and reissues the cookie when needed. Every session
also has a fixed maximum lifetime of 180 days from creation.

### API token

An API token represents a CLI, script, application, or CI job and has a fixed set of
scopes:

```
Authorization: Bearer hark_c2xLm9JbR1tXyLp0aNfCd7eJhSu4WgO7xY2bWvK
```

Tokens are created by the account owner ([`POST /tokens`](#post-tokens))
or issued by the [device grant](#post-authdevicecode). The plaintext is shown
exactly once, at creation, and is never recoverable — the server stores only a
digest.

**API tokens cannot create or modify credentials.** Token management and device
authorization approval require a session. An API token can revoke itself through
[`POST /auth/logout`](#post-authlogout).

#### Scopes

A token carries a subset of these nine scopes. Anything else is rejected.

| Scope | Allows the token to |
| --- | --- |
| `activities:read` | View Live Activities and their delivery state. |
| `activities:write` | Start, update, and end Live Activities. |
| `devices:read` | View registered devices and their capabilities. |
| `events:read` | View webhook notification delivery history and errors. |
| `interactions:create` | Ask questions and cancel pending questions. Asking also requires `notifications:send`, because the question is pushed to a device. |
| `interactions:read` | View questions, their status, and their answers. |
| `notifications:send` | Send one-shot push notifications. |
| `services:read` | View configured webhook services and their defaults. Webhook credentials are redacted for API tokens. |
| `services:write` | Change and delete webhook services and their related history. Deleting one also deletes its deliveries, questions, and Live Activities. Creating a service also creates its webhook credential, so that operation is [session-only](#post-services). |

Scope lists are stored deduplicated and sorted, so a request for
`["services:read","interactions:read","services:read"]` reads back as
`["interactions:read","services:read"]`.

If a token lacks a required scope, the endpoint returns `403
insufficient_scope` and lists the required scopes. Scope checks apply only to API
tokens; owner sessions have full account access.

### How failures are reported

| Situation | Response |
| --- | --- |
| No credential presented on an endpoint that needs one | `401 unauthorized` with `WWW-Authenticate: Bearer realm="hark"` |
| An `Authorization` header that is malformed, or names an unknown, expired, or revoked credential | `401 unauthorized` |
| A **cookie** that is unknown or expired | Treated as no credential and cleared with `Set-Cookie`. The browser can still reach public endpoints and sign in again. |
| A credential cannot be checked because the database is unavailable | `503 service_unavailable` |

### Cross-origin requests

A state-changing request authenticated by the session **cookie** must be
same-origin or it returns `403 origin_not_allowed`. Hark first checks
`Sec-Fetch-Site`. For browsers that do not send it, Hark validates the `Origin`
header against the request host and configured public origin. Requests without
an `Origin` header are allowed. Together with `SameSite=Lax`, this protects the
JSON API from cross-site request forgery without a separate CSRF token.

Requests authenticated through the `Authorization` header do not use this origin
check. Native and command-line clients may omit `Origin`.

### Rate limits

The endpoints an anonymous caller can reach are capped per minute. Each has a
per-client ceiling and a process-wide one twenty times larger:

| Endpoint | Per client | Process-wide |
| --- | --- | --- |
| `POST /auth/login` | 10 | 200 |
| `POST /auth/device/code` | 20 | 400 |
| `POST /auth/device/token` | 120 | 2400 |

Exceeding either gives `429 rate_limited` with `Retry-After` in seconds.

Per-client buckets exist only when the server is configured with the name of a
header its reverse proxy overwrites with the real client address
(`HARK_TRUSTED_CLIENT_IP_HEADER`). Without it there is no per-client bucket at
all, because trusting a client-supplied forwarding header would give each
request a fresh bucket. Deployments behind a controlled edge
should set it.

---

## Delivery

The following rules apply to notifications, questions, Live Activities, and
service webhooks, including [critical services](#critical-services).

### Who may send

A **session** represents the account owner. It can read account data, manage
credentials and devices, and answer questions.

An **API token** represents software. It can perform operations allowed by its
scopes, and it is the only bearer credential that may send. Notifications,
questions, and Live Activities are attributed to the token that created them.
As a result,
[`POST /notifications`](#post-notifications),
[`POST /interactions`](#post-interactions),
and the Live Activity writes return `403 api_token_required` when called with a
session.

A **webhook token** represents a service that sends through
[`/hooks/{token}`](#post-hookstoken) and manages its own Live Activities
using the same request and response formats as API-token clients.

### Choosing devices

Every send targets all reachable devices unless `device_ids` is provided. An id
that is not registered to the account returns `422 validation_failed`.
Registered devices that do not support the requested feature are skipped:

| Send | Needs |
| --- | --- |
| Notification | an active device |
| Question, as a notification | `interaction_schema_version` |
| Live Activity | a push-to-start token, its environment, and `live_activity_schema_version` |
| Question, on the Lock Screen | the above plus `live_activity_interaction_version` |

If no device is reachable, Hark still creates the record. The response has
`accepted: 0` and explains the reason in `message`.

### What `accepted` means

`accepted`, `accepted_count`, and `delivered_count` count messages that **APNs
accepted from Hark**. They do not confirm that a device received or displayed the
message.

When a send fails, Hark stores the APNs error in the account's delivery log
([`GET /events`](#get-events)). It does not return the raw error to the
sender because the text may contain a device token. The response includes a
safer summary in `message`.

A device whose token APNs reports as permanently invalid is marked inactive.
Its row is kept, so history keeps resolving, and registering it again revives
it.

### Idempotency

These endpoints honour an `Idempotency-Key` request header:

* [`POST /notifications`](#post-notifications)
* [`POST /interactions`](#post-interactions)
* [`POST /activities`](#post-activities), [`PATCH`](#patch-activitiesidentifier) and [`POST …/end`](#post-activitiesidentifierend)
* [`POST /hooks/{token}`](#post-hookstoken) and the webhook Live Activity routes

The key must be 1–200 characters and is scoped to the credential that used it.
Hark stores it before sending, so concurrent retries do not create duplicate
pushes.

| Situation | Response |
| --- | --- |
| New key | The normal `201`, with `"replayed": false`. |
| Same key, same body | `200` with the stored outcome and `"replayed": true`. Hark does not send another push. |
| Same key, different body | `409 conflict`. The key is supposed to identify the request. |
| Present but empty, or over 200 characters | `400 bad_request`. |

Hark compares normalized requests after applying defaults, trimming strings,
and sorting `device_ids`. Requests that normalize to the same value are treated
as the same body.

### Delivery limits

Sending also has rolling per-minute limits: 300 requests per credential and
1,500 per account by default. The counters are stored in the database and remain
in effect across restarts. Exceeding either limit returns `429 rate_limited`
with `Retry-After`.

---

## Endpoints

| Method | Path | Auth |
| --- | --- | --- |
| `GET` | [`/healthz`](#get-healthz) | none |
| `GET`·`POST` | [`/` and `/dashboard/…`](#dashboard) | session cookie (HTML, not JSON) |
| `POST` | [`/auth/login`](#post-authlogin) | none |
| `POST` | [`/auth/logout`](#post-authlogout) | session or API token |
| `GET` | [`/auth/session`](#get-authsession) | session or API token |
| `POST` | [`/auth/password`](#post-authpassword) | session |
| `POST` | [`/auth/device/code`](#post-authdevicecode) | none |
| `POST` | [`/auth/device/token`](#post-authdevicetoken) | device code in body |
| `GET` | [`/auth/device/requests/{user_code}`](#get-authdevicerequestsuser_code) | session |
| `POST` | [`/auth/device/requests/{user_code}/approve`](#post-authdevicerequestsuser_codeapprove) | session |
| `POST` | [`/auth/device/requests/{user_code}/deny`](#post-authdevicerequestsuser_codedeny) | session |
| `GET` | [`/tokens`](#get-tokens) | session |
| `POST` | [`/tokens`](#post-tokens) | session |
| `DELETE` | [`/tokens/{id}`](#delete-tokensid) | session |
| `GET` | [`/services`](#get-services) | session · token `services:read` |
| `POST` | [`/services`](#post-services) | session |
| `GET` | [`/services/{id}`](#get-servicesid) | session · token `services:read` |
| `PATCH` | [`/services/{id}`](#patch-servicesid) | session · token `services:write` |
| `DELETE` | [`/services/{id}`](#delete-servicesid) | session · token `services:write` |
| `POST` | [`/services/{id}/webhook-token`](#post-servicesidwebhook-token) | session |
| `GET` | [`/devices`](#get-devices) | session · token `devices:read` |
| `POST` | [`/devices`](#post-devices) | session |
| `GET` | [`/devices/{id}`](#get-devicesid) | session · token `devices:read` |
| `DELETE` | [`/devices/{id}`](#delete-devicesid) | session |
| `PUT` | [`/devices/{id}/push-to-start-token`](#put-devicesidpush-to-start-token) | session |
| `PUT` | [`/devices/{id}/activity-update-token`](#put-devicesidactivity-update-token) | session |
| `PUT` | [`/activity-deliveries/{id}/update-token`](#put-activity-deliveriesidupdate-token) | single-use credential in body |
| `POST` | [`/notifications`](#post-notifications) | token `notifications:send` |
| `GET` | [`/critical-services`](#get-critical-services) | session · token `services:read` |
| `POST` | [`/critical-services`](#post-critical-services) | session |
| `GET` | [`/critical-services/{id}`](#get-critical-servicesid) | session · token `services:read` |
| `PATCH` | [`/critical-services/{id}`](#patch-critical-servicesid) | session · token `services:write` |
| `DELETE` | [`/critical-services/{id}`](#delete-critical-servicesid) | session · token `services:write` |
| `POST` | [`/critical-services/{id}/webhook-token`](#post-critical-servicesidwebhook-token) | session |
| `GET` | [`/critical-settings`](#get-critical-settings) | session |
| `PATCH` | [`/critical-settings`](#patch-critical-settings) | session |
| `POST` | [`/interactions`](#post-interactions) | token `interactions:create` + `notifications:send` |
| `GET` | [`/interactions`](#get-interactions) | session · token `interactions:read` |
| `GET` | [`/interactions/{id}`](#get-interactionsid) | session · token `interactions:read` |
| `POST` | [`/interactions/{id}/response`](#post-interactionsidresponse) | session · credential in body |
| `POST` | [`/interactions/{id}/cancel`](#post-interactionsidcancel) | session · token `interactions:create` |
| `GET` | [`/activities`](#get-activities) | session · token `activities:read` |
| `POST` | [`/activities`](#post-activities) | token `activities:write` |
| `GET` | [`/activities/{identifier}`](#get-activitiesidentifier) | session · token `activities:read` |
| `PATCH` | [`/activities/{identifier}`](#patch-activitiesidentifier) | token `activities:write` |
| `POST` | [`/activities/{identifier}/end`](#post-activitiesidentifierend) | token `activities:write` |
| `GET` | [`/events`](#get-events) | session · token `events:read` |
| `DELETE` | [`/events/{id}`](#delete-eventsid) | session |
| `GET` | [`/history`](#get-history) | session |
| `GET` | [`/history/sources`](#get-historysources) | session |
| `DELETE` | [`/history`](#delete-history) | session |
| `DELETE` | [`/history/{id}`](#delete-historyid) | session |
| `POST` | [`/hooks/{token}`](#post-hookstoken) | webhook token in path |
| `GET` | [`/hooks/{token}/events/{event_id}`](#get-hookstokeneventsevent_id) | webhook token in path |
| `POST` | [`/hooks/{token}/events/{event_id}/cancel`](#post-hookstokeneventsevent_idcancel) | webhook token in path |
| `POST` | [`/hooks/{token}/activities`](#the-webhook-live-activity-routes) | webhook token in path |
| `GET` | [`/hooks/{token}/activities/{identifier}`](#the-webhook-live-activity-routes) | webhook token in path |
| `PATCH` | [`/hooks/{token}/activities/{identifier}`](#the-webhook-live-activity-routes) | webhook token in path |
| `POST` | [`/hooks/{token}/activities/{identifier}/end`](#the-webhook-live-activity-routes) | webhook token in path |

Hark makes outbound HTTP requests only for [answer callbacks](#the-answer-callback).

### `GET /healthz`

Readiness probe. No authentication.

The endpoint runs a database query with a two-second timeout. A `200` response
means the instance is ready for traffic. A `503` response means it is not ready.

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

**503 Service Unavailable** — the database is unreachable. Hark logs the driver
error but does not include it in the response.

```json
{
  "error": {
    "code": "service_unavailable",
    "message": "The database is unreachable."
  }
}
```

---

### `POST /auth/login`

Exchanges the account's username and password for a session. **No
authentication.** Rate limited to 10 attempts per minute per client.

The username is matched case-insensitively; the password is not.

**Request**

```http
POST /auth/login
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
| 401 | `unauthorized` | Unknown username, no password set, or wrong password. The response does not distinguish among them. |
| 429 | `rate_limited` | Too many attempts. See `Retry-After`. |

---

### `POST /auth/logout`

Invalidates the credential presented by the caller. **Session or API token.**

* A session is deleted and its cookie expired.
* An API token is revoked; the next request carrying it gets `401`.

The operation is idempotent. Revoking a credential that is already inactive
still succeeds.

No request body.

**204 No Content** — always, on success.

---

### `GET /auth/session`

Describes the current authenticated user and credential. **Session or API
token.** Never cached
(`Cache-Control: no-store`).

Use this endpoint to validate a credential and inspect its type and permissions.

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

### `POST /auth/password`

Changes the account's password. **Session only.** API tokens cannot change the
account password.

Changing the password signs out every other session but keeps the current
session active. API tokens are unaffected and must be revoked separately if
needed.

**Request**

```http
POST /auth/password
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

### `POST /auth/device/code`

Starts device authorization for a headless client that needs an API token without
handling the owner's password. **No authentication.** Rate limited to 20 requests
per minute per client.

The flow follows OAuth 2.0 Device Authorization Grant semantics (RFC 8628) while
using Hark's JSON naming, error format, and HTTP status codes. Device-flow
errors use the RFC vocabulary in `error.code`.

**Request**

```http
POST /auth/device/code
Content-Type: application/json

{
  "client_name": "harkctl",
  "scopes": ["notifications:send", "interactions:create", "interactions:read"],
  "token_expires_in_seconds": 7776000
}
```

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `client_name` | string | yes | 1–80 characters, trimmed. Shown to the owner on the approval screen and used as the issued token's `name`. |
| `scopes` | array of scope | yes | 1–10 known scopes. Shown to the owner, deduplicated, and sorted. |
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
| `device_code` | Secret used by the client to poll. **Do not display or log it.** |
| `user_code` | Short code to display to the owner, in canonical `XXXX-XXXX` form. |
| `verification_uri` | Page where the owner enters the code. |
| `verification_uri_complete` | The same page with the code filled in; open this in a browser when one is available. |
| `expires_in_seconds` | Always 600. The request is approvable for ten minutes. |
| `interval_seconds` | Minimum delay between polls. Faster polling increases the required delay. |

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

### `POST /auth/device/token`

Polls a device-authorization request. **The device code in the body is the
credential.** Rate limited to 120 per minute per client.

**Request**

```http
POST /auth/device/token
Content-Type: application/json

{ "device_code": "harkdev_kQ2mZ8bR1tXyLp0aNfCd7eJhSu4WgO7xY2bWv" }
```

**200 OK** — approved, and this poll created the token:

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

`access_token` is returned exactly once and cannot be recovered. Each device
authorization request can issue only one token.

**Every other outcome**

| Status | `code` | Meaning | Retry? |
| --- | --- | --- | --- |
| 400 | `authorization_pending` | The request is still awaiting a decision. | Yes — after `Retry-After`. |
| 429 | `slow_down` | You polled faster than the interval; it has been raised (by 5 s each time, to a 30 s ceiling) and never lowered. | Yes — after `Retry-After`. |
| 403 | `access_denied` | The owner denied the request. | No. Start over. |
| 410 | `expired_token` | The request was not approved or denied within ten minutes. | No. Start over. |
| 409 | `invalid_grant` | This request already issued its token. | No. |
| 409 | `token_limit_reached` | The account holds the maximum number of active tokens, so the approval was cancelled. | No. Revoke a token, then start over. |
| 404 | `not_found` | The device code is unknown or malformed. | No. |

**Retry only when the response includes `Retry-After`.**

---

### `GET /auth/device/requests/{user_code}`

Describes a pairing request so an approval screen can render it. **Session
only.** Never cached.

`{user_code}` is the code entered by the owner. Matching ignores case, spaces,
and hyphens.

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
| `token_expires_at` | When the resulting token will expire. Approval screens should display this value. |

The device code is never exposed here.

A request whose time has passed is reported as `expired` on read, so a screen
polling this endpoint sees the state change without any background job.

**404 `not_found`** — no request matches that code (including a code that is
not well formed).

`verification_uri` points to the dashboard's
[`/cli/authorize`](#get-cliauthorize) page. The page shows the client name, user
code, expiry times, and requested scopes, then allows the owner to approve or
deny the request. Native clients can use this JSON endpoint to build an
equivalent approval screen.

---

### `POST /auth/device/requests/{user_code}/approve`

Approves a pairing request. **Session only** — approval creates a token, so an
API token cannot approve another token.

No request body. The next poll of the matching device code creates the token.

**200 OK** — the same response object as the `GET`, with `status` now `approved`.

**Errors**

| Status | `code` | When |
| --- | --- | --- |
| 403 | `session_required` | Called with an API token. |
| 409 | `conflict` | The request is unknown, already decided, or expired. |

---

### `POST /auth/device/requests/{user_code}/deny`

Refuses a pairing request. **Session only.** No request body.

**200 OK** — the same response object, with `status` now `denied`. The polling client
receives `403 access_denied`.

Same errors as `approve`.

---

### `GET /tokens`

Lists the account's API tokens, newest first. **Session only.**

Revoked and expired tokens remain in the list for auditing.

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
| `prefix` | string | First 13 characters of the secret, used to identify the token in logs. It is not a usable credential. |
| `expires_at` | string \| null | `null` means the token never expires. |
| `last_used_at` | string \| null | Stamped at most once a minute per token, so it is accurate to within a minute and no more. |
| `revoked_at` | string \| null | Non-null means the token has been revoked. |

The secret does not appear in this or any later response.

---

### `POST /tokens`

Creates an API token. **Session only.**

**Request**

```http
POST /tokens
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
| `scopes` | array of scope | yes | 1–10 known scopes; stored deduplicated and sorted. |
| `expires_in_seconds` | integer \| null | no | 3600 – 31 536 000. Absent or `null` creates a token that never expires. |

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

**`secret` is shown exactly once.** If it is lost, create a new token.

**Errors**

| Status | `code` | When |
| --- | --- | --- |
| 403 | `session_required` | Called with an API token. |
| 409 | `token_limit_reached` | The account already holds 25 active tokens. Revoke one first. |
| 422 | `validation_failed` | `name`, `scopes` or `expires_in_seconds` are unusable. `fields` names which. |

"Active" means not revoked and not expired, so revoking or letting a token
lapse frees a slot.

---

### `DELETE /tokens/{id}`

Revokes one of the account's tokens. **Session only.**

Revocation is immediate: the next request carrying that token gets `401`. The
row is kept, so resources created by the token retain their attribution.

**204 No Content** on success.

**404 `not_found`** — the id is unknown or the token is already revoked.

To revoke the token you are *currently using*, call
[`POST /auth/logout`](#post-authlogout) with it instead — that needs no
session.

---

## Services

A **service** is a named webhook source with notification defaults and a secret
token embedded in its webhook URL. Use services for systems that can send HTTP
requests but cannot manage an Authorization header.

### `GET /services`

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
      "webhook_url": "https://hark.example.com/hooks/harkhook_kQ2mZ8bR1tXyLp0aNfCd7eJhSu4WgO7xY2bWv",
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
| `webhook_url` | The full webhook URL, including its credential. **Available only to sessions.** API tokens receive `null` so a read credential cannot be converted into a send credential. |

### `POST /services`

Creates a service and its webhook credential. **Session only.** API tokens cannot
create webhook credentials.

**Request**

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `title` | string | yes | 1–80 characters. The default sender name. |
| `image_url` | string \| null | no | Public HTTPS URL, ≤2048 characters. |
| `url` | string \| null | no | Tap destination: any scheme except `about:`, `blob:`, `data:`, `file:` and `javascript:`. |
| `priority` | enum | no | `normal` (default) or `time_sensitive`. Critical is available only through a [critical service](#critical-services) webhook. |

**201 Created**

```json
{
  "service": { "…as above…" },
  "webhook_url": "https://hark.example.com/hooks/harkhook_kQ2mZ8bR1tXyLp0aNfCd7eJhSu4WgO7xY2bWv"
}
```

The URL contains a secret credential. Hark stores it encrypted and returns the
decrypted URL only to owner sessions. API tokens receive `null` for this field.

**422 `validation_failed`** names the offending field.

### `GET /services/{id}`

One service. **Session, or a token with `services:read`.** `404 not_found` when
it does not exist.

### `PATCH /services/{id}`

Changes a service's defaults. **Session, or a token with `services:write`.**

Only the fields the request names are written. `null` clears `image_url` or
`url`; `title` and `priority` cannot be cleared. At least one field is required.

```http
PATCH /services/0198f3a1-2b4c-7d8e-9f01-23456789abcd
Content-Type: application/json

{ "title": "Deploys", "image_url": null }
```

**200 OK** — `{ "service": { … } }`.

### `POST /services/{id}/webhook-token`

Rotates the credential. **Session only.**

No request body. **201 Created**, with the same response object as service
creation: the new URL and the service.

The previous URL stops working immediately. There is no grace period.

### `DELETE /services/{id}`

**Session, or a token with `services:write`.** **204 No Content.**

Deleting a service also deletes its deliveries, questions, and Live Activities.

---

## Devices

A **device** is one iOS installation identified by its APNs token. A reinstall or
token change may create a new device row. The previous row remains until APNs
reports its token as invalid.

### `GET /devices`

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

### `POST /devices`

Registers or refreshes an iOS device. **Session only.**

**Request**

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `apns_token` | string | yes | 32–400 hexadecimal characters, case-insensitive. |
| `name` | string \| null | no | ≤80 characters, for the device list. |
| `interaction_schema_version` | integer \| null | no | `1` if this build can answer a question from a notification. |
| `live_activity_interaction_version` | integer \| null | no | `1` if it can answer from the Lock Screen. |

**201 Created** — `{ "device": { … } }`.

Registration behavior:

* **Omission clears.** Each request replaces the device's supported-feature
  state. Omitted capabilities are cleared.
* **The token is the identity.** Registering the same token twice updates one
  row; registering a reissued token creates a second. The response's `id` is
  required by other device endpoints.

The first registered device receives a short welcome sequence. If APNs rejects
that push as undeliverable, the response sets `active` to `false`.

### `GET /devices/{id}`

One device. **Session, or a token with `devices:read`.**

### `DELETE /devices/{id}`

Unregisters a phone. **Session only.** **204 No Content**, `404 not_found` for
an unknown id.

Deleting a device also deletes its Live Activity delivery records without
sending an end push. A Live Activity may therefore remain visible on the removed
device until iOS dismisses it.

### `PUT /devices/{id}/push-to-start-token`

Records the ActivityKit push-to-start token required to start Live Activities
on a device. **Session only.**

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `token` | string | yes | 32–512 hexadecimal characters. |
| `environment` | enum | yes | `sandbox` or `production`. A token issued for one environment is ignored by the other, so the environment is stored with the token. |
| `schema_version` | integer | no | The content-state version this build understands. Must be `1`. |

**204 No Content.** `404 not_found` when the device is unknown or inactive.

This operation replaces the stored token and is idempotent.

### `PUT /devices/{id}/activity-update-token`

Reports the per-activity update token for a Live Activity that was started by
push. **Session only.**

Hark cannot update or end an activity until the device reports this token. When
`activity_id` is absent, Hark first looks for a delivery with the same
`native_activity_id`, then considers deliveries on the device that are still
waiting for a token.

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
| 409 | `conflict` | More than one delivery could match. Retry after the other start has resolved. |

### `PUT /activity-deliveries/{id}/update-token`

Registers an update token without a session. **The single-use registration
token in the body authenticates the request.**

The start payload gives the widget extension a `token_registration_url` and
`registration_token`. The token authorizes registration only for that delivery
and expires with the activity.

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `registration_token` | string | yes | The single-use credential from the start push. |
| `native_activity_id` | string | no | ActivityKit's `activity.id`. |
| `update_token` | string | yes | 32–512 hexadecimal characters. |

**204 No Content.**

**404 `not_found`** — the delivery is unknown or finished, or the registration
credential is invalid. These cases share one response to prevent delivery
discovery.

---

## Notifications

### `POST /notifications`

Sends a one-shot push. **API token with `notifications:send`.** Supports
[`Idempotency-Key`](#idempotency).

**Request**

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `body` | string | yes | 1–2000 characters. |
| `title` | string | no | 1–80 characters. Defaults to `"Hark"`; it is shown as the sender. |
| `image_url` | string | no | Public HTTPS URL. |
| `url` | string | no | Tap destination. |
| `priority` | enum | no | `normal` (default) or `time_sensitive`. Critical is available only through a [critical service](#critical-services) webhook. |
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

`message` is non-null when no device is registered or no delivery was accepted.
See [what `accepted` means](#what-accepted-means).

**Errors** — `403 api_token_required` for a session, `409 conflict` for a reused
idempotency key, `422 validation_failed`, `429 rate_limited`.

---

## Critical services

Critical services are ordinary webhook services in a separate management flow.
They have the same title, avatar, tap destination, webhook URL, token rotation,
device targeting, questions and callbacks, Live Activity routes, idempotency,
delivery limits, and history as services created through `/services`.

The one capability difference is priority:

| Service kind | Allowed default and webhook priorities |
| --- | --- |
| Regular service | `normal`, `time_sensitive` |
| Critical service | `normal`, `time_sensitive`, `critical` |

New critical services default to `normal`. Normal and Time Sensitive are always
delivered as requested. A request that resolves to `critical` is delivered as
Critical only when both `critical_alerts_enabled` for the account and
`critical_enabled` for the service are true. If either switch is off, that
request falls back to `time_sensitive`; neither switch changes Normal or Time
Sensitive requests.

The iOS target declares Apple's Critical Alerts entitlement, and the user grants
Critical Alerts permission in the app.

### `GET /critical-services`

Lists the account's critical services, newest first. **Session, or a token with
`services:read`.** Not paged.

**200 OK**

```json
{
  "services": [
    {
      "id": "0198f3a1-2b4c-7d8e-9f01-23456789abcd",
      "title": "Home Assistant",
      "image_url": "https://home.example.com/avatar.png",
      "url": "homeassistant://dashboard-security",
      "priority": "normal",
      "critical_enabled": true,
      "webhook_url": "https://hark.example.com/hooks/harkhook_kQ2mZ8bR1tXyLp0aNfCd7eJhSu4WgO7xY2bWv",
      "created_at": "2026-08-20T08:00:00.000Z",
      "updated_at": "2026-08-21T09:30:00.000Z"
    }
  ]
}
```

`webhook_url` contains the service's send credential. Sessions receive it; API
tokens receive `null`, matching regular service reads.

### `POST /critical-services`

Creates a critical service and its webhook credential. **Session only.**

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `title` | string | yes | 1–80 characters. Default sender title. |
| `image_url` | string or null | no | Public HTTPS avatar URL. |
| `url` | string or null | no | Web URL or app deep link opened when tapped. |
| `priority` | enum | no | `normal` (default), `time_sensitive`, or `critical`. |
| `critical_enabled` | boolean | no | Per-service Critical switch; defaults to `true`. It gates only Critical priority. |

**201 Created**

```json
{
  "service": { "…as above…" },
  "webhook_url": "https://hark.example.com/hooks/harkhook_kQ2mZ8bR1tXyLp0aNfCd7eJhSu4WgO7xY2bWv"
}
```

Use the returned URL exactly like a regular service's
[`/hooks/{token}`](#post-hookstoken) URL.

### `GET /critical-services/{id}`

Returns one critical service. **Session, or a token with `services:read`.**
**200 OK** —
`{ "service": { … } }`; `404 not_found` when it does not exist. Critical
services are intentionally absent from the regular `/services` management
endpoints, and regular services are absent here.

### `PATCH /critical-services/{id}`

Changes a critical service's defaults or per-service switch. **Session, or a
token with `services:write`.** At least one field is required. Send `null` to
clear `image_url` or `url`.

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `title` | string | no | 1–80 characters. |
| `image_url` | string or null | no | Public HTTPS avatar URL; `null` clears it. |
| `url` | string or null | no | Web URL or app deep link; `null` clears it. |
| `priority` | enum | no | `normal`, `time_sensitive`, or `critical`. |
| `critical_enabled` | boolean | no | Gates only requests that resolve to Critical. |

**200 OK** — `{ "service": { … } }`.

### `DELETE /critical-services/{id}`

Deletes the service and the same dependent delivery records deleted for a
regular service. **Session, or a token with `services:write`.** **204 No
Content**; `404 not_found` for an unknown id.

### `POST /critical-services/{id}/webhook-token`

Rotates the service's webhook credential. **Session only.** The previous URL
stops working immediately. **201 Created** returns the service and new
`webhook_url`, in the same shape as creation.

### `GET /critical-settings`

Returns the account-wide Critical switch. **Session only.**

```json
{ "critical_alerts_enabled": true }
```

### `PATCH /critical-settings`

Writes the account-wide switch. **Session only.**

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `critical_alerts_enabled` | boolean | yes | Gates only Critical priority. Normal and Time Sensitive are unchanged. |

**200 OK** — the same object as the `GET`, with the new value.

---

## Interactions

An **interaction** is a question sent to a device. The requester can read the
answer by polling or receive it through a callback.

Its `kind` decides what may be answered — `approval` → `approve`/`deny`,
`yes_no` → `yes`/`no`, `reply` → `reply` with text — and its `presentation`
decides where it appears: as a notification with action buttons, or as a card on
the Lock Screen that can be answered without unlocking.

Every status other than `pending` is final.

### `POST /interactions`

Asks a question. **API token with both `interactions:create` and
`notifications:send`** — it sends a push and records an interaction whose answer
the sender can read. Supports [`Idempotency-Key`](#idempotency).

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
| `priority` | enum | no | `normal` (default) or `time_sensitive`. Critical is available only through a [critical service](#critical-services) webhook. |
| `device_ids` | array of id | no | 1–50 entries. |
| `expires_in_seconds` | integer | no | 30 – 86400, default 900. A Lock Screen card is additionally capped at 28800, the eight hours iOS allows. |

Every interaction has an expiration time so callers know when to stop waiting.

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
| `action_digest` | Binds an answer to this version of the question. A stale prompt cannot answer a newer version. |
| `activity_id` | The Live Activity presenting the question, when one could be started. `null` means it went out as a notification instead — including when `presentation` was `live_activity` but no device could show one, which `message` then says. |

**`presentation: live_activity` falls back to a notification.** If no device can
show an interactive Live Activity, Hark sends a standard question notification
with the same actions and response token. In that case `activity_id` is `null`
and `message` explains the fallback. If no device can present either format,
`accepted` is `0`.

**422 `validation_failed`** covers cross-field rules. A Live Activity question
supports two buttons, no free-text reply or tap URL, and a maximum lifetime of
eight hours.

### `GET /interactions`

The inbox. **Session, or a token with `interactions:read`.**
[Paged](#pagination).

A session lists all interactions on the account. An API token lists only
interactions created by that token.

| Parameter | Default | Notes |
| --- | --- | --- |
| `status` | `pending` | `pending` — still awaiting an answer and not past its deadline. `all` — every question, newest first. |

**200 OK** — `{ "interactions": [ … ], "next_cursor": null }`, where each item is
an interaction plus:

| Field | Notes |
| --- | --- |
| `source_name` | Who is asking: the service title, else the token name, else the question's own title. |
| `source_image_url` | The question's image, else the service's. |

The default `pending` filter excludes past-deadline interactions without changing
their stored status.

### `GET /interactions/{id}`

One question. **Session, or a token with `interactions:read`.**

A session can read any interaction on the account. An API token can read only
interactions created by that token. Other interactions and unknown ids return
the same `404 not_found` response.

| Parameter | Default | Notes |
| --- | --- | --- |
| `wait_seconds` | `0` | 0–25. Hold the request open until the question is answered, or until the wait runs out. |

Use `wait_seconds` for long polling. The request returns when the interaction is
answered or the wait expires. A timeout still returns `200` with the current
interaction, which may remain `pending`.

**200 OK** — `{ "interaction": { … } }`.

### `POST /interactions/{id}/response`

Answers a question. **A session, or the `response_token` from the push
payload.**

The app can answer with its session. Notification and widget extensions answer
with the single-use `response_token` included in the push payload.

**API tokens cannot answer interactions.** Allowing the requesting token to
answer would bypass owner approval. API-token requests return `403
session_required`. The requester never receives the plaintext `response_token`;
it appears only in the device push payload.

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `action` | string | yes | One of the question's `choices`. |
| `text` | string | for `reply` | 1–4000 characters. |
| `device_id` | id | yes | The phone answering. Must be active and on the account. |
| `action_digest` | string | yes | The digest the phone was shown. |
| `response_token` | string | no | The credential from the push. Omit when the request carries a session. |

**200 OK** — `{ "interaction": { … } }` with the new status.

Repeating the same action from the same device is idempotent and returns `200`.

| Status | `code` | When |
| --- | --- | --- |
| 401 | `unauthorized` | No credential at all, and no `response_token`. |
| 403 | `session_required` | An API token was used without a `response_token`. API tokens cannot answer interactions they create. |
| 404 | `not_found` | Unknown question, unknown device, or a `response_token` that does not match. The three are one answer. |
| 409 | `action_digest_mismatch` | The digest does not match the stored question. |
| 409 | `conflict` | The question has already been settled differently. |
| 422 | `validation_failed` | The action is not one this question offers, or a reply carried no text. |

### `POST /interactions/{id}/cancel`

Withdraws a question. **Session, or a token with `interactions:create`.** No
request body.

A session can cancel any pending interaction on the account. An API token can
cancel only interactions created by that token. Other interactions and unknown
ids return `404 not_found`.

**200 OK** — `{ "interaction": { … } }` with `status: "canceled"`.
`409 conflict` when it is no longer pending; `404 not_found` when there is no
such question within the caller's reach.

---

## Live Activities

A **Live Activity** is an updatable Lock Screen card, such as a deployment,
test run, or pending question.

Two iOS constraints apply:

* **A phone shows one ordinary activity at a time.** A start uses the available
  slot, returns `409 activity_conflict`, or replaces the current activity. An
  activity that presents a question may appear alongside the ordinary activity.
* **An activity can run for at most eight hours.** iOS removes it after that
  limit. Hark reports an overdue activity as `expired` on the next read.

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
  "accent_color": "#E64949",
  "style": "standard"
}
```

| Field | Notes |
| --- | --- |
| `title` | 1–80 characters. |
| `status` | 1–60 characters. Current activity status. |
| `detail` | 1–240 characters. **Omitted** rather than set to `null` when absent. |
| `progress` | 0.0–1.0. Same omission rule. |
| `symbol` | `terminal` (default), `code`, `build`, `success`, `warning`. |
| `privacy_mode` | `standard` (default) or `private`. `private` redacts the banner announcing the start; the state itself always carries the real values, and the widget decides what to show. |
| `accent_color` | `#RRGGBB`, default `#E64949`. |
| `style` | `standard` (default), `ring`, `hero`, `terminal`, `steps`. The four interactive styles belong to questions and are refused here. |
| `interaction` | Present only on a card that presents a question: its id, kind, prompt, the two button labels and the actions they post, and the answer once there is one. |

### `GET /activities`

The account's activities, newest first. **Session, or a token with
`activities:read`.** [Paged](#pagination).

| Parameter | Default | Notes |
| --- | --- | --- |
| `status` | `live` | `live` — what is on a Lock Screen right now. `all` — including finished ones. |

**200 OK** — `{ "activities": [ … ], "next_cursor": null }`, each item an
activity plus `source_name` and `source_image_url`.

Cards presenting a question are not listed: they are shown as the question.

### `POST /activities`

Starts an activity. **API token with `activities:write`.** Supports
[`Idempotency-Key`](#idempotency).

**Request**

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `key` | string | no | 1–100 characters. A stable handle for this run, reusable once it ends. |
| `replace` | boolean | no | End activities that conflict on device slot or key. Default `false`. |
| `title` | string | yes | 1–80 characters. |
| `status` | string | yes | 1–60 characters. |
| `detail` | string | no | 1–240 characters. |
| `progress` | number | no | 0.0–1.0. |
| `symbol` | enum | no | Default `terminal`. |
| `privacy_mode` | enum | no | Default `standard`. |
| `accent_color` | string | no | `#RRGGBB`, default `#E64949`. |
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
| `sequence` | The optimistic-concurrency token. Send it back as `if_sequence` to require that no update has occurred since the read. |
| `accepted_count` / `failed_count` | The result of the **most recent** operation, not a lifetime total. |
| `replaced` | Present only when the request set `replace`, and counts the activities that were ended to make room. |
| `message` | Unique reasons devices were not reached, such as `MissingPushToStartToken`, `InteractionTerminal`, or an APNs error. |

**409 `activity_conflict`** when a device slot or key is already in use. The
response includes the blocking activity id only when it belongs to the same
requester.

### `GET /activities/{identifier}`

One activity, by id or by key. **Session, or a token with `activities:read`.**
**200 OK** — `{ "activity": { … } }`.

Because a key can be reused, the lookup prefers a running activity and then the
newest.

### `PATCH /activities/{identifier}`

Changes a running activity and pushes the change. **API token with
`activities:write`.** Supports [`Idempotency-Key`](#idempotency).

Every field is optional; at least one is required. `detail` and `progress`
accept `null`, which removes them.

| Field | Type | Notes |
| --- | --- | --- |
| `title`, `status`, `detail`, `progress`, `symbol`, `privacy_mode`, `accent_color`, `style` | — | As in the start. |
| `stale_after_seconds` | integer | Restarts the staleness window. If omitted, the existing duration is measured again from the update time. |
| `if_sequence` | integer | Apply only if the activity is still at this sequence. |

**200 OK** — the same response object as the start, without `replaced`.

An update that reaches no device still returns `200` because the state change is
recorded. `message` explains the delivery failure. `MissingUpdateToken` means the
device has not yet reported its per-activity update token.

| Status | `code` | When |
| --- | --- | --- |
| 404 | `not_found` | No running or finished activity of this requester matches. |
| 409 | `conflict` | It has already finished. |
| 409 | `sequence_conflict` | `if_sequence` did not match, including when the activity changed between the read and write. |

### `POST /activities/{identifier}/end`

Finishes an activity and pushes the final state. **API token with
`activities:write`.** Supports [`Idempotency-Key`](#idempotency).

| Field | Type | Notes |
| --- | --- | --- |
| `status` | string | 1–60 characters, default `"Complete"`. |
| `detail`, `progress` | — | As in the update, `null` to remove. |
| `symbol` | enum | Default `success`. |
| `accent_color` | string | Omitting it keeps the current colour. |
| `dismiss_after_seconds` | integer | 0 – 14400, default 0. How long the finished card stays on screen. |
| `if_sequence` | integer | As above. |

**200 OK** — the update response, with the activity now `ended`.

Ending an activity records and pushes a final state. The activity remains in
history, so this operation uses `POST` with a body rather than `DELETE`.

---

## Push payloads

This section defines the APNs payloads sent to the iOS app, notification service
extension, and widget extension. These payloads are not HTTP API responses.

The payload combines Apple-defined keys with Hark-defined data:

* **`aps` keys are defined by Apple.** iOS decodes keys such as
  `mutable-content`, `interruption-level`, `content-state`, and
  `input-push-token`.
* **Hark data** uses `snake_case`, RFC 3339 timestamps, and UUIDv7 ids.
  Notification data is under the top-level `hark` key. Live Activity data is in
  `aps.attributes` and `aps.content-state`, as required by ActivityKit.

Every Hark object carries `schema_version`, currently `1`. A device announces
the versions it understands when it registers
([`POST /devices`](#post-devices),
[`PUT …/push-to-start-token`](#put-devicesidpush-to-start-token)), and a
a device with a different version is [excluded from the
send](#choosing-devices). Adding a field does not change the schema version, so
clients must ignore unknown response fields.

### Notification payload

Used for webhook events, API notifications, and welcome notifications. Hark
sends one payload per device.

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
| `mutable-content` | Always `1`. The notification-service extension must run to download `source.image_url` and apply the communication notification style. |
| `interruption-level` | **Omitted** for an ordinary notification, which iOS reads as its default level. Otherwise `"time-sensitive"` or `"critical"`. |
| `category` | Present only on a question. See below. |
| `thread-id` | The conversation, same string as `hark.thread_key`. |
| `badge` | **Never sent.** The app calculates its badge from pending interactions. |

Priority maps as follows. The `apns-priority` header is always `10`:

| API `priority` | `aps.sound` | `aps.interruption-level` |
| --- | --- | --- |
| `normal` | `"default"` | *(omitted)* |
| `time_sensitive` | `"default"` | `"time-sensitive"` |
| `critical` | `{"critical": 1, "name": "default", "volume": 1}` | `"critical"` |

[Critical service](#critical-services) webhooks can produce `critical`. iOS
requires Apple's Critical Alerts entitlement and the user's permission to
deliver at that level. Hark declares the entitlement on its app target.

**`hark`**

| Field | Presence | Notes |
| --- | --- | --- |
| `schema_version` | always | `1`. |
| `device_id` | always | The destination device. Extensions use it when answering an interaction. |
| `record_id` | always | The related event, notification, or interaction id used to open the history entry. Welcome notifications use a synthetic id. |
| `thread_key` | always | The conversation. Group the inbox by it the way `aps.thread-id` groups the Lock Screen. |
| `url` | omitted when absent | The tap destination. See below. |
| `source.id` / `source.name` | always | The sender: a regular or critical service, or the API token that sent it. |
| `source.image_url` | omitted when absent | A public HTTPS avatar. |
| `question` | only on a question | Below. |

Push payloads do not identify the account owner and do not include stored API or
webhook credentials. Question payloads may include a single-use
`response_token`.

**Tap destinations.** The app opens `hark.url` when the notification body is
tapped, not when an action button is used. The value is at most 2048 characters
and cannot use `about:`, `blob:`, `data:`, `file:`, or `javascript:`. HTTPS,
universal links, and custom app schemes are allowed. The client must validate the
length and scheme again before opening the URL.

### Question payload

A question is a notification with `aps.category` set and a `hark.question`
object alongside. The category controls which action buttons iOS displays.

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
| `id` | always | Answer at [`POST /interactions/{id}/response`](#post-interactionsidresponse). |
| `kind` | always | `approval`, `yes_no` or `reply`. |
| `category` | always | The same identifier as `aps.category`, so a decoder never has to read it off the notification. |
| `action_digest` | always | Send this value when answering. It prevents an older notification from answering a replacement interaction. |
| `response_token` | omitted when absent | A single-use credential that lets an extension answer without a session. Present on interactions created by a webhook or API token. |
| `primary_label` / `secondary_label` | omitted when absent | Override the labels the registered category carries. |
| `expires_at` | always | When answering stops working. Remove the prompt from the interface before this time to avoid failed responses. |

The client registers these versioned categories at launch. Versioning prevents
category changes from altering the actions on existing notifications:

| `kind` | Category | Action identifiers | Buttons |
| --- | --- | --- | --- |
| `approval` | `hark.approval.v1` | `hark.action.approve`, `hark.action.deny` | Approve, Deny (destructive) |
| `yes_no` | `hark.yes_no.v1` | `hark.action.yes`, `hark.action.no` | Yes, No (destructive) |
| `reply` | `hark.reply.v1` | `hark.action.reply` | Reply (text input) |

An unrecognized kind uses `hark.reply.v1` so the recipient can provide a
free-text response.

The action identifier suffix matches the `action` value accepted by
[the answer endpoint](#post-interactionsidresponse). Extensions remove the
`hark.action.` prefix and submit the remaining value. The client registers action
identifiers; the server sends only the category.

### Live Activity payload

Start, update, and end events use the same structure. Hark data is contained in
`content-state` and `attributes`; the other keys are defined by Apple.

**Start** — includes attributes. ActivityKit keeps these attributes unchanged
for the lifetime of the activity:

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
      "accent_color": "#E64949",
      "style": "standard"
    },
    "attributes-type": "HarkActivityAttributes",
    "attributes": {
      "schema_version": 1,
      "delivery_id": "0198f3c2-1a5d-7b90-8c34-000000000001",
      "device_id": "0198f3a1-2b4c-7d8e-9f01-23456789abcd",
      "token_registration_url": "https://hark.example.com/activity-deliveries/0198f3c2-1a5d-7b90-8c34-000000000001/update-token",
      "token_registration_token": "…"
    },
    "alert": { "title": "Deploy", "body": "Building" },
    "input-push-token": 1,
    "stale-date": 1786014400
  }
}
```

**Update** — includes only the state and dates:

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
| `content-state` | always | The [state document](#the-state-document), delivered unchanged. |
| `attributes-type` | start only | Always `HarkActivityAttributes`. The app must declare `struct HarkActivityAttributes: ActivityAttributes` with this exact name or ActivityKit will not create the activity. |
| `attributes` | start only | Below. |
| `alert` | start only | Banner announcing the activity. It has no sound, category, or thread. For a `private` activity the banner is generic, while `content-state` still contains the full title and status. |
| `input-push-token` | start only | `1`. Requests a per-activity update token from ActivityKit. Updates and end events require this token. |
| `stale-date` | when the activity has one | Epoch seconds. May appear on any event. |
| `dismissal-date` | end only, when set | Epoch seconds. |
| `relevance-score` | — | Never sent. |

The widget extension has no session or keychain access. The **`attributes`**
object therefore contains the values it needs to register update tokens and
answer interactions:

| Field | Presence | Notes |
| --- | --- | --- |
| `schema_version` | always | `1`. |
| `delivery_id` | always | Identifies this activity delivery on this device. |
| `device_id` | always | Needed to answer a question, and to report a token. |
| `token_registration_url` | always | Where to `PUT` the per-activity update token provided by ActivityKit: [`PUT /activity-deliveries/{id}/update-token`](#put-activity-deliveriesidupdate-token). **Hark cannot update or end the activity until registration succeeds.** |
| `token_registration_token` | always | Single-use credential for that request. It is limited to this delivery and expires with the activity. |
| `question.id` / `question.action_digest` / `question.response_token` | only on a card presenting a question | The same three values a question notification carries, so a Lock Screen button answers through the same endpoint the notification action does. |

### Delivery rules

The following APNs transport rules apply:

| Rule | Notification | Live Activity |
| --- | --- | --- |
| `apns-push-type` | `alert` | `liveactivity` |
| `apns-topic` | the bundle id | the bundle id + `.push-type.liveactivity` |
| `apns-priority` | `10` | `10` |
| `apns-expiration` | `0` — APNs delivers it now or discards it | *(not sent; Apple's default applies)* |
| `apns-collapse-id` | never sent | never sent |

* **Payloads are capped at 4096 encoded bytes.** Hark rejects oversized payloads
  before contacting APNs and records a delivery failure.
* **Pushes are not retried** after rate limits, provider errors, or timeouts.
  Clients reconcile state by registering tokens when the app enters the
  foreground and ending local Live Activities that the server no longer lists.

  The only exception is `403 ExpiredProviderToken`, which rejects Hark's provider
  JWT before accepting the push. Hark refreshes the JWT and retries once.
* **Permanent token failures deactivate the device.** `410 Unregistered`, `BadDeviceToken`, or
  `ExpiredToken` marks the device inactive. Registering the token again reactivates
  it. Topic errors do not deactivate the device because they indicate server
  configuration problems.
* **Live Activity tokens are environment-scoped.** Debug builds use `sandbox`
  and release builds use `production`. Register the environment with the token.
  Hark skips tokens that do not match its configured APNs environment.

---

## Events and history

### `GET /events`

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
| `error` | Truncated APNs failure text. This owner-only field may include a device token, so webhook responses omit it. |

### `DELETE /events/{id}`

**Session only.** **204 No Content.**

Deleting a delivery also deletes its associated interaction.

### `GET /history`

Returns account history in one newest-first list: webhook deliveries, API
notifications, answered interactions, and Live Activity changes. **Session
only.** [Paged](#pagination).

| Parameter | Default | Notes |
| --- | --- | --- |
| `kind` | `all` | `all`, `notification`, `response`, `live_activity`. |
| `source` | *(absent)* | Exact `source_name` match. |
| `priority` | *(absent)* | `normal`, `time_sensitive`, `critical`. Items with a `null` priority never match. |

The filters combine. An unknown `kind` or `priority` is rejected with `422
validation_failed` naming the field.

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

Fields that do not apply to an item's `kind` are `null`.

* `id` is `"<source>:<row id>"`. The sources are `event`, `notification`,
  `response` and `live_activity`.
* Answered interactions are ordered by `responded_at`, not by the time they
  were created. Their `result` is `approved`, `denied`, `yes`, `no` or
  `replied`. For Live Activity entries, `result` is `start`, `update` or `end`.

### `GET /history/sources`

The distinct `source_name` values of the entries currently in history, sorted
case-insensitively. **Session only.** Not paged: the list is bounded by the
account's services and tokens.

**200 OK**

```json
{
  "sources": ["Deploy bot", "harkctl"]
}
```

### `DELETE /history`

Removes every entry the filters match, in one transaction. **Session only.**
Takes the same `kind`, `source`, and `priority` parameters as
[`GET /history`](#get-history); with no parameters it clears the whole history.

**204 No Content**, whether or not anything matched. An unknown `kind` or
`priority` is rejected with `422 validation_failed` naming the field.

Deleting a webhook delivery also deletes its associated interaction, as
[`DELETE /events/{id}`](#delete-eventsid) does. Pending interactions are not
history entries and are left alone.

### `DELETE /history/{id}`

Removes one entry. **Session only.** `{id}` is the composite id above.

**204 No Content**, `404 not_found` for an unknown or unowned entry.

Pending interactions cannot be deleted. Answer, cancel, or let them expire
first.

---

## Webhooks

Webhook routes are intended for systems that can call a URL but cannot manage
an API token header. The webhook token is part of the path. Hark does not write
it to access logs, and unknown or malformed tokens return `404 not_found`.

Create or rotate webhook URLs through the [service endpoints](#services).

### `POST /hooks/{token}`

Sends a notification, optionally as a question. Supports
[`Idempotency-Key`](#idempotency).

**Request**

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `body` | string | yes | 1–2000 characters. |
| `title` | string | no | 1–80 characters. Defaults to the service's title. |
| `image_url` | string | no | Public HTTPS URL. Defaults to the service's. |
| `url` | string | no | Tap destination. Defaults to the service's. |
| `priority` | enum | no | Defaults to the service's. Regular service webhooks accept `normal` or `time_sensitive`; [critical service](#critical-services) webhooks also accept `critical`. |
| `device_ids` | array of id | no | 1–50 entries. |
| `response` | object | no | Turns the notification into a question. |

`response`:

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `kind` | enum | yes | `approval`, `yes_no`, `reply`. |
| `expires_in_seconds` | integer | no | 30 – 86400, default 900. |
| `correlation_id` | string | no | 1–100 characters, echoed back with the answer so a caller can match it to its own work. |
| `callback` | object | no | `{ "url": <public HTTPS URL>, "token": <16–512 characters> }`. The answer is posted there when it arrives, with the token as a bearer credential, so the caller does not have to poll. See [the answer callback](#the-answer-callback). |

Omitted fields use the service defaults. Only `body` is required.

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

`response` is present only when the request includes a question. Hark always
creates the event record. Check `event.status` for the delivery result;
`message` explains `no_devices` and `failed` results. Detailed APNs errors are
available only in the owner's [event log](#get-events).

**404 `not_found`** for an unknown or malformed token. **422
`validation_failed`**, **409 `conflict`** for a reused idempotency key, **429
`rate_limited`** as elsewhere.

### The answer callback

When a webhook request includes `response.callback`, Hark sends the answer to
that URL. The callback receiver is an endpoint you provide.

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
| `type` | Currently always `interaction.answered`. Clients should still branch on this value. |
| `correlation_id` | The value supplied in the request, or `null`. |
| `action` | The selected choice, or `"reply"` for a reply interaction. |
| `text` | The reply text for a reply interaction; otherwise `null`. |

Rules a receiver can rely on:

* Hark sends callbacks only for answers. Expired and canceled interactions do
  not trigger one. Poll [the event](#get-hookstokeneventsevent_id) if you also
  need those states.
* Any `2xx` response confirms delivery. Other responses fail the attempt.
  Redirects are not followed because the bearer token is valid only for the
  configured host.
* `callback.url` must be a public HTTPS URL. Hark checks the hostname when the
  interaction is created and again before every connection. Every resolved
  address must be public and routable; private, loopback, link-local, or mixed
  public/private results fail the attempt. Hark connects directly and does not
  use HTTP proxies from the server environment.
* Hark retries failures after 30 seconds, 2 minutes, 10 minutes, and 1 hour.
  After the final failure, the answer remains available by polling.
* Delivery is at least once. If the receiver times out after processing a
  callback, Hark may send it again. Use `interaction_id` as the idempotency key.
* The callback does not include account, owner, device, or prompt details.

### `GET /hooks/{token}/events/{event_id}`

Returns the delivery result and the current state of its interaction. Use this
endpoint when the caller cannot receive callbacks.

**200 OK** — `{ "event": { … }, "response": { … } | null }`, the same two
objects as above.

If the interaction deadline has passed, this read changes its status to
`expired` before returning it.

### `POST /hooks/{token}/events/{event_id}/cancel`

Cancels the interaction created by a delivery. No request body.

**200 OK** — `{ "interaction": { … } }` with `status: "canceled"`.
**404 `not_found`** when there is no question here still awaiting an answer.

This route can cancel an interaction even if its deadline has just passed. The
owner-only cancel endpoint first marks overdue interactions as expired.

### The webhook Live Activity routes

These routes use the same request fields, responses, and conflict rules as the
[API token Live Activity endpoints](#live-activities), with the service as the
requester.

A service can only see and drive the activities it started: keys are unique per
requester, so the same key means different things to different senders.

#### `POST /hooks/{token}/activities`

Starts a service-owned Live Activity. Same request and response as
[`POST /activities`](#post-activities). Supports `Idempotency-Key`.

#### `GET /hooks/{token}/activities/{identifier}`

Reads one Live Activity started by this service. Same response as
[`GET /activities/{identifier}`](#get-activitiesidentifier).

#### `PATCH /hooks/{token}/activities/{identifier}`

Updates one Live Activity started by this service. Same request and response as
[`PATCH /activities/{identifier}`](#patch-activitiesidentifier). Supports
`Idempotency-Key`.

#### `POST /hooks/{token}/activities/{identifier}/end`

Ends one Live Activity started by this service. Same request and response as
[`POST /activities/{identifier}/end`](#post-activitiesidentifierend).
Supports `Idempotency-Key`.

---

## Dashboard

Hark includes an owner administration interface at `/dashboard`. Its routes are
separate from the JSON API:

| Method | Path | What it is |
| --- | --- | --- |
| `GET` | `/` | Redirects to `/dashboard` (`302`). |
| `GET` | `/dashboard` | Overview of counts, running Live Activities, and recent deliveries. |
| `GET` | `/dashboard/live/overview` | Overview data fragment used for automatic updates. |
| `GET` | `/dashboard/history` | The full archive, paged, filterable by kind (`?kind=`, `?after=`). |
| `GET` | `/dashboard/login` | Sign-in form. |
| `POST` | `/dashboard/login` | Signs in and sets the session cookie. |
| `POST` | `/dashboard/logout` | Invalidates the session and clears the cookie. |
| `GET` | `/dashboard/services` | Webhook services, and the form that creates one. |
| `POST` | `/dashboard/services` | Creates a service. |
| `GET` | `/dashboard/services/{id}` | One service: its webhook URL, defaults, recent deliveries. |
| `POST` | `/dashboard/services/{id}` | Saves the defaults. |
| `POST` | `/dashboard/services/{id}/rotate` | Replaces the webhook credential immediately. |
| `POST` | `/dashboard/services/{id}/delete` | Deletes the service and its associated deliveries. |
| `GET` | `/dashboard/critical-services` | Critical Alert settings, critical services, and the form that creates one. |
| `POST` | `/dashboard/critical-services` | Creates a critical service. |
| `POST` | `/dashboard/critical-services/settings` | Saves the account-wide Critical switch. |
| `GET` | `/dashboard/critical-services/{id}` | One critical service: its webhook URL, defaults, switches, and recent deliveries. |
| `POST` | `/dashboard/critical-services/{id}` | Saves a critical service's title, avatar, tap destination, default priority, and switch. |
| `POST` | `/dashboard/critical-services/{id}/rotate` | Replaces the webhook credential immediately. |
| `POST` | `/dashboard/critical-services/{id}/delete` | Deletes the service and its delivery history. |
| `GET` | `/dashboard/devices` | Registered phones. |
| `POST` | `/dashboard/devices/{id}/delete` | Unregisters one. |
| `GET` | `/dashboard/tokens` | API tokens and the token creation form. |
| `POST` | `/dashboard/tokens` | Creates a token and shows its secret once. |
| `POST` | `/dashboard/tokens/{id}/revoke` | Revokes one. |
| `GET` | `/dashboard/test` | The test-notification form. |
| `POST` | `/dashboard/test` | Sends one notification and reports what APNs said. |
| `GET` | `/cli/authorize` | The [device authorization approval screen](#get-cliauthorize). |
| `POST` | `/cli/authorize` | Records Approve or Deny, or looks up a typed code. |
| `GET` | `/docs` | Rendered API documentation. |
| `GET` | `/docs.md` | Raw Markdown API documentation. |
| `GET` | `/openapi.json` | OpenAPI 3.1 document. |
| `GET` | `/llms.txt` | Compact documentation index. |
| `GET` | `/dashboard/assets/{file}` | The stylesheets and script, at content-hashed URLs. |

Dashboard routes return HTML, including error responses. They are not a client
API and may change independently. Software clients should use the documented
JSON endpoints.

### Authentication

The dashboard uses the same [session](#session) as the API. Signing in through
`POST /dashboard/login` sets the same cookie as
[`POST /auth/login`](#post-authlogin). In that browser,
`GET /auth/session` returns `"kind": "session"`.

Every dashboard page except sign-in requires a session. API tokens do not grant
dashboard access; requests without a valid session are redirected to sign-in.

Dashboard sign-in uses the same per-client and per-process rate limits as
`POST /auth/login`. The dashboard and API login routes have separate rate
limit buckets.

### CSRF

Forms that change data use a **double-submit CSRF token**. Hark stores a random
32-byte value in an `HttpOnly`, `SameSite=Lax` cookie (`__Host-hark_csrf` over
HTTPS and `hark_csrf` over HTTP) and includes the same value in each form's
hidden `csrf_token` field. On submission, the server compares the values in
constant time. A mismatch returns an HTML `403` response. Hark issues a new
token after sign-in and after a rejected submission.

Hark also applies the API's [same-origin check](#cross-origin-requests) to unsafe
requests authenticated by a cookie. A request from another origin returns
[`origin_not_allowed`](#error-codes). The CSRF token also protects sign-in,
which happens before the browser has a session cookie.

Set `HARK_PUBLIC_URL` to the exact origin used in the browser. For example, if
it is configured as `http://localhost`, opening the dashboard through
`http://127.0.0.1` causes form submissions to fail with
`origin_not_allowed`.

### Responses

* Dashboard pages use `Cache-Control: no-store` because they can show token
  prefixes, device identifiers, and a newly created token secret. The public
  [`/docs`](#get-docs) page is cacheable.
* Assets are served at content-hashed URLs (`app-<digest>.css`) with
  `Cache-Control: public, max-age=31536000, immutable` and an `ETag`.
* Every response carries a content security policy that allows scripts and
  styles from this origin only, plus Google Fonts for the brand typeface, and
  no framing.

### The test notification

`POST /dashboard/test` sends one alert through the same push transport as
[`POST /notifications`](#post-notifications), to one device or to all of
them, and reports the accepted count and any provider failures on the page.

Test notifications are not recorded in history. To create a recorded
notification, create an API token and call `POST /notifications`.

### `GET /cli/authorize`

This page lets the owner approve or deny a
[device authorization request](#post-authdevicecode) from a command-line or
other input-limited client. It uses the dashboard session and CSRF protection.

| Parameter | Notes |
| --- | --- |
| `code` | Optional `user_code`. If omitted, the page displays a field for entering it. |

When the owner is signed out, the page redirects to sign-in and preserves the
code. API tokens cannot access or approve authorization requests.

The page shows the client name, code, expiry times, and requested scopes, then
offers **Approve** and **Deny**. Both use the same application logic as
[`POST /auth/device/requests/{user_code}/approve`](#post-authdevicerequestsuser_codeapprove)
and its `deny` counterpart. A request that has already been decided or expired
returns an HTML `409` response. An unknown code returns an HTML `404` response
and redisplays the code field.

`POST /cli/authorize` accepts `code`, `csrf_token` and an optional
`decision` of `approve` or `deny`; without a decision it is the lookup the
field submits. It answers `303` back to this page.

### `GET /docs`

Returns this API reference as HTML.

This route is public. Hark does not read cookies or `Authorization` headers for
it, and viewing the page does not extend a session.

`docs/api.md` is compiled into the binary and converted to HTML at startup.
The response is `text/html; charset=utf-8` with
`Cache-Control: public, max-age=300` and an `ETag`; a conditional `GET` gets
`304`.

The server also publishes these public, cacheable formats:

| Path | Content type | Use |
| --- | --- | --- |
| `/docs.md` | `text/markdown` | Complete Markdown reference and examples. |
| `/openapi.json` | `application/vnd.oai.openapi+json` | OpenAPI 3.1 operations, security requirements, request schemas and stable error format. Hark-specific authorization details use `x-hark-auth` and `x-hark-scopes`. |
| `/llms.txt` | `text/plain` | Compact index of the available documentation. |

Use OpenAPI for operation discovery, client generation, and schema validation.
Use this reference for delivery behavior, idempotency, callbacks, and push
payload rules.
