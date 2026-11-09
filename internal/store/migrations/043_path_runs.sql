-- Network path checks (traceroute): every run's hops, kept 30 days.
CREATE TABLE path_runs (
    check_id bigint NOT NULL REFERENCES checks(id) ON DELETE CASCADE,
    at       timestamptz NOT NULL,
    changed  boolean NOT NULL DEFAULT false,
    hops     jsonb NOT NULL,
    PRIMARY KEY (check_id, at)
);
CREATE INDEX path_runs_changed ON path_runs (check_id, at) WHERE changed;
INSERT INTO triggers (name, kind, check_types, metric, op, threshold, for_s, severity) VALUES
    ('Loss on the network path', 'metric', '{trace}', 'loss_pct', '>', 20, 600, 'warning');
