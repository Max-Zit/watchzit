-- Templates: a set of checks for a kind of device (Linux server, MikroTik
-- router, printer …), applied to devices by hand, to many at once, or by
-- itself to new devices of the template's kinds.
CREATE TABLE templates (
    id         bigserial PRIMARY KEY,
    name       text NOT NULL,
    kinds      text[] NOT NULL DEFAULT '{}',
    auto       boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX templates_name ON templates (lower(name));

CREATE TABLE template_checks (
    id          bigserial PRIMARY KEY,
    template_id bigint NOT NULL REFERENCES templates(id) ON DELETE CASCADE,
    type        text NOT NULL,
    name        text NOT NULL DEFAULT '',
    params      jsonb NOT NULL DEFAULT '{}',
    interval_s  int NOT NULL DEFAULT 60
);
CREATE INDEX template_checks_template ON template_checks (template_id);

CREATE TABLE device_templates (
    device_id   bigint NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    template_id bigint NOT NULL REFERENCES templates(id) ON DELETE CASCADE,
    applied_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, template_id)
);
