-- Ready triggers for the printer supplies and UPSs discovery now finds
-- (a UPS on battery is a check down already).
INSERT INTO triggers (name, kind, check_types, metric, op, threshold, for_s, severity) VALUES
    ('Toner or ink low', 'metric', '{snmp_supply}', 'level_pct', '<', 10, 0, 'warning'),
    ('UPS runtime short', 'metric', '{snmp_ups}', 'runtime_min', '<', 10, 60, 'critical');
