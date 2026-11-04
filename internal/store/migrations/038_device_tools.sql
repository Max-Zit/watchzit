-- Which tools a device offers under "Open with", where it differs from the
-- tool's device kinds: tool ID → shown or not (Proxmox without RDP, a
-- printer with SSH).
ALTER TABLE devices ADD COLUMN access_tools jsonb NOT NULL DEFAULT '{}';
