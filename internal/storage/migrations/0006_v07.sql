-- v0.7.0 schema additions (SPEC-v0.7): storage-usage accounts (Google
-- Drive / Dropbox, §3) and a platform column on update_jobs (§1/§6, so
-- a remote-update job records which OS family it targeted at creation
-- time — needed once macOS/Windows agents can also receive jobs).

CREATE TABLE storage_accounts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    provider    TEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    config_json TEXT NOT NULL DEFAULT '{}',
    secret_json TEXT NOT NULL DEFAULT '{}',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE INDEX idx_storage_accounts_provider ON storage_accounts (provider);

-- config_json holds non-secret per-provider fields (e.g. Google's
-- client_id, Dropbox's app key); secret_json holds secret fields (e.g.
-- Google's client_secret, a stored refresh token) as an opaque JSON
-- object, matching notify_channels' existing config-as-JSON pattern
-- but split into two columns so the hub's redaction boundary
-- (StorageAccount.Redacted, internal/models/storage.go) can omit
-- secret_json from an API response by construction rather than by a
-- field-by-field masking pass.

CREATE TABLE storage_snapshots (
    account_id      INTEGER PRIMARY KEY,
    status          TEXT NOT NULL DEFAULT 'not_configured',
    status_detail   TEXT NOT NULL DEFAULT '',
    account_email   TEXT NOT NULL DEFAULT '',
    account_name    TEXT NOT NULL DEFAULT '',
    used_bytes      INTEGER NOT NULL DEFAULT 0,
    limit_bytes     INTEGER NOT NULL DEFAULT 0,
    unlimited       INTEGER NOT NULL DEFAULT 0,
    trash_bytes     INTEGER NOT NULL DEFAULT 0,
    allocation_type TEXT NOT NULL DEFAULT '',
    collected_at    INTEGER NOT NULL DEFAULT 0,
    last_success_at INTEGER NOT NULL DEFAULT 0,
    last_attempt_at INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (account_id) REFERENCES storage_accounts (id) ON DELETE CASCADE
) WITHOUT ROWID;

-- One row per account, upserted on every poll (mirrors
-- cloud_cost_snapshots' one-row-per-provider design) — only the latest
-- + last-success timestamps are needed, not a full history table.
-- ON DELETE CASCADE means deleting a storage_accounts row (disconnect)
-- also removes its snapshot in the same statement.

ALTER TABLE update_jobs ADD COLUMN platform TEXT NOT NULL DEFAULT '';

-- platform records the target host's OS family ("linux"/"darwin"/
-- "windows") at job-creation time, copied from
-- HostInfo.RemoteUpdate.Platform (empty for a job created before this
-- column existed, or against an agent that never reported Platform).
-- SQLite's ALTER TABLE ADD COLUMN with a non-null DEFAULT is a fast
-- schema-only change (no full-table rewrite), safe to run against a
-- table that may already hold rows from a pre-v0.7.0 hub.
