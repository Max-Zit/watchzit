-- Machines without an agent (Linux over SSH, Windows over WinRM) have the
-- agent's values: the processor, memory and disk triggers that watch the
-- agent's checks watch them too.
UPDATE triggers SET check_types = check_types || '{ssh_host,winrm}'
    WHERE kind IN ('metric', 'forecast') AND metric IN ('cpu_pct', 'mem_pct', 'used_pct')
      AND check_types && '{agent_cpu,agent_mem,agent_disk}'::text[]
      AND NOT 'ssh_host' = ANY(check_types);
