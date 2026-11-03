-- Remote collectors: an agent with collector = true runs the checks of the
-- devices whose collector_id points at it (from its own network) and sends
-- the results; when it is removed, the console polls them again.
ALTER TABLE devices ADD COLUMN collector boolean NOT NULL DEFAULT false;
ALTER TABLE devices ADD COLUMN collector_id bigint REFERENCES devices(id) ON DELETE SET NULL;
CREATE INDEX devices_collector_id ON devices(collector_id) WHERE collector_id IS NOT NULL;
