CREATE TABLE alert_rules (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL,
    enabled         INTEGER NOT NULL DEFAULT 1,
    metric          TEXT NOT NULL,
    host_id         TEXT NOT NULL DEFAULT '',
    operator        TEXT NOT NULL,
    threshold       REAL NOT NULL,
    duration_sec    INTEGER NOT NULL DEFAULT 0,
    cooldown_sec    INTEGER NOT NULL DEFAULT 3600,
    notify_resolved INTEGER NOT NULL DEFAULT 0,
    channel_ids     TEXT NOT NULL DEFAULT '[]',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE TABLE notify_channels (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    type       TEXT NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    config     TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE alert_state (
    rule_id       INTEGER NOT NULL,
    host_id       TEXT NOT NULL,
    state         TEXT NOT NULL DEFAULT 'ok',
    since         INTEGER NOT NULL DEFAULT 0,
    last_notified INTEGER NOT NULL DEFAULT 0,
    last_value    REAL NOT NULL DEFAULT 0,
    PRIMARY KEY (rule_id, host_id)
) WITHOUT ROWID;

CREATE TABLE alert_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id     INTEGER NOT NULL,
    rule_name   TEXT NOT NULL DEFAULT '',
    host_id     TEXT NOT NULL DEFAULT '',
    hostname    TEXT NOT NULL DEFAULT '',
    metric      TEXT NOT NULL DEFAULT '',
    state       TEXT NOT NULL,
    value       REAL NOT NULL DEFAULT 0,
    threshold   REAL NOT NULL DEFAULT 0,
    started_at  INTEGER NOT NULL,
    notified_at INTEGER NOT NULL DEFAULT 0,
    resolved_at INTEGER NOT NULL DEFAULT 0,
    deliveries  TEXT NOT NULL DEFAULT '[]'
);

CREATE INDEX idx_alert_events_started_at ON alert_events (started_at);
CREATE INDEX idx_alert_events_state ON alert_events (state);
CREATE INDEX idx_alert_events_rule_host ON alert_events (rule_id, host_id, state);

CREATE TABLE host_inventory (
    host_id      TEXT PRIMARY KEY,
    collected_at INTEGER NOT NULL,
    json         TEXT NOT NULL
) WITHOUT ROWID;

-- Seed default rules replicating the pre-v0.5.0 egress alerting
-- behaviour: three egress_out_pct rules (warning/critical/exceeded),
-- enabled, with cooldown 0 (the engine special-cases egress_* metrics to
-- fire at most once per calendar month per threshold regardless of
-- cooldown/resolution). If a webhook URL is already configured (the
-- "alert_webhook_url" setting, set from CP_ALERT_WEBHOOK_URL at hub
-- startup before migrations run, or previously via the settings API), a
-- "Default webhook" notify_channels row is created from it and attached
-- to all three seeded rules; otherwise the rules are seeded with no
-- channels (channel_ids '[]') until the operator adds one.
INSERT INTO notify_channels (name, type, enabled, config, created_at, updated_at)
SELECT 'Default webhook', 'webhook', 1,
       '{"webhook_url":"' || REPLACE(REPLACE(value, '\', '\\'), '"', '\"') || '"}',
       strftime('%s','now'), strftime('%s','now')
FROM settings WHERE key = 'alert_webhook_url' AND value <> '';

INSERT INTO alert_rules (name, enabled, metric, host_id, operator, threshold, duration_sec, cooldown_sec, notify_resolved, channel_ids, created_at, updated_at)
SELECT 'Outbound traffic 80% (warning)', 1, 'egress_out_pct', '', '>=', 80, 0, 0, 0,
       CASE WHEN (SELECT id FROM notify_channels WHERE name = 'Default webhook') IS NOT NULL
            THEN '[' || (SELECT id FROM notify_channels WHERE name = 'Default webhook') || ']'
            ELSE '[]' END,
       strftime('%s','now'), strftime('%s','now');

INSERT INTO alert_rules (name, enabled, metric, host_id, operator, threshold, duration_sec, cooldown_sec, notify_resolved, channel_ids, created_at, updated_at)
SELECT 'Outbound traffic 95% (critical)', 1, 'egress_out_pct', '', '>=', 95, 0, 0, 0,
       CASE WHEN (SELECT id FROM notify_channels WHERE name = 'Default webhook') IS NOT NULL
            THEN '[' || (SELECT id FROM notify_channels WHERE name = 'Default webhook') || ']'
            ELSE '[]' END,
       strftime('%s','now'), strftime('%s','now');

INSERT INTO alert_rules (name, enabled, metric, host_id, operator, threshold, duration_sec, cooldown_sec, notify_resolved, channel_ids, created_at, updated_at)
SELECT 'Outbound traffic 100% (exceeded)', 1, 'egress_out_pct', '', '>=', 100, 0, 0, 0,
       CASE WHEN (SELECT id FROM notify_channels WHERE name = 'Default webhook') IS NOT NULL
            THEN '[' || (SELECT id FROM notify_channels WHERE name = 'Default webhook') || ']'
            ELSE '[]' END,
       strftime('%s','now'), strftime('%s','now');
