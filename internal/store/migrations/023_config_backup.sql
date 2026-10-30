-- Configuration backups of network devices over SSH: how to read each
-- device (secret and key sealed, the SSH host key pinned at the first
-- backup) and the versions read (a new one only when something changed).
CREATE TABLE device_backup (
    device_id  bigint PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    enabled    boolean NOT NULL DEFAULT true,
    method     text NOT NULL CHECK (method IN ('mikrotik', 'cisco', 'fortigate', 'command')),
    port       integer NOT NULL DEFAULT 22,
    username   text NOT NULL,
    secret     text NOT NULL DEFAULT '',
    key        text NOT NULL DEFAULT '',
    command    text NOT NULL DEFAULT '',
    host_key   text NOT NULL DEFAULT '',
    last_run   timestamptz,
    last_error text NOT NULL DEFAULT ''
);

CREATE TABLE config_versions (
    id        bigserial PRIMARY KEY,
    device_id bigint NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    at        timestamptz NOT NULL,
    hash      text NOT NULL,
    size      integer NOT NULL,
    added     integer NOT NULL DEFAULT 0,
    removed   integer NOT NULL DEFAULT 0,
    content   text NOT NULL
);
CREATE INDEX config_versions_device ON config_versions (device_id, at DESC);
