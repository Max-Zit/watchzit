-- Escalation and on-call: a channel can wait before it is told (only
-- problems still open and not acknowledged then), repeat while a problem
-- stays open, and be on duty only on some days and hours. channel_sent
-- remembers which channel was told about which problem.
ALTER TABLE notify_channels ADD COLUMN delay_min  integer  NOT NULL DEFAULT 0;
ALTER TABLE notify_channels ADD COLUMN repeat_min integer  NOT NULL DEFAULT 0;
ALTER TABLE notify_channels ADD COLUMN days       smallint NOT NULL DEFAULT 127;
ALTER TABLE notify_channels ADD COLUMN from_hour  smallint NOT NULL DEFAULT 0;
ALTER TABLE notify_channels ADD COLUMN to_hour    smallint NOT NULL DEFAULT 24;
ALTER TABLE notify_channels ADD COLUMN skip_acked boolean  NOT NULL DEFAULT true;

CREATE TABLE channel_sent (
    problem_id bigint NOT NULL REFERENCES problems(id) ON DELETE CASCADE,
    channel_id bigint NOT NULL REFERENCES notify_channels(id) ON DELETE CASCADE,
    sent_at    timestamptz NOT NULL,
    times      integer NOT NULL DEFAULT 1,
    PRIMARY KEY (problem_id, channel_id)
);
