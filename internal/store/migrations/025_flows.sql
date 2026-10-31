-- Traffic flows (1.1): NetFlow, IPFIX and sFlow summed per minute and
-- exporter — the top conversations of each minute, the rest in one row
-- without addresses (src and dst NULL).
CREATE TABLE flow_minutes (
    at        timestamptz NOT NULL,
    exporter  inet NOT NULL,
    device_id bigint REFERENCES devices(id) ON DELETE CASCADE,
    src       inet,
    dst       inet,
    proto     smallint NOT NULL DEFAULT 0,
    port      integer NOT NULL DEFAULT 0,
    bytes     bigint NOT NULL,
    packets   bigint NOT NULL
);
CREATE INDEX flow_minutes_at ON flow_minutes (at);
CREATE INDEX flow_minutes_device_at ON flow_minutes (device_id, at);
