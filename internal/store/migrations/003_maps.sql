-- Network maps (The Dude-like diagrams that are live monitoring). A map
-- holds nodes (devices, links to submaps, text labels) and links between
-- nodes, optionally bound to an interface on each end.
CREATE TABLE maps (
    id         bigserial PRIMARY KEY,
    name       text NOT NULL UNIQUE,
    notes      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE map_nodes (
    id        bigserial PRIMARY KEY,
    map_id    bigint NOT NULL REFERENCES maps(id) ON DELETE CASCADE,
    kind      text NOT NULL CHECK (kind IN ('device', 'submap', 'label')),
    device_id bigint REFERENCES devices(id) ON DELETE CASCADE,
    submap_id bigint REFERENCES maps(id) ON DELETE CASCADE,
    label     text NOT NULL DEFAULT '',
    x         double precision NOT NULL DEFAULT 0,
    y         double precision NOT NULL DEFAULT 0,
    CHECK ((kind = 'device') = (device_id IS NOT NULL)),
    CHECK ((kind = 'submap') = (submap_id IS NOT NULL))
);
CREATE INDEX map_nodes_map ON map_nodes (map_id);
CREATE UNIQUE INDEX map_nodes_device ON map_nodes (map_id, device_id) WHERE device_id IS NOT NULL;

CREATE TABLE map_links (
    id      bigserial PRIMARY KEY,
    map_id  bigint NOT NULL REFERENCES maps(id) ON DELETE CASCADE,
    a_node  bigint NOT NULL REFERENCES map_nodes(id) ON DELETE CASCADE,
    b_node  bigint NOT NULL REFERENCES map_nodes(id) ON DELETE CASCADE,
    a_if    int,   -- ifIndex on the device of a_node
    b_if    int,
    kind    text NOT NULL DEFAULT 'wired' CHECK (kind IN ('wired', 'wireless', 'vpn')),
    label   text NOT NULL DEFAULT '',
    CHECK (a_node <> b_node)
);
CREATE INDEX map_links_map ON map_links (map_id);

-- The latest value of every metric of a check, for maps and lists
-- (graphs read the metric store).
ALTER TABLE checks ADD COLUMN last_values jsonb NOT NULL DEFAULT '{}';
