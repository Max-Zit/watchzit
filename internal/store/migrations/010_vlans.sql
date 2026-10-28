-- VLANs of devices: which VLANs each port carries, tagged or untagged.
-- Read from SNMP (Q-BRIDGE-MIB) or the RouterOS API; each source replaces
-- only its own rows. if_index 0 is a VLAN the device knows on no port.
CREATE TABLE device_vlans (
    device_id  bigint NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    source     text NOT NULL,
    vlan_id    int NOT NULL CHECK (vlan_id BETWEEN 1 AND 4094),
    if_index   int NOT NULL DEFAULT 0,
    name       text NOT NULL DEFAULT '',
    tagged     boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, source, vlan_id, if_index)
);
