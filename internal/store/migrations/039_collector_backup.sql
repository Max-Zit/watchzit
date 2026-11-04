-- A device can have a backup collector that takes over its checks while
-- its own collector is offline, and a discovery range can be scanned by a
-- remote collector (the devices it finds are then run by it).
ALTER TABLE devices ADD COLUMN collector_backup_id bigint REFERENCES devices(id) ON DELETE SET NULL;
ALTER TABLE discovery_ranges ADD COLUMN collector_id bigint REFERENCES devices(id) ON DELETE SET NULL;
ALTER TABLE discovered_hosts ADD COLUMN collector_id bigint REFERENCES devices(id) ON DELETE SET NULL;
