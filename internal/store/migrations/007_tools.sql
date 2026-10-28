-- Opening devices with local programs through the WatchZit Launcher.
-- builtin names a tool the launcher knows (ssh, rdp, winbox, vnc, telnet);
-- custom tools (builtin NULL) carry the program path and argument templates.
-- kinds limits a tool to device kinds ('{}' = every device).
CREATE TABLE tools (
    id         bigserial PRIMARY KEY,
    builtin    text UNIQUE,
    name       text NOT NULL,
    program    text NOT NULL DEFAULT '',
    args       text[] NOT NULL DEFAULT '{}',
    port       int NOT NULL CHECK (port BETWEEN 1 AND 65535),
    kinds      text[] NOT NULL DEFAULT '{}',
    enabled    boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tools_name ON tools (lower(name));

INSERT INTO tools (builtin, name, port, kinds, enabled) VALUES
    ('ssh',    'SSH',            22,   '{router,switch,firewall,server,accesspoint,other}', true),
    ('rdp',    'Remote Desktop', 3389, '{server,pc}', true),
    ('winbox', 'WinBox',         8291, '{router,switch,accesspoint}', true),
    ('vnc',    'VNC',            5900, '{server,pc}', false),
    ('telnet', 'Telnet',         23,   '{router,switch,other}', false);

-- Per device: the user name the tools sign in with (never a password) and
-- ports that differ from the tools' defaults (tool id → port).
ALTER TABLE devices
    ADD COLUMN access_user  text NOT NULL DEFAULT '',
    ADD COLUMN access_ports jsonb NOT NULL DEFAULT '{}';
