-- v0.8.0 schema additions (SPEC-v0.8 §3, Part B). This migration is
-- shared scaffolding: the prep stage lays down what is certain up
-- front (see each section below); the three feature stages append
-- ONLY inside their own section so parallel worktrees merge cleanly.
-- Keep the banner comments and the blank-comment-line spacer between
-- sections intact — do not reorder sections or remove the spacers.

-- ==== 3-1 rollout ====
--
--
--
--
--
--
-- Agent rollout dashboard + update-failure alerts (SPEC-v0.8 §3-1).
-- The rollout stage owns this section: version/OS aggregation is
-- computed at read time from hosts + update_jobs (no new table needed
-- for that), but batch history needs an efficient "list recent
-- batches" query, which update_jobs' existing indexes (idx_update_jobs
-- _batch on batch_id alone, idx_update_jobs_host_state on
-- (host_id, state) — see 0005_v06.sql) do not serve well: listing
-- batches ordered by recency requires scanning by created_at, and the
-- rollout page's "GET /api/v1/agents/updates/batches?before=" cursor
-- pagination filters on (batch_id, created_at) together. This index
-- covers both without a table scan.
CREATE INDEX idx_update_jobs_batch_created ON update_jobs (batch_id, created_at);

-- The rollout stage appends any of its own new tables (e.g. a
-- materialized batch-summary cache, if it turns out update_jobs alone
-- is too slow to aggregate live) below this line, inside this section.

-- ==== 3-2 channel health ====
--
--
--
--
--
--
-- Notification channel health report (SPEC-v0.8 §3-2): weekly
-- synthetic connectivity tests per channel, plus their results.
CREATE TABLE channel_health_settings (
    channel_id    INTEGER PRIMARY KEY,
    enabled       INTEGER NOT NULL DEFAULT 0,
    weekday       INTEGER NOT NULL DEFAULT 0,
    minute_of_day INTEGER NOT NULL DEFAULT 0,
    last_run_at   INTEGER NOT NULL DEFAULT 0,
    updated_at    INTEGER NOT NULL,
    FOREIGN KEY (channel_id) REFERENCES notify_channels (id) ON DELETE CASCADE
) WITHOUT ROWID;

-- One row per channel, created/updated only once an admin opts in via
-- the channel's "weekly connection check" toggle (default off, per
-- SPEC-v0.8 §3-2); a channel with no row here has synthetic testing
-- disabled. weekday is 0=Sunday..6=Saturday and minute_of_day is
-- 0-1439, both interpreted in UTC (SPEC-v0.8 §3-2's explicit "DST 무관
-- UTC 저장" requirement) so the health scheduler never has to reason
-- about a channel's local time zone. last_run_at is the last time this
-- channel's synthetic test actually ran (0 = never), independent of
-- its outcome — see channel_synthetic_results for outcomes. ON DELETE
-- CASCADE mirrors storage_snapshots' pattern (0006_v07.sql): deleting
-- a notify_channels row removes its health settings in the same
-- statement.

CREATE INDEX idx_channel_health_settings_enabled ON channel_health_settings (enabled, weekday, minute_of_day);

-- Serves the health scheduler's "which channels are due right now"
-- query without a table scan as the channel count grows.

CREATE TABLE channel_synthetic_results (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id   INTEGER NOT NULL,
    started_at   INTEGER NOT NULL,
    finished_at  INTEGER NOT NULL DEFAULT 0,
    outcome      TEXT NOT NULL DEFAULT 'accepted',
    diagnosis_code TEXT NOT NULL DEFAULT '',
    detail       TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (channel_id) REFERENCES notify_channels (id) ON DELETE CASCADE
);

-- One row per synthetic test run (append-only history, unlike the
-- single-row-per-entity snapshot tables elsewhere in this schema,
-- since SPEC-v0.8 §3-2's health report page shows "the last 12 weeks"
-- as a strip of individual results). outcome is one of "accepted"
-- (platform accepted the send but read-back/cleanup wasn't
-- attempted or didn't apply), "verified" (read-back confirmed
-- delivery and cleanup succeeded where supported), or "failed".
-- diagnosis_code mirrors models.Diagnosis's Code field (see
-- internal/notify/diagnose.go) for a failed run; empty for
-- accepted/verified. detail is a short, already-redacted
-- human-readable string (never a channel secret — see diagnose.go's
-- redaction pass) suitable for direct display, empty when outcome is
-- "accepted". finished_at is 0 while a test is still in flight (a
-- crash mid-test leaves such a row behind; readers should treat a
-- started_at older than a healthy timeout as an implicit failure
-- rather than expecting this table to self-correct).
CREATE INDEX idx_channel_synthetic_results_channel_started
    ON channel_synthetic_results (channel_id, started_at);

-- The channel-health stage appends any of its own new tables (e.g. a
-- fallback-notification cooldown/dedup table, if delivery-failure
-- alerting needs one beyond the existing alert_rules state machine)
-- below this line, inside this section.

CREATE TABLE channel_failure_incidents (
    channel_id     INTEGER PRIMARY KEY,
    opened_at      INTEGER NOT NULL,
    notified_at    INTEGER NOT NULL DEFAULT 0,
    fallback_channel_id INTEGER,
    recovered_at   INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (channel_id) REFERENCES notify_channels (id) ON DELETE CASCADE
) WITHOUT ROWID;

-- One row per channel currently in (or most recently in) a "failing"
-- incident, used to de-duplicate the "channel X is failing" fallback
-- notification (SPEC-v0.8 §3-2: "once per incident") — a channel
-- entering ChannelFailing opens a row here (or reuses its existing
-- open one) and the fallback notifier sends at most once per open
-- incident, tracked by notified_at (0 = not yet sent, e.g. because no
-- healthy fallback channel existed at the time — see
-- ChannelHealthReport.BannerNeeded for that case). fallback_channel_id
-- records which channel actually carried the notification (nullable:
-- absent when the banner-only path was taken). recovered_at is set
-- (and, per SPEC-v0.8 §3-2's optional recovery message, may trigger a
-- second notification through the same fallback channel) once the
-- channel's health state returns to healthy/degraded; a row with
-- recovered_at != 0 is a closed incident kept for history rather than
-- deleted, so re-entering ChannelFailing later is unambiguously a new
-- incident rather than a resurrected old one — callers distinguish
-- "still the same open incident" from "a new incident" by checking
-- recovered_at == 0, not by row presence alone.

-- ==== 3-3 i18n ====
--
--
--
--
--
--
-- Dashboard/notification language (SPEC-v0.8 §3-3). The dashboard's
-- own language preference (`cp_lang`) is a browser-local setting
-- (localStorage), not hub state, so it needs no table. The hub-side
-- setting controlling which language notification message bodies and
-- chart titles are rendered in reuses the existing generic key/value
-- settings table (see internal/storage/settings.go) under the key
-- "notify_language" ("en"/"ko", default "en" when absent) rather than
-- a dedicated column or table — exactly like every other single-value
-- hub setting added since 0002_limits_settings.sql (network_config,
-- billing interval, storage interval, display currency, ...). No DDL
-- is needed for that reason.
--
-- The i18n stage appends any of its own new tables/columns below this
-- line, inside this section, only if a concrete need for server-side
-- schema arises beyond the settings-table key above.
