-- Configured alarm sources and their delivery history.

-- Per-source Critical Alert settings live on safety_sources.
ALTER TABLE users ADD COLUMN critical_alerts_enabled boolean NOT NULL DEFAULT true;

CREATE TABLE safety_sources (
    id               text        PRIMARY KEY,
    user_id          text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- Safety reports accept only these alarm types.
    kind             text        NOT NULL CHECK (kind IN
                       ('smoke', 'carbon_monoxide', 'panic', 'intrusion', 'water_leak')),
    name             text        NOT NULL,
    -- New sources require a separate update before using Critical Alerts.
    critical_enabled boolean     NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL
);
CREATE INDEX safety_sources_user_created_idx ON safety_sources (user_id, created_at DESC, id DESC);

CREATE TABLE safety_events (
    id                 text        PRIMARY KEY,
    source_id          text        NOT NULL REFERENCES safety_sources (id) ON DELETE CASCADE,
    -- Session-initiated setup tests have no requester token.
    requester_token_id text        REFERENCES api_tokens (id) ON DELETE CASCADE,
    state              text        NOT NULL CHECK (state IN ('active', 'resolved', 'test')),
    -- Composed by the server, not supplied by the reporter.
    title              text        NOT NULL,
    body               text        NOT NULL,
    priority           text        NOT NULL CHECK (priority IN ('normal', 'time_sensitive', 'critical')),
    -- coalesced and rate_limited are recorded without a push.
    status             text        NOT NULL CHECK (status IN
                         ('processing', 'no_devices', 'accepted', 'partial', 'failed',
                          'coalesced', 'rate_limited')),
    delivered_count    integer     NOT NULL DEFAULT 0 CHECK (delivered_count >= 0),
    -- APNs failure details shown only to the owner.
    error              text,
    idempotency_key    text,
    request_hash       text,
    created_at         timestamptz NOT NULL
);
-- NULL values allow keyless reports and session tests.
CREATE UNIQUE INDEX safety_events_token_idempotency_key ON safety_events (requester_token_id, idempotency_key);
CREATE INDEX safety_events_source_created_idx ON safety_events (source_id, created_at DESC, id DESC);
CREATE INDEX safety_events_created_idx        ON safety_events (created_at DESC, id DESC);

-- General notification callers can no longer select critical priority.
UPDATE services SET priority = 'time_sensitive' WHERE priority = 'critical';
ALTER TABLE services DROP CONSTRAINT services_priority_check;
ALTER TABLE services ADD CONSTRAINT services_priority_check
    CHECK (priority IN ('normal', 'time_sensitive'));

ALTER TABLE api_tokens DROP CONSTRAINT api_tokens_scopes_check;
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_scopes_check
    CHECK (array_length(scopes, 1) >= 1 AND scopes <@ ARRAY[
      'activities:read', 'activities:write', 'devices:read', 'events:read',
      'interactions:create', 'interactions:read', 'notifications:send',
      'safety:report', 'services:read', 'services:write'
    ]::text[]);
