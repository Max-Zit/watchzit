-- SLA: the availability a group of devices should reach (percent; 0 =
-- none), shown in the availability report.
ALTER TABLE device_groups ADD COLUMN sla_target numeric(6,3) NOT NULL DEFAULT 0;
