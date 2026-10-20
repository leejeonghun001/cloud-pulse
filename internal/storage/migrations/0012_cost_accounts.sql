-- Cloud cost snapshots become per account: (provider, account_id) is
-- the key, so several OCI tenancies / AWS accounts each keep their own
-- row. account_id '' is the hub's own CLI-configured account. An
-- existing agent-reported row has no known account yet: it gets
-- 'host:<source_host>' until that agent reports its real account ID,
-- which replaces it. label is the user's display name.
CREATE TABLE cloud_cost_snapshots_v2 (
    provider        TEXT NOT NULL,
    account_id      TEXT NOT NULL DEFAULT '',
    label           TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'not_configured',
    status_detail   TEXT NOT NULL DEFAULT '',
    currency        TEXT NOT NULL DEFAULT '',
    mtd_cost        REAL NOT NULL DEFAULT 0,
    forecast_cost   REAL NOT NULL DEFAULT 0,
    forecast_method TEXT NOT NULL DEFAULT '',
    account_level   INTEGER NOT NULL DEFAULT 1,
    per_resource    TEXT NOT NULL DEFAULT '{}',
    collected_at    INTEGER NOT NULL DEFAULT 0,
    last_success_at INTEGER NOT NULL DEFAULT 0,
    last_attempt_at INTEGER NOT NULL DEFAULT 0,
    source_host     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (provider, account_id)
) WITHOUT ROWID;

INSERT INTO cloud_cost_snapshots_v2 (
    provider, account_id, status, status_detail, currency, mtd_cost, forecast_cost, forecast_method,
    account_level, per_resource, collected_at, last_success_at, last_attempt_at, source_host
)
SELECT provider, CASE WHEN source_host = '' THEN '' ELSE 'host:' || source_host END, status, status_detail, currency, mtd_cost, forecast_cost, forecast_method,
       account_level, per_resource, collected_at, last_success_at, last_attempt_at, source_host
FROM cloud_cost_snapshots;

DROP TABLE cloud_cost_snapshots;
ALTER TABLE cloud_cost_snapshots_v2 RENAME TO cloud_cost_snapshots;
