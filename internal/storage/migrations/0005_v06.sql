-- v0.6.0 schema additions (SPEC-v0.6): remote agent updates, network
-- egress cost estimation (pricing plans + audit log), and cloud
-- billing snapshots. Every table here is new — no existing table
-- changes shape, so there is nothing to migrate forward from v0.5.0's
-- data beyond adding these tables.

CREATE TABLE update_jobs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id   TEXT NOT NULL,
    host_id    TEXT NOT NULL,
    target     TEXT NOT NULL,
    state      TEXT NOT NULL DEFAULT 'queued',
    reason     TEXT NOT NULL DEFAULT '',
    error      TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    attempt    INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX idx_update_jobs_batch ON update_jobs (batch_id);
CREATE INDEX idx_update_jobs_host_state ON update_jobs (host_id, state);

-- One row per host+queued/in_progress job is enough for the hub to
-- know what to hand back on that host's next report; a host may have
-- many historical (succeeded/failed) rows, hence no uniqueness
-- constraint beyond the primary key.

CREATE TABLE pricing_plans (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    name                TEXT NOT NULL,
    provider            TEXT NOT NULL DEFAULT '',
    currency            TEXT NOT NULL DEFAULT 'USD',
    egress_free_gb      REAL NOT NULL DEFAULT 0,
    egress_tiers        TEXT NOT NULL DEFAULT '[]',
    ingress_price_per_gb REAL NOT NULL DEFAULT 0,
    pool_free_tier      INTEGER NOT NULL DEFAULT 0,
    builtin             INTEGER NOT NULL DEFAULT 0,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
);

CREATE TABLE host_pricing (
    host_id    TEXT PRIMARY KEY,
    plan_id    INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) WITHOUT ROWID;

CREATE TABLE cloud_cost_snapshots (
    provider        TEXT PRIMARY KEY,
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
    last_attempt_at INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;

-- One row per provider ("aws"/"oci"): the billing collector always
-- upserts this single row per provider rather than appending history,
-- since only the latest + last-success timestamps are needed (SPEC-v0.6
-- §1 개선 a). Stale-ness is derived at read time from last_success_at
-- and the configured interval, not stored.

CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at          INTEGER NOT NULL,
    actor       TEXT NOT NULL,
    remote      TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id   TEXT NOT NULL DEFAULT '',
    before_json TEXT NOT NULL DEFAULT '',
    after_json  TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_audit_log_at ON audit_log (at);
CREATE INDEX idx_audit_log_entity ON audit_log (entity_type, entity_id);

-- Seed the built-in default pricing plans (SPEC-v0.6 §3). Builtin rows
-- are read-only via the API (create a copy to customize); the default
-- host->plan mapping (provider-based) is applied by application code
-- when a host has no host_pricing row, not by a foreign key here.
INSERT INTO pricing_plans (name, provider, currency, egress_free_gb, egress_tiers, ingress_price_per_gb, pool_free_tier, builtin, created_at, updated_at)
VALUES (
    'AWS Free Tier (Internet egress, US)', 'aws', 'USD', 100,
    '[{"up_to_gb":10240,"price_per_gb":0.09},{"up_to_gb":51200,"price_per_gb":0.085},{"up_to_gb":153600,"price_per_gb":0.07},{"up_to_gb":0,"price_per_gb":0.05}]',
    0, 1, 1, strftime('%s','now'), strftime('%s','now')
);

INSERT INTO pricing_plans (name, provider, currency, egress_free_gb, egress_tiers, ingress_price_per_gb, pool_free_tier, builtin, created_at, updated_at)
VALUES (
    'OCI Always Free (North America/Europe)', 'oci-na-eu', 'USD', 10240,
    '[{"up_to_gb":0,"price_per_gb":0.0085}]',
    0, 1, 1, strftime('%s','now'), strftime('%s','now')
);

INSERT INTO pricing_plans (name, provider, currency, egress_free_gb, egress_tiers, ingress_price_per_gb, pool_free_tier, builtin, created_at, updated_at)
VALUES (
    'OCI Always Free (APAC/Japan/South America)', 'oci-apac', 'USD', 10240,
    '[{"up_to_gb":0,"price_per_gb":0.025}]',
    0, 1, 1, strftime('%s','now'), strftime('%s','now')
);

INSERT INTO pricing_plans (name, provider, currency, egress_free_gb, egress_tiers, ingress_price_per_gb, pool_free_tier, builtin, created_at, updated_at)
VALUES (
    'Other / Free', 'other', 'USD', 0,
    '[{"up_to_gb":0,"price_per_gb":0}]',
    0, 0, 1, strftime('%s','now'), strftime('%s','now')
);
