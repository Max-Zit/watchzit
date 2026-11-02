-- Where devices are plugged in (1.2): neighbour tables (LLDP, CDP,
-- MikroTik), MAC address tables of switches and ARP tables of routers, read
-- every 15 minutes; maps that add the links (and devices) they show by
-- themselves; cables a map added that way.
CREATE TABLE topo_neighbours (
    device_id   bigint NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    if_index    int NOT NULL DEFAULT 0,
    local_port  text NOT NULL DEFAULT '',
    name        text NOT NULL DEFAULT '',
    remote_port text NOT NULL DEFAULT '',
    remote_desc text NOT NULL DEFAULT '',
    address     inet,
    mac         text NOT NULL DEFAULT '',
    via         text NOT NULL
);
CREATE INDEX topo_neighbours_device ON topo_neighbours (device_id);
CREATE TABLE topo_fdb (
    device_id bigint NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    if_index  int NOT NULL,
    mac       text NOT NULL,
    vlan      int NOT NULL DEFAULT 0,
    PRIMARY KEY (device_id, if_index, mac, vlan)
);
CREATE INDEX topo_fdb_mac ON topo_fdb (mac);
CREATE TABLE topo_arp (
    device_id bigint NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    address   inet NOT NULL,
    mac       text NOT NULL,
    if_index  int NOT NULL DEFAULT 0,
    PRIMARY KEY (device_id, address)
);
CREATE INDEX topo_arp_mac ON topo_arp (mac);
CREATE TABLE topo_read (
    device_id bigint PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    at        timestamptz NOT NULL,
    neighbours int NOT NULL DEFAULT 0,
    fdb        int NOT NULL DEFAULT 0,
    arp        int NOT NULL DEFAULT 0
);
ALTER TABLE maps ADD COLUMN auto_links boolean NOT NULL DEFAULT false;
ALTER TABLE maps ADD COLUMN auto_devices boolean NOT NULL DEFAULT false;
ALTER TABLE maps ADD COLUMN auto_ignore jsonb NOT NULL DEFAULT '[]';
ALTER TABLE map_links ADD COLUMN auto boolean NOT NULL DEFAULT false;
ALTER TABLE map_nodes ADD COLUMN auto boolean NOT NULL DEFAULT false;
