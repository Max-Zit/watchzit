-- Anomaly triggers: a value far from what is usual for that hour of the
-- week (learned from the last four weeks). op '<>' = above or below;
-- threshold = how many typical deviations count as unusual.
ALTER TABLE triggers DROP CONSTRAINT triggers_kind_check;
ALTER TABLE triggers ADD CONSTRAINT triggers_kind_check CHECK (kind IN ('device_down', 'check_down', 'metric', 'ha_degraded', 'log', 'forecast', 'nodata', 'anomaly'));
ALTER TABLE triggers DROP CONSTRAINT triggers_op_check;
ALTER TABLE triggers ADD CONSTRAINT triggers_op_check CHECK (op IN ('>', '<', '<>'));
