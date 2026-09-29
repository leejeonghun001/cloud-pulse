CREATE TABLE host_limits (
    host_id            TEXT PRIMARY KEY,
    egress_limit_bytes INTEGER NULL,
    ingress_limit_bytes INTEGER NULL,
    updated_at         INTEGER NOT NULL
) WITHOUT ROWID;

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
) WITHOUT ROWID;

CREATE TABLE alerts_sent_new (
    host_id   TEXT NOT NULL,
    month     TEXT NOT NULL,
    direction TEXT NOT NULL DEFAULT 'out',
    level     TEXT NOT NULL,
    sent_at   INTEGER NOT NULL,
    PRIMARY KEY (host_id, month, direction, level)
) WITHOUT ROWID;

INSERT INTO alerts_sent_new (host_id, month, direction, level, sent_at)
SELECT host_id, month, 'out', level, sent_at FROM alerts_sent;

DROP TABLE alerts_sent;

ALTER TABLE alerts_sent_new RENAME TO alerts_sent;
