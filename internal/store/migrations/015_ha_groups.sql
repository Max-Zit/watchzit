-- HA groups on maps: devices that stand in for each other (a firewall HA
-- pair, VRRP routers, a switch stack or MLAG pair, a server cluster). The
-- members are map nodes; paths go through all of them, and a group with a
-- member down has lost its redundancy.
CREATE TABLE map_ha (
    id     bigserial PRIMARY KEY,
    map_id bigint NOT NULL REFERENCES maps(id) ON DELETE CASCADE,
    name   text NOT NULL DEFAULT '',
    kind   text NOT NULL CHECK (kind IN ('firewall', 'router', 'switch', 'server', 'storage', 'other')),
    nodes  bigint[] NOT NULL
);
CREATE INDEX map_ha_map ON map_ha (map_id);

-- cables between the members of a HA group (heartbeat, sync, stack, peer
-- link): not a way for traffic
ALTER TABLE map_links DROP CONSTRAINT map_links_kind_check;
ALTER TABLE map_links ADD CONSTRAINT map_links_kind_check CHECK (kind IN ('ethernet', 'fiber', 'fiber-sm', 'dac', 'wireless', 'vpn', 'vxlan', 'ha', 'wan', 'serial', 'power'));

-- a hypervisor cluster: more physical servers than the first (host_device)
ALTER TABLE map_nodes ADD COLUMN host_devices bigint[] NOT NULL DEFAULT '{}';

-- a problem when a HA group runs on fewer members than it has
ALTER TABLE triggers DROP CONSTRAINT triggers_kind_check;
ALTER TABLE triggers ADD CONSTRAINT triggers_kind_check CHECK (kind IN ('device_down', 'check_down', 'metric', 'ha_degraded'));
INSERT INTO triggers (name, kind, check_types, metric, op, threshold, for_s, severity) VALUES
    ('HA group without redundancy', 'ha_degraded', '{}', '', '>', 0, 60, 'warning');

-- the HA role a device reports (VRRP over SNMP or the RouterOS API):
-- master or backup, since when, and from where
ALTER TABLE devices ADD COLUMN ha_state text NOT NULL DEFAULT '',
    ADD COLUMN ha_source text NOT NULL DEFAULT '',
    ADD COLUMN ha_changed timestamptz;
