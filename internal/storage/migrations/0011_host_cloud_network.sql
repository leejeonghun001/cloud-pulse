-- Provider-measured monthly traffic per host (OCI VNIC metrics),
-- reported by the host's agent. One row per host, overwritten each
-- report; a failed attempt keeps the last successful month/bytes.
CREATE TABLE host_cloud_network (
    host_id         TEXT PRIMARY KEY,
    provider        TEXT NOT NULL,
    month           TEXT NOT NULL DEFAULT '',
    in_bytes        INTEGER NOT NULL DEFAULT 0,
    out_bytes       INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT '',
    status_detail   TEXT NOT NULL DEFAULT '',
    last_success_at INTEGER NOT NULL DEFAULT 0,
    last_attempt_at INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;
