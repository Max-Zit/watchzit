-- VXLAN tunnels of devices (the device is a VTEP): VNI, interface, its own
-- address and the remote VTEPs. Read from RouterOS and the agents; each
-- source replaces only its own rows.
CREATE TABLE device_vxlans (
    device_id  bigint NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    source     text NOT NULL,
    vni        int NOT NULL CHECK (vni BETWEEN 1 AND 16777215),
    name       text NOT NULL DEFAULT '',
    local_ip   text NOT NULL DEFAULT '',
    remotes    text[] NOT NULL DEFAULT '{}',
    port       int NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, source, vni, name)
);

-- a VXLAN VNI set by hand on a map cable (0: none)
ALTER TABLE map_links ADD COLUMN vni int NOT NULL DEFAULT 0;

-- cables of kind VXLAN tunnel
ALTER TABLE map_links DROP CONSTRAINT map_links_kind_check;
ALTER TABLE map_links ADD CONSTRAINT map_links_kind_check CHECK (kind IN ('ethernet', 'fiber', 'fiber-sm', 'dac', 'wireless', 'vpn', 'vxlan', 'wan', 'serial', 'power'));
