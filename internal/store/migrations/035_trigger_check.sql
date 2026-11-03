-- A metric, forecast, down or no-data trigger can watch one check only
-- (an SNMP OID value has its own unit, so it is watched check by check).
ALTER TABLE triggers ADD COLUMN check_id bigint REFERENCES checks(id) ON DELETE CASCADE;
