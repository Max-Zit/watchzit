-- Things in a rack that are not built for it (a NAS, a small switch, a
-- mini PC on a shelf): their height in units and a part of the rack's
-- width, so several share the same units side by side.
ALTER TABLE map_nodes ADD COLUMN rack_h int NOT NULL DEFAULT 0 CHECK (rack_h BETWEEN 0 AND 20);
ALTER TABLE map_nodes ADD COLUMN rack_w int NOT NULL DEFAULT 0 CHECK (rack_w BETWEEN 0 AND 4);
ALTER TABLE map_nodes ADD COLUMN rack_x int NOT NULL DEFAULT 0 CHECK (rack_x BETWEEN 0 AND 3);
