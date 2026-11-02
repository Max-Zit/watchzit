-- Error messages agents read from the Windows event logs and the systemd
-- journal, kept with the syslog messages and traps.
ALTER TABLE log_messages DROP CONSTRAINT log_messages_kind_check;
ALTER TABLE log_messages ADD CONSTRAINT log_messages_kind_check CHECK (kind IN ('syslog', 'trap', 'agent'));
