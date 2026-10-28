-- VLANs set by hand on a map cable: [{"id": 10, "tagged": true}, …]
ALTER TABLE map_links ADD COLUMN vlans jsonb NOT NULL DEFAULT '[]';

-- Web links of a device: a name for the main web interface button and
-- more links ([{"name": "iDRAC", "url": "https://…"}, …]).
ALTER TABLE devices ADD COLUMN web_name text NOT NULL DEFAULT '',
    ADD COLUMN web_links jsonb NOT NULL DEFAULT '[]';
