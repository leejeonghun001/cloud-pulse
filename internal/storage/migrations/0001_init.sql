CREATE TABLE hosts (
    id          TEXT PRIMARY KEY,
    info_json   TEXT NOT NULL,
    last_seen   INTEGER NOT NULL,
    latest_ts   INTEGER,
    latest_json TEXT
);

CREATE TABLE metrics_raw (
    host_id       TEXT NOT NULL,
    ts            INTEGER NOT NULL,
    cpu           REAL NOT NULL,
    mem_used      INTEGER NOT NULL,
    mem_total     INTEGER NOT NULL,
    mem_pct       REAL NOT NULL,
    swap_used     INTEGER NOT NULL,
    swap_total    INTEGER NOT NULL,
    disk_used     INTEGER NOT NULL,
    disk_total    INTEGER NOT NULL,
    disk_pct      REAL NOT NULL,
    disk_read_bps REAL NOT NULL,
    disk_write_bps REAL NOT NULL,
    net_rx_bps    REAL NOT NULL,
    net_tx_bps    REAL NOT NULL,
    net_rx_bytes  INTEGER NOT NULL,
    net_tx_bytes  INTEGER NOT NULL,
    load1         REAL NOT NULL,
    load5         REAL NOT NULL,
    load15        REAL NOT NULL,
    PRIMARY KEY (host_id, ts)
) WITHOUT ROWID;

CREATE TABLE metrics_5m (
    host_id       TEXT NOT NULL,
    ts            INTEGER NOT NULL,
    cpu           REAL NOT NULL,
    mem_used      INTEGER NOT NULL,
    mem_total     INTEGER NOT NULL,
    mem_pct       REAL NOT NULL,
    swap_used     INTEGER NOT NULL,
    swap_total    INTEGER NOT NULL,
    disk_used     INTEGER NOT NULL,
    disk_total    INTEGER NOT NULL,
    disk_pct      REAL NOT NULL,
    disk_read_bps REAL NOT NULL,
    disk_write_bps REAL NOT NULL,
    net_rx_bps    REAL NOT NULL,
    net_tx_bps    REAL NOT NULL,
    net_rx_bytes  INTEGER NOT NULL,
    net_tx_bytes  INTEGER NOT NULL,
    load1         REAL NOT NULL,
    load5         REAL NOT NULL,
    load15        REAL NOT NULL,
    PRIMARY KEY (host_id, ts)
) WITHOUT ROWID;

CREATE TABLE metrics_1h (
    host_id       TEXT NOT NULL,
    ts            INTEGER NOT NULL,
    cpu           REAL NOT NULL,
    mem_used      INTEGER NOT NULL,
    mem_total     INTEGER NOT NULL,
    mem_pct       REAL NOT NULL,
    swap_used     INTEGER NOT NULL,
    swap_total    INTEGER NOT NULL,
    disk_used     INTEGER NOT NULL,
    disk_total    INTEGER NOT NULL,
    disk_pct      REAL NOT NULL,
    disk_read_bps REAL NOT NULL,
    disk_write_bps REAL NOT NULL,
    net_rx_bps    REAL NOT NULL,
    net_tx_bps    REAL NOT NULL,
    net_rx_bytes  INTEGER NOT NULL,
    net_tx_bytes  INTEGER NOT NULL,
    load1         REAL NOT NULL,
    load5         REAL NOT NULL,
    load15        REAL NOT NULL,
    PRIMARY KEY (host_id, ts)
) WITHOUT ROWID;

CREATE TABLE egress_monthly (
    host_id    TEXT NOT NULL,
    month      TEXT NOT NULL,
    tx_bytes   INTEGER NOT NULL DEFAULT 0,
    rx_bytes   INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (host_id, month)
) WITHOUT ROWID;

CREATE TABLE bucket_stats (
    provider                   TEXT NOT NULL,
    bucket                     TEXT NOT NULL,
    ts                         INTEGER NOT NULL,
    region                     TEXT NOT NULL DEFAULT '',
    size_bytes                 INTEGER NOT NULL DEFAULT 0,
    object_count               INTEGER NOT NULL DEFAULT 0,
    class_a_ops_mtd            INTEGER NOT NULL DEFAULT 0,
    class_b_ops_mtd            INTEGER NOT NULL DEFAULT 0,
    requests_window            INTEGER NOT NULL DEFAULT 0,
    window_seconds             INTEGER NOT NULL DEFAULT 0,
    egress_bytes_mtd           INTEGER NOT NULL DEFAULT 0,
    request_metrics_available  INTEGER NOT NULL DEFAULT 0,
    error                      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (provider, bucket, ts)
) WITHOUT ROWID;

CREATE INDEX idx_bucket_stats_provider_bucket_ts ON bucket_stats (provider, bucket, ts);

CREATE TABLE alerts_sent (
    host_id TEXT NOT NULL,
    month   TEXT NOT NULL,
    level   TEXT NOT NULL,
    sent_at INTEGER NOT NULL,
    PRIMARY KEY (host_id, month, level)
) WITHOUT ROWID;
