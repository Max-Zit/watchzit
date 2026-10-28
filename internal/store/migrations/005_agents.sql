-- WatchZit agents. A computer joins with an enrollment code (token); the
-- agent is then known by the ed25519 key of its TLS client certificate.
CREATE TABLE enroll_tokens (
    id         bigserial PRIMARY KEY,
    name       text NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    group_id   bigint REFERENCES device_groups(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by text NOT NULL DEFAULT '',
    expires_at timestamptz,
    max_uses   int,
    uses       int NOT NULL DEFAULT 0,
    revoked    boolean NOT NULL DEFAULT false
);

ALTER TABLE devices
    ADD COLUMN agent_key       bytea UNIQUE,
    ADD COLUMN agent_id        text NOT NULL DEFAULT '',
    ADD COLUMN agent_version   text NOT NULL DEFAULT '',
    ADD COLUMN agent_os        text NOT NULL DEFAULT '',
    ADD COLUMN agent_seen      timestamptz,
    ADD COLUMN agent_ip        text NOT NULL DEFAULT '',
    ADD COLUMN agent_online    boolean NOT NULL DEFAULT false,
    ADD COLUMN agent_inventory jsonb NOT NULL DEFAULT '{}';
