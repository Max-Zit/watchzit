-- Data sources: databases and services outside WatchZit whose time series
-- graphs and dashboards can show (PostgreSQL, MySQL/MariaDB, SQL Server,
-- Prometheus, InfluxDB, JSON over HTTP). The password or token is sealed
-- with the secret key.
CREATE TABLE datasources (
    id         bigserial PRIMARY KEY,
    name       text NOT NULL UNIQUE,
    kind       text NOT NULL CHECK (kind IN ('postgres', 'mysql', 'mssql', 'prometheus', 'influxdb', 'json')),
    address    text NOT NULL,
    database   text NOT NULL DEFAULT '',
    username   text NOT NULL DEFAULT '',
    secret     text NOT NULL DEFAULT '',
    tls        text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
