-- What a picture of the library shows: manufacturer, type, rack units,
-- default width and the port positions (picture coordinates, port 1
-- first; the last "fiber" ports are SFP cages).
ALTER TABLE map_assets ADD COLUMN meta jsonb NOT NULL DEFAULT '{}';
