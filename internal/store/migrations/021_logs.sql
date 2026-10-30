-- Syslog messages and SNMP traps that devices send to WatchZit, and
-- triggers that open problems from them (kind "log": metric holds the text
-- to look for, threshold the least important syslog level, check_types the
-- source: syslog, trap or both; for_s how long the problem stays open after
-- the last matching message).
CREATE TABLE log_messages (
    id        bigserial PRIMARY KEY,
    at        timestamptz NOT NULL,
    device_id bigint REFERENCES devices(id) ON DELETE SET NULL,
    source    inet NOT NULL,
    kind      text NOT NULL CHECK (kind IN ('syslog', 'trap')),
    facility  smallint NOT NULL DEFAULT -1,
    severity  smallint NOT NULL DEFAULT 6,
    host      text NOT NULL DEFAULT '',
    tag       text NOT NULL DEFAULT '',
    message   text NOT NULL
);
CREATE INDEX log_messages_at ON log_messages (at DESC);
CREATE INDEX log_messages_device ON log_messages (device_id, at DESC);

ALTER TABLE triggers DROP CONSTRAINT triggers_kind_check;
ALTER TABLE triggers ADD CONSTRAINT triggers_kind_check CHECK (kind IN ('device_down', 'check_down', 'metric', 'ha_degraded', 'log'));
