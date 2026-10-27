-- SNMP credentials: one profile is used by many devices. Secrets are
-- sealed with the server key (store/secret.go) and never shown again.
CREATE TABLE snmp_profiles (
    id         bigserial PRIMARY KEY,
    name       text NOT NULL UNIQUE,
    version    int NOT NULL CHECK (version IN (1, 2, 3)),
    port       int NOT NULL DEFAULT 161 CHECK (port BETWEEN 1 AND 65535),
    community  text NOT NULL DEFAULT '',
    v3_user    text NOT NULL DEFAULT '',
    auth_proto text NOT NULL DEFAULT '',
    auth_pass  text NOT NULL DEFAULT '',
    priv_proto text NOT NULL DEFAULT '',
    priv_pass  text NOT NULL DEFAULT '',
    context    text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE devices
    ADD COLUMN snmp_profile_id bigint REFERENCES snmp_profiles(id) ON DELETE SET NULL,
    -- what the device says about itself (sysName, sysDescr, sysObjectID, …)
    ADD COLUMN facts jsonb NOT NULL DEFAULT '{}';

-- Interfaces found on a device (SNMP now, the agent later); the map
-- links devices through them.
CREATE TABLE device_interfaces (
    device_id  bigint NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    if_index   int NOT NULL,
    name       text NOT NULL DEFAULT '',
    alias      text NOT NULL DEFAULT '',
    descr      text NOT NULL DEFAULT '',
    if_type    int NOT NULL DEFAULT 0,
    speed_bps  bigint NOT NULL DEFAULT 0,
    mac        text NOT NULL DEFAULT '',
    admin_up   boolean NOT NULL DEFAULT false,
    oper_up    boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, if_index)
);
