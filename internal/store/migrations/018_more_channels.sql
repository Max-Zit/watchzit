-- More notification channels: Discord, Telegram, ntfy, PagerDuty, SMS
-- through Twilio or an HTTP gateway.
ALTER TABLE notify_channels DROP CONSTRAINT notify_channels_kind_check;
ALTER TABLE notify_channels ADD CONSTRAINT notify_channels_kind_check
    CHECK (kind IN ('email', 'webhook', 'slack', 'teams', 'discord', 'telegram', 'ntfy', 'pagerduty', 'twilio', 'smsgw'));
