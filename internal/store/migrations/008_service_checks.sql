-- Service checks (0.7): triggers for certificates, and RouterOS values in
-- the CPU and memory triggers that already exist.
INSERT INTO triggers (name, kind, check_types, metric, op, threshold, for_s, severity) VALUES
    ('Certificate expires soon', 'metric', '{http,tls}', 'cert_days', '<', 21, 0, 'warning'),
    ('Certificate expires',      'metric', '{http,tls}', 'cert_days', '<', 7,  0, 'critical'),
    ('High temperature',         'metric', '{routeros}', 'temperature', '>', 70, 300, 'warning');
UPDATE triggers SET check_types = check_types || '{routeros}'
    WHERE kind = 'metric' AND metric IN ('cpu_pct', 'mem_pct') AND check_types <> '{}' AND NOT 'routeros' = ANY(check_types);
