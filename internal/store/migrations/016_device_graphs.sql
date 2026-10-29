-- The graphs of a device as its user arranged them: panels with their
-- series, kind, size and thresholds (JSON); without a row the console
-- draws one graph per check and unit.
CREATE TABLE device_graphs (
    device_id  bigint PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    layout     jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
