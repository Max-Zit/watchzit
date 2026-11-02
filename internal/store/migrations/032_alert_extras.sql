-- Recurring maintenance windows (every day, week or month, optionally
-- until a date) and "no data" triggers.
ALTER TABLE maintenance ADD COLUMN repeat text NOT NULL DEFAULT '' CHECK (repeat IN ('', 'daily', 'weekly', 'monthly'));
ALTER TABLE maintenance ADD COLUMN repeat_until timestamptz;
ALTER TABLE triggers DROP CONSTRAINT triggers_kind_check;
ALTER TABLE triggers ADD CONSTRAINT triggers_kind_check CHECK (kind IN ('device_down', 'check_down', 'metric', 'ha_degraded', 'log', 'forecast', 'nodata'));
