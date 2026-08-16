CREATE TABLE IF NOT EXISTS jobs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    source          TEXT NOT NULL,
    instance        TEXT NOT NULL,
    event_type      TEXT NOT NULL,
    media_type      TEXT NOT NULL,
    path            TEXT NOT NULL,
    target          TEXT NOT NULL,
    scan_path       TEXT NOT NULL,
    status          TEXT NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMP NOT NULL,
    created_at      TIMESTAMP NOT NULL,
    updated_at      TIMESTAMP NOT NULL,
    last_error      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_jobs_claim ON jobs (status, next_attempt_at);
CREATE INDEX IF NOT EXISTS idx_jobs_dedup ON jobs (target, scan_path, status);
