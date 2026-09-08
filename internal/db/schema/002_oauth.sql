-- OAuth clients and authorization codes.

CREATE TABLE oauth_clients (
    id            text        PRIMARY KEY,
    name          text        NOT NULL,
    redirect_uris text[]      NOT NULL CHECK (cardinality(redirect_uris) >= 1),
    client_uri    text,
    logo_uri      text,
    created_at    timestamptz NOT NULL,
    last_used_at  timestamptz
);
CREATE INDEX oauth_clients_purge_idx ON oauth_clients (last_used_at, created_at);

CREATE TABLE oauth_authorization_codes (
    id               text        PRIMARY KEY,
    code_hash        text        NOT NULL,
    -- Metadata-document client IDs have no oauth_clients row.
    client_id        text        NOT NULL,
    client_name      text        NOT NULL,
    client_logo_uri  text,
    user_id          text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    redirect_uri     text        NOT NULL,
    scopes           text[]      NOT NULL
                       CHECK (cardinality(scopes) >= 1 AND scopes <@ ARRAY[
                         'activities:read', 'activities:write', 'devices:read', 'events:read',
                         'interactions:create', 'interactions:read', 'notifications:send',
                         'services:read', 'services:write'
                       ]::text[]),
    code_challenge   text        NOT NULL,
    resource         text,
    expires_at       timestamptz NOT NULL,
    consumed_at      timestamptz,
    created_at       timestamptz NOT NULL
);
CREATE UNIQUE INDEX oauth_authorization_codes_code_hash_key ON oauth_authorization_codes (code_hash);
CREATE INDEX        oauth_authorization_codes_expires_idx   ON oauth_authorization_codes (expires_at);
