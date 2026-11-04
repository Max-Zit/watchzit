-- A device is never its own collector or backup; backups are looked up.
UPDATE devices SET collector_id = NULL WHERE collector_id = id;
UPDATE devices SET collector_backup_id = NULL WHERE collector_backup_id = id OR collector_backup_id = collector_id;
ALTER TABLE devices ADD CONSTRAINT devices_collector_not_self CHECK (collector_id IS NULL OR collector_id <> id);
ALTER TABLE devices ADD CONSTRAINT devices_backup_not_self CHECK (collector_backup_id IS NULL OR (collector_backup_id <> id AND collector_backup_id IS DISTINCT FROM collector_id));
CREATE INDEX devices_collector_backup_id ON devices(collector_backup_id) WHERE collector_backup_id IS NOT NULL;
