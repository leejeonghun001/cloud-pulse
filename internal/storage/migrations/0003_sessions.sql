CREATE TABLE sessions (
    id_hash    TEXT NOT NULL PRIMARY KEY,
    created_at INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    remote     TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT ''
) WITHOUT ROWID;

CREATE INDEX idx_sessions_expires_at ON sessions (expires_at);
