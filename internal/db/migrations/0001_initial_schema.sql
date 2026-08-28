-- Hark initial schema.
--
-- Conventions used throughout:
--
--   * Tables are plural snake_case. No table needs quoting.
--   * Every primary key is a UUIDv7 in canonical lowercase text form (see
--     internal/id). Ids therefore sort chronologically, which is what the
--     keyset pagination in internal/db relies on for its tie-breaks.
--   * Timestamps are timestamptz. The store truncates every value it writes to
--     millisecond precision, because that is the resolution the API exposes and
--     the resolution optimistic-concurrency predicates compare against.
--   * Enumerations are text with a CHECK constraint rather than a native enum:
--     members appear inside partial-index predicates, and adding a member stays
--     a plain migration instead of an ALTER TYPE.
--   * Plaintext credentials are never stored. Columns ending in _hash hold a
--     hex SHA-256 digest; columns ending in _ciphertext hold an AES-256-GCM
--     envelope that the application can decrypt to show the operator a token
--     again.
--   * There are no analytics tables, counters, or rollups in this schema, and
--     none may be added. The events, agent_notifications,
--     live_activity_operations and live_activity_delivery_attempts tables are
--     delivery records tied to a specific push, not telemetry.

-- ── Identity ────────────────────────────────────────────────────────────────

-- Hark is single-user: this table holds exactly one row, seeded at boot. There
-- is no sign-up surface, so no invitation, verification or password-reset
-- state exists. The password hash lives here rather than in a separate
-- credential table because there is exactly one credential per account.
CREATE TABLE users (
    id                  text        PRIMARY KEY,
    username            text        NOT NULL,
    email               text        NOT NULL,
    display_name        text        NOT NULL,
    password_hash       text,
    password_updated_at timestamptz,
    -- Claimed exactly once, by the first device registration, to authorise the
    -- one-shot welcome push.
    welcome_sent_at     timestamptz,
    critical_alerts_enabled boolean NOT NULL DEFAULT true,
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL
);
CREATE UNIQUE INDEX users_username_key ON users (username);
CREATE UNIQUE INDEX users_email_key    ON users (email);

-- Session tokens are hashed at rest exactly like every other credential: a
-- stolen database dump must not hand over live sessions.
CREATE TABLE sessions (
    id           text        PRIMARY KEY,
    user_id      text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   text        NOT NULL,
    -- Sliding refresh moves expires_at forward and stamps refreshed_at.
    created_at   timestamptz NOT NULL,
    refreshed_at timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL
);
CREATE UNIQUE INDEX sessions_token_hash_key ON sessions (token_hash);
CREATE INDEX        sessions_user_idx       ON sessions (user_id);
CREATE INDEX        sessions_expires_idx    ON sessions (expires_at);

-- ── Services (webhook sources) ──────────────────────────────────────────────

CREATE TABLE services (
    id               text        PRIMARY KEY,
    user_id          text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title            text        NOT NULL,
    image_url        text,
    url              text,
    priority         text        NOT NULL DEFAULT 'normal'
                       CHECK (priority IN ('normal', 'time_sensitive', 'critical')),
    -- Critical-capable services are managed in their own flow but otherwise
    -- use this exact service, webhook, interaction and delivery model.
    critical_capable boolean     NOT NULL DEFAULT false,
    critical_enabled boolean     NOT NULL DEFAULT false,
    -- SHA-256 of the plaintext webhook token; the hot path for POST /hooks.
    token_hash       text        NOT NULL,
    -- Encrypted copy so the owner can re-read the webhook URL after creation.
    token_ciphertext text        NOT NULL,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL,
    CONSTRAINT services_critical_priority_check
      CHECK (critical_capable OR priority <> 'critical'),
    CONSTRAINT services_critical_enabled_check
      CHECK (critical_capable OR NOT critical_enabled)
);
CREATE UNIQUE INDEX services_token_hash_key  ON services (token_hash);
CREATE INDEX        services_user_created_idx ON services (user_id, created_at DESC, id DESC);

-- ── Devices ────────────────────────────────────────────────────────────────

CREATE TABLE devices (
    id                                text        PRIMARY KEY,
    user_id                           text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- Raw APNs device token, lowercase hex. This is the push address and the
    -- natural key: a re-issued token registers as a new row.
    apns_token                        text        NOT NULL,
    platform                          text        NOT NULL DEFAULT 'ios' CHECK (platform = 'ios'),
    name                              text,
    -- Cleared to false when APNs reports the token permanently invalid. Rows
    -- are kept so history keeps resolving.
    active                            boolean     NOT NULL DEFAULT true,
    -- ActivityKit push-to-start token. Presence plus a known environment and
    -- schema version is what makes a device Live-Activity-capable.
    push_to_start_token_ciphertext    text,
    push_to_start_environment         text        CHECK (push_to_start_environment IN ('sandbox', 'production')),
    push_to_start_updated_at          timestamptz,
    live_activity_schema_version      integer,
    -- Capability flags reported by the client at registration. NULL means the
    -- device predates the feature and must not receive it.
    interaction_schema_version        integer,
    live_activity_interaction_version integer,
    created_at                        timestamptz NOT NULL,
    last_seen_at                      timestamptz NOT NULL
);
CREATE UNIQUE INDEX devices_apns_token_key     ON devices (apns_token);
CREATE INDEX        devices_user_last_seen_idx ON devices (user_id, last_seen_at DESC);

-- ── API tokens ─────────────────────────────────────────────────────────────

CREATE TABLE api_tokens (
    id           text        PRIMARY KEY,
    user_id      text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         text        NOT NULL,
    -- Domain-separated SHA-256 of the plaintext secret, which is shown once at
    -- creation and never stored.
    token_hash   text        NOT NULL,
    -- Leading characters of the plaintext, for display in token lists.
    prefix       text        NOT NULL,
    scopes       text[]      NOT NULL
                   CHECK (array_length(scopes, 1) >= 1 AND scopes <@ ARRAY[
                     'activities:read', 'activities:write', 'devices:read', 'events:read',
                     'interactions:create', 'interactions:read', 'notifications:send',
                     'services:read', 'services:write'
                   ]::text[]),
    expires_at   timestamptz,
    -- Written at most once a minute per token; every agent request would
    -- otherwise dirty the row.
    last_used_at timestamptz,
    -- Revocation is a soft flag: tokens are never hard-deleted, so the history
    -- they requested keeps its attribution.
    revoked_at   timestamptz,
    created_at   timestamptz NOT NULL
);
CREATE UNIQUE INDEX api_tokens_token_hash_key  ON api_tokens (token_hash);
CREATE INDEX        api_tokens_user_created_idx ON api_tokens (user_id, created_at DESC, id DESC);

-- OAuth-device-flow-shaped pairing for CLI clients. The device code is stored
-- only as a digest; the user code is short and human-readable by design.
CREATE TABLE device_authorization_requests (
    id                    text        PRIMARY KEY,
    device_code_hash      text        NOT NULL,
    user_code             text        NOT NULL,
    client_name           text        NOT NULL,
    requested_scopes      text[]      NOT NULL CHECK (array_length(requested_scopes, 1) >= 1),
    status                text        NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending', 'approved', 'denied', 'expired', 'consumed')),
    approved_user_id      text        REFERENCES users (id) ON DELETE CASCADE,
    -- TTL of the request itself.
    expires_at            timestamptz NOT NULL,
    -- Becomes expires_at on the token this request issues.
    token_expires_at      timestamptz NOT NULL,
    -- Grows when a client polls faster than it was told to.
    poll_interval_seconds integer     NOT NULL CHECK (poll_interval_seconds > 0),
    last_polled_at        timestamptz,
    -- Stamped when the request reaches any terminal status.
    resolved_at           timestamptz,
    created_at            timestamptz NOT NULL
);
CREATE UNIQUE INDEX device_authorization_requests_device_code_key ON device_authorization_requests (device_code_hash);
CREATE UNIQUE INDEX device_authorization_requests_user_code_key   ON device_authorization_requests (user_code);
CREATE INDEX        device_authorization_requests_purge_idx       ON device_authorization_requests (status, expires_at);

-- ── Delivery log ───────────────────────────────────────────────────────────

-- One row per webhook request that reached validation. This is the owner's
-- delivery history, not analytics: it is user-visible and user-deletable.
CREATE TABLE events (
    id              text        PRIMARY KEY,
    service_id      text        NOT NULL REFERENCES services (id) ON DELETE CASCADE,
    -- Resolved values: the request's field, else the service default.
    title           text        NOT NULL,
    body            text        NOT NULL,
    image_url       text,
    url             text,
    priority        text        NOT NULL DEFAULT 'normal'
                      CHECK (priority IN ('normal', 'time_sensitive', 'critical')),
    -- processing is written before any push is attempted; everything else is
    -- terminal.
    status          text        NOT NULL
                      CHECK (status IN ('processing', 'no_devices', 'accepted', 'partial', 'failed')),
    -- APNs acceptances, not device receipts.
    delivered_count integer     NOT NULL DEFAULT 0 CHECK (delivered_count >= 0),
    -- Joined APNs failure reasons. Kept out of the caller's response because
    -- provider errors can embed device tokens.
    error           text,
    idempotency_key text,
    -- SHA-256 of the canonicalised request body; only set alongside a key.
    request_hash    text,
    created_at      timestamptz NOT NULL
);
-- NULLS DISTINCT (the default) is load-bearing on every idempotency index in
-- this schema: most rows carry no key and must not collide.
CREATE UNIQUE INDEX events_service_idempotency_key ON events (service_id, idempotency_key);
CREATE INDEX        events_service_created_idx     ON events (service_id, created_at DESC, id DESC);
CREATE INDEX        events_created_idx             ON events (created_at DESC, id DESC);

-- One-shot pushes sent with an API token. Kept so idempotent retries can
-- replay and so the send shows up in the account's history.
CREATE TABLE agent_notifications (
    id                 text        PRIMARY KEY,
    user_id            text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- Mandatory here, unlike the other requester tables: there is no webhook
    -- equivalent of an agent notification.
    requester_token_id text        NOT NULL REFERENCES api_tokens (id) ON DELETE CASCADE,
    title              text        NOT NULL,
    body               text        NOT NULL,
    image_url          text,
    url                text,
    priority           text        NOT NULL DEFAULT 'normal'
                         CHECK (priority IN ('normal', 'time_sensitive')),
    -- The same vocabulary as events.status, settled the same way. The history
    -- feed shows webhook deliveries and agent pushes side by side, so "nothing
    -- to send to" and "nothing got through" have to read alike in both.
    status             text        NOT NULL
                         CHECK (status IN ('processing', 'no_devices', 'accepted', 'partial', 'failed')),
    accepted_count     integer     NOT NULL DEFAULT 0 CHECK (accepted_count >= 0),
    idempotency_key    text,
    request_hash       text,
    created_at         timestamptz NOT NULL
);
CREATE UNIQUE INDEX agent_notifications_token_idempotency_key ON agent_notifications (requester_token_id, idempotency_key);
CREATE INDEX        agent_notifications_token_created_idx     ON agent_notifications (requester_token_id, created_at DESC);
CREATE INDEX        agent_notifications_user_created_idx      ON agent_notifications (user_id, created_at DESC, id DESC);

-- ── Interactions ───────────────────────────────────────────────────────────

-- A question sent to the phone that expects an answer. Created either by an
-- API token (the agent surface) or by a service (a webhook carrying a response
-- block); exactly one requester is set.
CREATE TABLE interactions (
    id                        text        PRIMARY KEY,
    user_id                   text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    requester_token_id        text        REFERENCES api_tokens (id) ON DELETE CASCADE,
    requester_service_id      text        REFERENCES services (id)   ON DELETE CASCADE,
    -- Set only by the webhook flow, one interaction per event.
    event_id                  text        REFERENCES events (id)     ON DELETE CASCADE,
    title                     text        NOT NULL,
    prompt                    text        NOT NULL,
    kind                      text        NOT NULL CHECK (kind IN ('approval', 'yes_no', 'reply')),
    presentation              text        NOT NULL DEFAULT 'notification'
                                CHECK (presentation IN ('notification', 'live_activity')),
    -- Custom button labels; only meaningful for a Live Activity presentation.
    primary_label             text,
    secondary_label           text,
    status                    text        NOT NULL DEFAULT 'pending'
                                CHECK (status IN ('pending', 'approved', 'denied', 'yes', 'no',
                                                  'replied', 'canceled', 'expired')),
    choices                   text[]      NOT NULL CHECK (array_length(choices, 1) >= 1),
    -- The action string for approval/yes_no, the free text for reply.
    response                  text,
    url                       text,
    image_url                 text,
    -- Echoed back to the webhook caller in the callback body.
    correlation_id            text,
    -- Binds a device's answer to the exact question that was displayed.
    action_digest             text        NOT NULL,
    idempotency_key           text,
    request_hash              text,
    -- Lets the phone answer a webhook interaction without a session; the
    -- plaintext travels in the push payload only.
    response_token_hash       text,
    callback_url              text,
    callback_token_ciphertext text,
    callback_status           text        CHECK (callback_status IN ('pending', 'retrying', 'delivered', 'failed')),
    callback_attempts         integer     NOT NULL DEFAULT 0 CHECK (callback_attempts >= 0),
    -- NULL once delivered or permanently failed. Doubles as the worker's lease.
    callback_next_attempt_at  timestamptz,
    callback_last_error       text,
    callback_delivered_at     timestamptz,
    accepted_count            integer     NOT NULL DEFAULT 0 CHECK (accepted_count >= 0),
    responding_device_id      text        REFERENCES devices (id) ON DELETE SET NULL,
    expires_at                timestamptz NOT NULL,
    created_at                timestamptz NOT NULL,
    responded_at              timestamptz,
    canceled_at               timestamptz,
    CONSTRAINT interactions_requester_check
      CHECK ((requester_token_id IS NOT NULL) <> (requester_service_id IS NOT NULL))
);
CREATE UNIQUE INDEX interactions_token_idempotency_key ON interactions (requester_token_id, idempotency_key);
CREATE UNIQUE INDEX interactions_event_key             ON interactions (event_id);
CREATE INDEX        interactions_token_created_idx     ON interactions (requester_token_id, created_at DESC);
CREATE INDEX        interactions_service_created_idx   ON interactions (requester_service_id, created_at DESC);
CREATE INDEX        interactions_user_created_idx      ON interactions (user_id, created_at DESC, id DESC);
-- The pending inbox and the lazy-expiry sweep both read only pending rows.
CREATE INDEX        interactions_pending_idx           ON interactions (user_id, created_at DESC, id DESC)
                                                       WHERE status = 'pending';
-- The answered-interaction rows of the history feed.
CREATE INDEX        interactions_user_responded_idx    ON interactions (user_id, responded_at DESC, id DESC)
                                                       WHERE responded_at IS NOT NULL;
-- The callback worker's claim query.
CREATE INDEX        interactions_callback_due_idx      ON interactions (callback_next_attempt_at)
                                                       WHERE callback_status IN ('pending', 'retrying');

-- ── Live Activities ────────────────────────────────────────────────────────

-- The logical activity. live_activity_deliveries materialise it per device.
CREATE TABLE live_activities (
    id                   text        PRIMARY KEY,
    user_id              text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    requester_token_id   text        REFERENCES api_tokens (id)  ON DELETE CASCADE,
    requester_service_id text        REFERENCES services (id)    ON DELETE CASCADE,
    -- Non-NULL marks an interactive activity: one that presents an interaction
    -- on the Lock Screen. Those are hidden from the ordinary activity surfaces
    -- and are exempt from the one-activity-per-device rule.
    interaction_id       text        REFERENCES interactions (id) ON DELETE CASCADE,
    -- Requester-chosen stable handle, reusable once the activity is terminal.
    key                  text,
    schema_version       integer     NOT NULL,
    -- The full ActivityKit content-state document.
    props                jsonb       NOT NULL,
    status               text        NOT NULL DEFAULT 'starting'
                           CHECK (status IN ('starting', 'active', 'partial', 'failed', 'ended', 'expired')),
    -- Optimistic-concurrency token: every mutation is guarded on it.
    sequence             integer     NOT NULL DEFAULT 0 CHECK (sequence >= 0),
    -- Epoch SECONDS, and a monotonic counter rather than a clock: ActivityKit
    -- uses it to discard out-of-order pushes, so it is max(now_s, prev + 1).
    apns_timestamp       bigint      NOT NULL DEFAULT 0 CHECK (apns_timestamp >= 0),
    -- Result of the most recent operation, not a lifetime total.
    accepted_count       integer     NOT NULL DEFAULT 0 CHECK (accepted_count >= 0),
    failed_count         integer     NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    -- Set by the start request only; updates and ends key off the operation.
    idempotency_key      text,
    request_hash         text,
    expires_at           timestamptz NOT NULL,
    -- Becomes the APNs stale-date and dismissal-date respectively.
    stale_at             timestamptz,
    dismissal_at         timestamptz,
    created_at           timestamptz NOT NULL,
    updated_at           timestamptz NOT NULL,
    ended_at             timestamptz,
    CONSTRAINT live_activities_requester_check
      CHECK ((requester_token_id IS NOT NULL) <> (requester_service_id IS NOT NULL))
);
CREATE UNIQUE INDEX live_activities_interaction_key         ON live_activities (interaction_id);
CREATE UNIQUE INDEX live_activities_token_idempotency_key   ON live_activities (requester_token_id, idempotency_key);
CREATE UNIQUE INDEX live_activities_service_idempotency_key ON live_activities (requester_service_id, idempotency_key);
-- One live activity per key per requester. Partial, so a key frees up as soon
-- as its activity reaches a terminal status.
CREATE UNIQUE INDEX live_activities_token_key_key   ON live_activities (requester_token_id, key)
  WHERE status IN ('starting', 'active', 'partial');
CREATE UNIQUE INDEX live_activities_service_key_key ON live_activities (requester_service_id, key)
  WHERE status IN ('starting', 'active', 'partial');
CREATE INDEX live_activities_user_updated_idx    ON live_activities (user_id, updated_at DESC, id DESC);
CREATE INDEX live_activities_token_created_idx   ON live_activities (requester_token_id, created_at DESC, id DESC);
CREATE INDEX live_activities_service_created_idx ON live_activities (requester_service_id, created_at DESC, id DESC);
CREATE INDEX live_activities_expiry_idx          ON live_activities (expires_at)
  WHERE status IN ('starting', 'active', 'partial');

-- One row per (activity, device): the activity as it exists on one phone.
CREATE TABLE live_activity_deliveries (
    id                      text        PRIMARY KEY,
    activity_id             text        NOT NULL REFERENCES live_activities (id) ON DELETE CASCADE,
    device_id               text        NOT NULL REFERENCES devices (id)         ON DELETE CASCADE,
    purpose                 text        NOT NULL DEFAULT 'task' CHECK (purpose IN ('task', 'interaction')),
    -- active means the phone confirmed the activity exists and handed back an
    -- update token.
    status                  text        NOT NULL DEFAULT 'pending'
                              CHECK (status IN ('pending', 'accepted', 'active', 'failed', 'ended')),
    environment             text        NOT NULL CHECK (environment IN ('sandbox', 'production')),
    schema_version          integer     NOT NULL,
    -- ActivityKit's own identifier, reported by the phone.
    native_activity_id      text,
    update_token_ciphertext text,
    update_token_updated_at timestamptz,
    -- Diagnostics from the last APNs attempt.
    last_event              text        CHECK (last_event IN ('start', 'update', 'end')),
    last_sequence           integer     NOT NULL DEFAULT -1,
    last_apns_status        integer,
    last_apns_reason        text,
    last_apns_id            text,
    last_attempt_at         timestamptz,
    created_at              timestamptz NOT NULL,
    updated_at              timestamptz NOT NULL,
    ended_at                timestamptz
);
CREATE UNIQUE INDEX live_activity_deliveries_activity_device_key ON live_activity_deliveries (activity_id, device_id);
CREATE INDEX        live_activity_deliveries_device_status_idx   ON live_activity_deliveries (device_id, status);
CREATE INDEX        live_activity_deliveries_native_idx          ON live_activity_deliveries (native_activity_id)
  WHERE native_activity_id IS NOT NULL;
-- The device-slot invariant: a phone shows at most one ordinary Live Activity
-- at a time. Interaction deliveries sit outside the index and may coexist.
CREATE UNIQUE INDEX live_activity_deliveries_one_task_per_device_key ON live_activity_deliveries (device_id)
  WHERE purpose = 'task' AND status IN ('pending', 'accepted', 'active');

-- One row per requester-initiated mutation. Also the unit of rate-limit
-- accounting and the source of the feed's Live Activity rows.
CREATE TABLE live_activity_operations (
    id                   text        PRIMARY KEY,
    activity_id          text        NOT NULL REFERENCES live_activities (id) ON DELETE CASCADE,
    requester_token_id   text        REFERENCES api_tokens (id) ON DELETE CASCADE,
    requester_service_id text        REFERENCES services (id)   ON DELETE CASCADE,
    event                text        NOT NULL CHECK (event IN ('start', 'update', 'end')),
    -- The activity's sequence after this operation landed.
    sequence             integer     NOT NULL CHECK (sequence >= 0),
    -- Activity state immediately after the operation.
    props                jsonb       NOT NULL,
    idempotency_key      text,
    request_hash         text,
    accepted_count       integer     NOT NULL DEFAULT 0 CHECK (accepted_count >= 0),
    failed_count         integer     NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    created_at           timestamptz NOT NULL,
    CONSTRAINT live_activity_operations_requester_check
      CHECK ((requester_token_id IS NOT NULL) <> (requester_service_id IS NOT NULL))
);
CREATE UNIQUE INDEX live_activity_operations_token_idempotency_key   ON live_activity_operations (requester_token_id, idempotency_key);
CREATE UNIQUE INDEX live_activity_operations_service_idempotency_key ON live_activity_operations (requester_service_id, idempotency_key);
CREATE INDEX        live_activity_operations_token_created_idx       ON live_activity_operations (requester_token_id, created_at DESC);
CREATE INDEX        live_activity_operations_service_created_idx     ON live_activity_operations (requester_service_id, created_at DESC);
CREATE INDEX        live_activity_operations_activity_created_idx    ON live_activity_operations (activity_id, created_at DESC, id DESC);

-- Append-only audit of every APNs call made for an activity. Nothing reads it
-- at request time; it exists for after-the-fact debugging, and the store
-- offers a retention delete because it would otherwise grow without bound.
CREATE TABLE live_activity_delivery_attempts (
    id                   text        PRIMARY KEY,
    activity_id          text        NOT NULL REFERENCES live_activities (id)           ON DELETE CASCADE,
    delivery_id          text        NOT NULL REFERENCES live_activity_deliveries (id)  ON DELETE CASCADE,
    operation_id         text        NOT NULL REFERENCES live_activity_operations (id)  ON DELETE CASCADE,
    requester_token_id   text        REFERENCES api_tokens (id) ON DELETE CASCADE,
    requester_service_id text        REFERENCES services (id)   ON DELETE CASCADE,
    event                text        NOT NULL CHECK (event IN ('start', 'update', 'end')),
    sequence             integer     NOT NULL CHECK (sequence >= 0),
    -- NULL when no HTTP call was made; apns_reason then carries a synthetic
    -- reason such as MissingUpdateToken or ProviderNotConfigured.
    apns_status          integer,
    apns_reason          text,
    apns_id              text,
    created_at           timestamptz NOT NULL,
    CONSTRAINT live_activity_delivery_attempts_requester_check
      CHECK ((requester_token_id IS NOT NULL) <> (requester_service_id IS NOT NULL))
);
CREATE INDEX live_activity_delivery_attempts_activity_created_idx ON live_activity_delivery_attempts (activity_id, created_at DESC);
CREATE INDEX live_activity_delivery_attempts_created_idx          ON live_activity_delivery_attempts (created_at);
