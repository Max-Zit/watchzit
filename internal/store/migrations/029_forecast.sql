-- Forecast triggers: a value's trend reaches a threshold within a time
-- (for_s holds that time).
ALTER TABLE triggers DROP CONSTRAINT triggers_kind_check;
ALTER TABLE triggers ADD CONSTRAINT triggers_kind_check CHECK (kind IN ('device_down', 'check_down', 'metric', 'ha_degraded', 'log', 'forecast'));
ALTER TABLE triggers DROP CONSTRAINT triggers_for_s_check;
ALTER TABLE triggers ADD CONSTRAINT triggers_for_s_check CHECK (for_s BETWEEN 0 AND 31536000);
INSERT INTO triggers (name, kind, check_types, metric, op, threshold, for_s, severity) VALUES
    ('Disk full within two weeks', 'forecast', '{agent_disk}', 'used_pct', '>', 98, 1209600, 'warning');
