-- Core tables. IDs are UUIDv7 text; timestamps use millisecond precision.

CREATE TABLE users (
    id                  text        PRIMARY KEY,
    username            text        NOT NULL,
    email               text        NOT NULL,
    display_name        text        NOT NULL,
    role                text        NOT NULL DEFAULT 'user' CHECK (role IN ('admin', 'user')),
    password_hash       text,
    password_updated_at timestamptz,
    welcome_sent_at     timestamptz,
    critical_alerts_enabled boolean NOT NULL DEFAULT true,
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL
);
CREATE UNIQUE INDEX users_username_key ON users (username);
CREATE UNIQUE INDEX users_email_key    ON users (email);
CREATE UNIQUE INDEX users_one_admin_key ON users (role) WHERE role = 'admin';

CREATE TABLE sessions (
    id           text        PRIMARY KEY,
    user_id      text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   text        NOT NULL,
    created_at   timestamptz NOT NULL,
    refreshed_at timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL
);
CREATE UNIQUE INDEX sessions_token_hash_key ON sessions (token_hash);
CREATE INDEX        sessions_user_idx       ON sessions (user_id);
CREATE INDEX        sessions_expires_idx    ON sessions (expires_at);


CREATE TABLE services (
    id               text        PRIMARY KEY,
    user_id          text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title            text        NOT NULL,
    image_url        text,
    url              text,
    priority         text        NOT NULL DEFAULT 'normal'
                       CHECK (priority IN ('normal', 'time_sensitive', 'critical')),
    critical_capable boolean     NOT NULL DEFAULT false,
    critical_enabled boolean     NOT NULL DEFAULT false,
    token_hash       text        NOT NULL,
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


CREATE TABLE devices (
    id                                text        PRIMARY KEY,
    user_id                           text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    apns_token                        text        NOT NULL,
    platform                          text        NOT NULL DEFAULT 'ios' CHECK (platform = 'ios'),
    name                              text,
    active                            boolean     NOT NULL DEFAULT true,
    push_to_start_token_ciphertext    text,
    push_to_start_environment         text        CHECK (push_to_start_environment IN ('sandbox', 'production')),
    push_to_start_updated_at          timestamptz,
    live_activity_schema_version      integer,
    interaction_schema_version        integer,
    live_activity_interaction_version integer,
    created_at                        timestamptz NOT NULL,
    last_seen_at                      timestamptz NOT NULL
);
CREATE UNIQUE INDEX devices_apns_token_key     ON devices (apns_token);
CREATE INDEX        devices_user_last_seen_idx ON devices (user_id, last_seen_at DESC);


CREATE TABLE api_tokens (
    id           text        PRIMARY KEY,
    user_id      text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         text        NOT NULL,
    token_hash   text        NOT NULL,
    prefix       text        NOT NULL,
    scopes       text[]      NOT NULL
                   CHECK (cardinality(scopes) >= 1 AND scopes <@ ARRAY[
                     'activities:read', 'activities:write', 'devices:read', 'events:read',
                     'interactions:create', 'interactions:read', 'notifications:send',
                     'services:read', 'services:write'
                   ]::text[]),
    -- A picture the owner set, or the logo an OAuth client published; NULL
    -- otherwise.
    image_url    text,
    expires_at   timestamptz,
    last_used_at timestamptz,
    revoked_at   timestamptz,
    created_at   timestamptz NOT NULL
);
CREATE UNIQUE INDEX api_tokens_token_hash_key  ON api_tokens (token_hash);
CREATE INDEX        api_tokens_user_created_idx ON api_tokens (user_id, created_at DESC, id DESC);

CREATE TABLE device_authorization_requests (
    id                    text        PRIMARY KEY,
    device_code_hash      text        NOT NULL,
    user_code             text        NOT NULL,
    client_name           text        NOT NULL,
    requested_scopes      text[]      NOT NULL CHECK (cardinality(requested_scopes) >= 1),
    status                text        NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending', 'approved', 'denied', 'expired', 'consumed')),
    approved_user_id      text        REFERENCES users (id) ON DELETE CASCADE,
    expires_at            timestamptz NOT NULL,
    token_expires_at      timestamptz NOT NULL,
    poll_interval_seconds integer     NOT NULL CHECK (poll_interval_seconds > 0),
    last_polled_at        timestamptz,
    resolved_at           timestamptz,
    created_at            timestamptz NOT NULL
);
CREATE UNIQUE INDEX device_authorization_requests_device_code_key ON device_authorization_requests (device_code_hash);
CREATE UNIQUE INDEX device_authorization_requests_user_code_key   ON device_authorization_requests (user_code);
CREATE INDEX        device_authorization_requests_purge_idx       ON device_authorization_requests (status, expires_at);


CREATE TABLE events (
    id              text        PRIMARY KEY,
    service_id      text        NOT NULL REFERENCES services (id) ON DELETE CASCADE,
    title           text        NOT NULL,
    body            text        NOT NULL,
    image_url       text,
    url             text,
    priority        text        NOT NULL DEFAULT 'normal'
                      CHECK (priority IN ('normal', 'time_sensitive', 'critical')),
    status          text        NOT NULL
                      CHECK (status IN ('processing', 'no_devices', 'accepted', 'partial', 'failed')),
    delivered_count integer     NOT NULL DEFAULT 0 CHECK (delivered_count >= 0),
    error           text,
    idempotency_key text,
    request_hash    text,
    created_at      timestamptz NOT NULL
);
-- NULL idempotency keys remain distinct in every idempotency index.
CREATE UNIQUE INDEX events_service_idempotency_key ON events (service_id, idempotency_key);
CREATE INDEX        events_service_created_idx     ON events (service_id, created_at DESC, id DESC);
CREATE INDEX        events_created_idx             ON events (created_at DESC, id DESC);

CREATE TABLE agent_notifications (
    id                 text        PRIMARY KEY,
    user_id            text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    requester_token_id text        NOT NULL REFERENCES api_tokens (id) ON DELETE CASCADE,
    title              text        NOT NULL,
    body               text        NOT NULL,
    image_url          text,
    url                text,
    priority           text        NOT NULL DEFAULT 'normal'
                         CHECK (priority IN ('normal', 'time_sensitive')),
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


CREATE TABLE interactions (
    id                        text        PRIMARY KEY,
    user_id                   text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    requester_token_id        text        REFERENCES api_tokens (id) ON DELETE CASCADE,
    requester_service_id      text        REFERENCES services (id)   ON DELETE CASCADE,
    event_id                  text        REFERENCES events (id)     ON DELETE CASCADE,
    title                     text        NOT NULL,
    prompt                    text        NOT NULL,
    kind                      text        NOT NULL CHECK (kind IN ('approval', 'yes_no', 'reply')),
    presentation              text        NOT NULL DEFAULT 'notification'
                                CHECK (presentation IN ('notification', 'live_activity')),
    primary_label             text,
    secondary_label           text,
    status                    text        NOT NULL DEFAULT 'pending'
                                CHECK (status IN ('pending', 'approved', 'denied', 'yes', 'no',
                                                  'replied', 'canceled', 'expired')),
    choices                   text[]      NOT NULL CHECK (cardinality(choices) >= 1),
    response                  text,
    url                       text,
    image_url                 text,
    correlation_id            text,
    action_digest             text        NOT NULL,
    idempotency_key           text,
    request_hash              text,
    response_token_hash       text,
    callback_url              text,
    callback_token_ciphertext text,
    callback_status           text        CHECK (callback_status IN ('pending', 'retrying', 'delivered', 'failed')),
    callback_attempts         integer     NOT NULL DEFAULT 0 CHECK (callback_attempts >= 0),
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
CREATE INDEX        interactions_pending_idx           ON interactions (user_id, created_at DESC, id DESC)
                                                       WHERE status = 'pending';
CREATE INDEX        interactions_user_responded_idx    ON interactions (user_id, responded_at DESC, id DESC)
                                                       WHERE responded_at IS NOT NULL;
CREATE INDEX        interactions_callback_due_idx      ON interactions (callback_next_attempt_at)
                                                       WHERE callback_status IN ('pending', 'retrying');


CREATE TABLE live_activities (
    id                   text        PRIMARY KEY,
    user_id              text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    requester_token_id   text        REFERENCES api_tokens (id)  ON DELETE CASCADE,
    requester_service_id text        REFERENCES services (id)    ON DELETE CASCADE,
    interaction_id       text        REFERENCES interactions (id) ON DELETE CASCADE,
    key                  text,
    schema_version       integer     NOT NULL,
    props                jsonb       NOT NULL,
    status               text        NOT NULL DEFAULT 'starting'
                           CHECK (status IN ('starting', 'active', 'partial', 'failed', 'ended', 'expired')),
    sequence             integer     NOT NULL DEFAULT 0 CHECK (sequence >= 0),
    -- Monotonic APNs timestamp: max(current epoch seconds, previous + 1).
    apns_timestamp       bigint      NOT NULL DEFAULT 0 CHECK (apns_timestamp >= 0),
    accepted_count       integer     NOT NULL DEFAULT 0 CHECK (accepted_count >= 0),
    failed_count         integer     NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    idempotency_key      text,
    request_hash         text,
    expires_at           timestamptz NOT NULL,
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
CREATE UNIQUE INDEX live_activities_token_key_key   ON live_activities (requester_token_id, key)
  WHERE status IN ('starting', 'active', 'partial');
CREATE UNIQUE INDEX live_activities_service_key_key ON live_activities (requester_service_id, key)
  WHERE status IN ('starting', 'active', 'partial');
CREATE INDEX live_activities_user_updated_idx    ON live_activities (user_id, updated_at DESC, id DESC);
CREATE INDEX live_activities_token_created_idx   ON live_activities (requester_token_id, created_at DESC, id DESC);
CREATE INDEX live_activities_service_created_idx ON live_activities (requester_service_id, created_at DESC, id DESC);
CREATE INDEX live_activities_expiry_idx          ON live_activities (expires_at)
  WHERE status IN ('starting', 'active', 'partial');

CREATE TABLE live_activity_deliveries (
    id                      text        PRIMARY KEY,
    activity_id             text        NOT NULL REFERENCES live_activities (id) ON DELETE CASCADE,
    device_id               text        NOT NULL REFERENCES devices (id)         ON DELETE CASCADE,
    purpose                 text        NOT NULL DEFAULT 'task' CHECK (purpose IN ('task', 'interaction')),
    status                  text        NOT NULL DEFAULT 'pending'
                              CHECK (status IN ('pending', 'accepted', 'active', 'failed', 'ended')),
    environment             text        NOT NULL CHECK (environment IN ('sandbox', 'production')),
    schema_version          integer     NOT NULL,
    native_activity_id      text,
    update_token_ciphertext text,
    update_token_updated_at timestamptz,
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
-- Interactive deliveries do not occupy the ordinary activity slot.
CREATE UNIQUE INDEX live_activity_deliveries_one_task_per_device_key ON live_activity_deliveries (device_id)
  WHERE purpose = 'task' AND status IN ('pending', 'accepted', 'active');

CREATE TABLE live_activity_operations (
    id                   text        PRIMARY KEY,
    activity_id          text        NOT NULL REFERENCES live_activities (id) ON DELETE CASCADE,
    requester_token_id   text        REFERENCES api_tokens (id) ON DELETE CASCADE,
    requester_service_id text        REFERENCES services (id)   ON DELETE CASCADE,
    event                text        NOT NULL CHECK (event IN ('start', 'update', 'end')),
    sequence             integer     NOT NULL CHECK (sequence >= 0),
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

CREATE TABLE live_activity_delivery_attempts (
    id                   text        PRIMARY KEY,
    activity_id          text        NOT NULL REFERENCES live_activities (id)           ON DELETE CASCADE,
    delivery_id          text        NOT NULL REFERENCES live_activity_deliveries (id)  ON DELETE CASCADE,
    operation_id         text        NOT NULL REFERENCES live_activity_operations (id)  ON DELETE CASCADE,
    requester_token_id   text        REFERENCES api_tokens (id) ON DELETE CASCADE,
    requester_service_id text        REFERENCES services (id)   ON DELETE CASCADE,
    event                text        NOT NULL CHECK (event IN ('start', 'update', 'end')),
    sequence             integer     NOT NULL CHECK (sequence >= 0),
    apns_status          integer,
    apns_reason          text,
    apns_id              text,
    created_at           timestamptz NOT NULL,
    CONSTRAINT live_activity_delivery_attempts_requester_check
      CHECK ((requester_token_id IS NOT NULL) <> (requester_service_id IS NOT NULL))
);
CREATE INDEX live_activity_delivery_attempts_activity_created_idx ON live_activity_delivery_attempts (activity_id, created_at DESC);
CREATE INDEX live_activity_delivery_attempts_created_idx          ON live_activity_delivery_attempts (created_at);
