-- Alerts: triggers turn check results into problems; problems are sent to
-- notification channels unless a maintenance window or a down parent
-- device (dependency) suppresses them.

-- A device can depend on another (the router or switch it is behind):
-- while the parent is down, the device's problems are only "unreachable".
ALTER TABLE devices ADD COLUMN parent_id bigint REFERENCES devices(id) ON DELETE SET NULL;

-- kind: device_down (the whole device), check_down (one check), metric
-- (a value of a check above or below a threshold). Scope: every device, a
-- group or one device; check_types limits metric triggers ('{}' = any check
-- with the metric).
CREATE TABLE triggers (
    id          bigserial PRIMARY KEY,
    name        text NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('device_down', 'check_down', 'metric')),
    check_types text[] NOT NULL DEFAULT '{}',
    metric      text NOT NULL DEFAULT '',
    op          text NOT NULL DEFAULT '>' CHECK (op IN ('>', '<')),
    threshold   double precision NOT NULL DEFAULT 0,
    for_s       int NOT NULL DEFAULT 0 CHECK (for_s BETWEEN 0 AND 86400),
    severity    text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    group_id    bigint REFERENCES device_groups(id) ON DELETE CASCADE,
    device_id   bigint REFERENCES devices(id) ON DELETE CASCADE,
    enabled     boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now()
);

INSERT INTO triggers (name, kind, check_types, metric, op, threshold, for_s, severity) VALUES
    ('Device down',         'device_down', '{}', '', '>', 0, 60, 'critical'),
    ('Check down',          'check_down',  '{}', '', '>', 0, 60, 'warning'),
    ('High CPU load',       'metric', '{snmp_cpu,agent_cpu}', 'cpu_pct', '>', 90, 300, 'warning'),
    ('Memory almost full',  'metric', '{agent_mem}', 'mem_pct', '>', 90, 300, 'warning'),
    ('Disk almost full',    'metric', '{agent_disk}', 'used_pct', '>', 90, 0, 'warning'),
    ('Disk full',           'metric', '{agent_disk}', 'used_pct', '>', 97, 0, 'critical'),
    ('Packet loss',         'metric', '{icmp}', 'loss_pct', '>', 20, 300, 'warning');

-- A problem is open while resolved_at is NULL; key identifies what it is
-- about (trigger, check, metric), so one condition has one open problem.
-- suppressed: '' (sent), 'maintenance' or 'dependency'.
CREATE TABLE problems (
    id          bigserial PRIMARY KEY,
    key         text NOT NULL,
    trigger_id  bigint REFERENCES triggers(id) ON DELETE SET NULL,
    device_id   bigint NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    check_id    bigint REFERENCES checks(id) ON DELETE CASCADE,
    severity    text NOT NULL,
    title       text NOT NULL,
    detail      text NOT NULL DEFAULT '',
    started_at  timestamptz NOT NULL,
    resolved_at timestamptz,
    suppressed  text NOT NULL DEFAULT '',
    notified_at timestamptz,
    ack_at      timestamptz,
    ack_by      text NOT NULL DEFAULT '',
    ack_note    text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX problems_open_key ON problems (key) WHERE resolved_at IS NULL;
CREATE INDEX problems_device ON problems (device_id, started_at DESC);
CREATE INDEX problems_started ON problems (started_at DESC);

-- Maintenance windows: problems of the devices (and groups) inside are
-- recorded but not sent.
CREATE TABLE maintenance (
    id         bigserial PRIMARY KEY,
    name       text NOT NULL,
    starts_at  timestamptz NOT NULL,
    ends_at    timestamptz NOT NULL CHECK (ends_at > starts_at),
    device_ids bigint[] NOT NULL DEFAULT '{}',
    group_ids  bigint[] NOT NULL DEFAULT '{}',
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Where problems are sent. target: email recipients (comma separated) or
-- the sealed URL; secret: sealed HMAC key of a webhook.
CREATE TABLE notify_channels (
    id           bigserial PRIMARY KEY,
    name         text NOT NULL,
    kind         text NOT NULL CHECK (kind IN ('email', 'webhook', 'slack', 'teams')),
    target       text NOT NULL,
    secret       text NOT NULL DEFAULT '',
    min_severity text NOT NULL DEFAULT 'warning' CHECK (min_severity IN ('info', 'warning', 'critical')),
    on_resolve   boolean NOT NULL DEFAULT true,
    enabled      boolean NOT NULL DEFAULT true,
    last_sent    timestamptz,
    last_error   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX notify_channels_name ON notify_channels (lower(name));
