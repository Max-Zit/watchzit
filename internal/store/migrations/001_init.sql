CREATE TABLE settings (
    key   text PRIMARY KEY,
    value jsonb NOT NULL
);

CREATE TABLE users (
    id                   bigserial PRIMARY KEY,
    username             text NOT NULL,
    display_name         text NOT NULL DEFAULT '',
    password_hash        text NOT NULL,
    role                 text NOT NULL CHECK (role IN ('admin', 'operator', 'viewer')),
    totp_secret          text NOT NULL DEFAULT '',
    totp_enabled         boolean NOT NULL DEFAULT false,
    totp_last_step       bigint NOT NULL DEFAULT 0,
    recovery_codes       text[] NOT NULL DEFAULT '{}',
    must_change_password boolean NOT NULL DEFAULT false,
    disabled             boolean NOT NULL DEFAULT false,
    lang                 text NOT NULL DEFAULT '',
    created_at           timestamptz NOT NULL DEFAULT now(),
    last_login_at        timestamptz
);
CREATE UNIQUE INDEX users_username ON users (lower(username));

-- Sign-in sessions; only the SHA-256 of the cookie value is stored.
-- stage 'mfa' is a password-verified sign-in waiting for the second factor.
CREATE TABLE sessions (
    token_hash text PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    stage      text NOT NULL CHECK (stage IN ('mfa', 'full')),
    created_at timestamptz NOT NULL,
    last_seen  timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    ip         text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user ON sessions (user_id);

-- Addresses blocked after failed sign-ins.
CREATE TABLE login_blocks (
    ip         text PRIMARY KEY,
    until      timestamptz NOT NULL,
    strikes    int NOT NULL,
    reason     text NOT NULL,
    updated_at timestamptz NOT NULL
);

-- Append-only, hash-chained audit log: hash = SHA-256(prev_hash, fields).
CREATE TABLE audit_log (
    id        bigserial PRIMARY KEY,
    at        timestamptz NOT NULL,
    username  text NOT NULL,
    ip        text NOT NULL,
    action    text NOT NULL,
    target    text NOT NULL,
    detail    text NOT NULL,
    prev_hash text NOT NULL,
    hash      text NOT NULL
);

-- Devices are organised in groups (a tree; maps come later).
CREATE TABLE device_groups (
    id         bigserial PRIMARY KEY,
    name       text NOT NULL,
    notes      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX device_groups_name ON device_groups (lower(name));

CREATE TABLE devices (
    id           bigserial PRIMARY KEY,
    group_id     bigint REFERENCES device_groups(id) ON DELETE SET NULL,
    name         text NOT NULL,
    address      text NOT NULL,
    kind         text NOT NULL DEFAULT 'other',
    web_url      text NOT NULL DEFAULT '',
    notes        text NOT NULL DEFAULT '',
    paused       boolean NOT NULL DEFAULT false,
    status       text NOT NULL DEFAULT 'unknown',
    status_since timestamptz NOT NULL DEFAULT now(),
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX devices_name ON devices (lower(name));

-- A check polls one thing of a device (ICMP, a TCP port, later SNMP …).
CREATE TABLE checks (
    id           bigserial PRIMARY KEY,
    device_id    bigint NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    type         text NOT NULL,
    name         text NOT NULL,
    params       jsonb NOT NULL DEFAULT '{}',
    interval_s   int NOT NULL DEFAULT 60 CHECK (interval_s BETWEEN 10 AND 3600),
    paused       boolean NOT NULL DEFAULT false,
    status       text NOT NULL DEFAULT 'unknown',
    status_since timestamptz NOT NULL DEFAULT now(),
    last_run     timestamptz,
    last_text    text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX checks_device ON checks (device_id);

-- A series is one metric of a check (rtt_ms, loss_pct, up …).
CREATE TABLE series (
    id       serial PRIMARY KEY,
    check_id bigint NOT NULL REFERENCES checks(id) ON DELETE CASCADE,
    metric   text NOT NULL,
    unit     text NOT NULL DEFAULT '',
    UNIQUE (check_id, metric)
);

-- Metric storage (internal/tsdb): new samples go to samples_hot, which is
-- partitioned by hour. Every closed hour is packed per series into one
-- compressed block in samples_raw and its partition is dropped; daily and
-- monthly jobs build the 5-minute and 1-hour rollups.
CREATE TABLE samples_hot (
    series_id int NOT NULL,
    ts        timestamptz NOT NULL,
    value     double precision NOT NULL
) PARTITION BY RANGE (ts);
CREATE TABLE samples_hot_default PARTITION OF samples_hot DEFAULT;

CREATE TABLE samples_raw (
    series_id int NOT NULL,
    hour      timestamptz NOT NULL,
    block     bytea NOT NULL,
    PRIMARY KEY (series_id, hour)
);
CREATE INDEX samples_raw_hour ON samples_raw (hour);

CREATE TABLE samples_5m (
    series_id int NOT NULL,
    day       timestamptz NOT NULL,
    block     bytea NOT NULL,
    PRIMARY KEY (series_id, day)
);
CREATE INDEX samples_5m_day ON samples_5m (day);

CREATE TABLE samples_1h (
    series_id int NOT NULL,
    month     timestamptz NOT NULL,
    block     bytea NOT NULL,
    PRIMARY KEY (series_id, month)
);
CREATE INDEX samples_1h_month ON samples_1h (month);

-- Which periods the jobs have finished (hour / day / month starts).
CREATE TABLE tsdb_marks (
    kind text PRIMARY KEY,
    upto timestamptz NOT NULL
);
