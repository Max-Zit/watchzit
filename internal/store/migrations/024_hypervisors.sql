-- Hypervisors (1.1): what a hypervisor check (vSphere, Proxmox VE) or a
-- Hyper-V agent read last, hosts that show their machines on maps by
-- themselves, and triggers for full datastores and hosts that are down.
CREATE TABLE hv_inventory (
    device_id bigint PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    source    text NOT NULL,
    at        timestamptz NOT NULL,
    data      jsonb NOT NULL
);
ALTER TABLE map_nodes ADD COLUMN auto_vms boolean NOT NULL DEFAULT false;
INSERT INTO triggers (name, kind, check_types, metric, op, threshold, for_s, severity) VALUES
    ('Datastore almost full',  'metric', '{hypervisor}', 'ds_used_pct', '>', 90, 0,   'warning'),
    ('Hypervisor host down',   'metric', '{hypervisor}', 'hosts_down',  '>', 0,  120, 'critical');
UPDATE triggers SET check_types = check_types || '{hypervisor}'
    WHERE kind = 'metric' AND metric IN ('cpu_pct', 'mem_pct') AND check_types <> '{}' AND NOT 'hypervisor' = ANY(check_types);
