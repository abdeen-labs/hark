-- ── OAuth ──────────────────────────────────────────────────────────────────

-- Dynamically registered clients (RFC 7591). Every client is public and
-- authenticates with PKCE alone, so there is no secret here to hash. A client
-- identified by a metadata document never has a row: only registrations do.
CREATE TABLE oauth_clients (
    id            text        PRIMARY KEY,
    name          text        NOT NULL,
    -- cardinality rather than array_length: the latter is NULL for an empty
    -- array, which a CHECK lets through.
    redirect_uris text[]      NOT NULL CHECK (cardinality(redirect_uris) >= 1),
    client_uri    text,
    logo_uri      text,
    created_at    timestamptz NOT NULL,
    -- Stamped when a code the client obtained is exchanged. NULL means the
    -- registration never completed an authorization, which is what the
    -- opportunistic purge keys off.
    last_used_at  timestamptz
);
CREATE INDEX oauth_clients_purge_idx ON oauth_clients (last_used_at, created_at);

-- Authorization codes. The code is stored only as a digest; the client holds
-- the plaintext and presents it once at the token endpoint. client_id is not a
-- foreign key because it may name a metadata document rather than a row above.
CREATE TABLE oauth_authorization_codes (
    id               text        PRIMARY KEY,
    code_hash        text        NOT NULL,
    client_id        text        NOT NULL,
    -- Captured at consent, so the issued token keeps the name the owner saw
    -- even if the client's document changes before the exchange.
    client_name      text        NOT NULL,
    user_id          text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    redirect_uri     text        NOT NULL,
    scopes           text[]      NOT NULL
                       CHECK (cardinality(scopes) >= 1 AND scopes <@ ARRAY[
                         'activities:read', 'activities:write', 'devices:read', 'events:read',
                         'interactions:create', 'interactions:read', 'notifications:send',
                         'services:read', 'services:write'
                       ]::text[]),
    -- The PKCE challenge the exchange must satisfy (RFC 7636, S256).
    code_challenge   text        NOT NULL,
    -- The RFC 8707 resource the request named, when it named one.
    resource         text,
    -- TTL of the code itself. The token it issues has no expiry; the owner
    -- revokes it.
    expires_at       timestamptz NOT NULL,
    -- Stamped by the guarded consume: a code is spent by its first presentation.
    consumed_at      timestamptz,
    created_at       timestamptz NOT NULL
);
CREATE UNIQUE INDEX oauth_authorization_codes_code_hash_key ON oauth_authorization_codes (code_hash);
CREATE INDEX        oauth_authorization_codes_expires_idx   ON oauth_authorization_codes (expires_at);
