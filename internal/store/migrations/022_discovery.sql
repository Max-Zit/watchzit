-- Network discovery: address ranges scanned now or on a schedule, and the
-- hosts found that are not devices yet.
CREATE TABLE discovery_ranges (
    id         bigserial PRIMARY KEY,
    cidr       text NOT NULL UNIQUE,
    every      text NOT NULL DEFAULT '' CHECK (every IN ('', 'day', 'week')),
    last_run   timestamptz,
    last_found integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE discovered_hosts (
    address    inet PRIMARY KEY,
    range_id   bigint REFERENCES discovery_ranges(id) ON DELETE CASCADE,
    name       text NOT NULL DEFAULT '',
    dns_name   text NOT NULL DEFAULT '',
    sys_name   text NOT NULL DEFAULT '',
    sys_descr  text NOT NULL DEFAULT '',
    vendor     text NOT NULL DEFAULT '',
    kind       text NOT NULL DEFAULT 'other',
    ports      integer[] NOT NULL DEFAULT '{}',
    ping       boolean NOT NULL DEFAULT false,
    snmp_profile bigint REFERENCES snmp_profiles(id) ON DELETE SET NULL,
    first_seen timestamptz NOT NULL,
    last_seen  timestamptz NOT NULL,
    dismissed  boolean NOT NULL DEFAULT false
);
