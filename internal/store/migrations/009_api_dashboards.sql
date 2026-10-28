-- REST API tokens: each belongs to a user and has that user's role; only a
-- SHA-256 of the token is stored.
CREATE TABLE api_tokens (
    id         bigserial PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       text NOT NULL,
    token_hash text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz,
    last_used  timestamptz,
    last_ip    text NOT NULL DEFAULT ''
);
CREATE INDEX api_tokens_user ON api_tokens (user_id);

-- Dashboards: an ordered list of widgets (graphs, values, device status,
-- problems, maps) shown in a grid.
CREATE TABLE dashboards (
    id         bigserial PRIMARY KEY,
    name       text NOT NULL,
    widgets    jsonb NOT NULL DEFAULT '[]',
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX dashboards_name ON dashboards (lower(name));
