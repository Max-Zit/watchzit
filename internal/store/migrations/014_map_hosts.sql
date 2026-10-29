-- Hypervisor hosts on maps: a box (kind host) with its virtual machines
-- inside (their parent), the hypervisor type as its picture and,
-- optionally, the physical server it runs on.
ALTER TABLE map_nodes DROP CONSTRAINT map_nodes_kind_check;
ALTER TABLE map_nodes ADD CONSTRAINT map_nodes_kind_check CHECK (kind IN ('device', 'submap', 'label', 'item', 'rack', 'host'));
ALTER TABLE map_nodes ADD COLUMN host_device bigint REFERENCES devices(id) ON DELETE SET NULL;
