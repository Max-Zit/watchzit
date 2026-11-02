-- Fields administrators define for devices (asset number, warranty end,
-- rack, contract, …) and their values.
CREATE TABLE custom_fields (
    id         bigserial PRIMARY KEY,
    name       text NOT NULL,
    kind       text NOT NULL DEFAULT 'text' CHECK (kind IN ('text', 'number', 'date')),
    created_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX custom_fields_name ON custom_fields (lower(name));

CREATE TABLE device_field_values (
    field_id  bigint NOT NULL REFERENCES custom_fields(id) ON DELETE CASCADE,
    device_id bigint NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    value     text NOT NULL,
    PRIMARY KEY (field_id, device_id)
);
CREATE INDEX device_field_values_device ON device_field_values (device_id);
