-- Map design: uploaded pictures (backgrounds and device icons), racks,
-- passive items (patch panels, internet, buildings), cable kinds, bends
-- and the port on a device picture each cable ends in; kiosk displays.

CREATE TABLE map_assets (
    id         bigserial PRIMARY KEY,
    kind       text NOT NULL CHECK (kind IN ('background', 'icon')),
    name       text NOT NULL,
    mime       text NOT NULL,
    data       bytea NOT NULL,
    width      int NOT NULL DEFAULT 0,
    height     int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE maps
    ADD COLUMN bg_asset   bigint REFERENCES map_assets(id) ON DELETE SET NULL,
    ADD COLUMN bg_x       double precision NOT NULL DEFAULT 0,
    ADD COLUMN bg_y       double precision NOT NULL DEFAULT 0,
    ADD COLUMN bg_w       double precision NOT NULL DEFAULT 1000,
    ADD COLUMN bg_opacity real NOT NULL DEFAULT 1;

ALTER TABLE map_nodes DROP CONSTRAINT map_nodes_kind_check;
ALTER TABLE map_nodes ADD CONSTRAINT map_nodes_kind_check CHECK (kind IN ('device', 'submap', 'label', 'item', 'rack'));
ALTER TABLE map_nodes
    ADD COLUMN icon   text NOT NULL DEFAULT '',   -- '' = kind symbol, 'lib:<name>' or 'asset:<id>'
    ADD COLUMN size   double precision NOT NULL DEFAULT 0, -- shown width (0 = the picture's default)
    ADD COLUMN parent bigint REFERENCES map_nodes(id) ON DELETE CASCADE, -- the rack it is mounted in
    ADD COLUMN u      int NOT NULL DEFAULT 0,     -- rack: height in units; mounted: lowest unit
    ADD COLUMN face   text NOT NULL DEFAULT '';   -- reserved (front/rear)

ALTER TABLE map_links DROP CONSTRAINT map_links_kind_check;
UPDATE map_links SET kind = 'ethernet' WHERE kind = 'wired';
ALTER TABLE map_links ALTER COLUMN kind SET DEFAULT 'ethernet';
ALTER TABLE map_links ADD CONSTRAINT map_links_kind_check CHECK (kind IN ('ethernet', 'fiber', 'fiber-sm', 'dac', 'wireless', 'vpn', 'wan', 'serial', 'power'));
ALTER TABLE map_links
    ADD COLUMN route  text NOT NULL DEFAULT 'straight' CHECK (route IN ('straight', 'ortho', 'diagonal', 'curve')),
    ADD COLUMN points jsonb NOT NULL DEFAULT '[]', -- bend points [[x,y],…]
    ADD COLUMN a_slot int NOT NULL DEFAULT 0,      -- port on the picture (1 = first)
    ADD COLUMN b_slot int NOT NULL DEFAULT 0;

-- Kiosk displays: a secret link shows maps without signing in (read only),
-- e.g. on a monitoring TV. Only a hash of the token is kept.
CREATE TABLE kiosks (
    id         bigserial PRIMARY KEY,
    name       text NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    maps       bigint[] NOT NULL DEFAULT '{}',
    rotate_s   int NOT NULL DEFAULT 30 CHECK (rotate_s BETWEEN 5 AND 3600),
    theme      text NOT NULL DEFAULT 'dark' CHECK (theme IN ('dark', 'light')),
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen  timestamptz,
    last_ip    text NOT NULL DEFAULT ''
);
