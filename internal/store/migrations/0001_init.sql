CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token TEXT NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX idx_sessions_expiry ON sessions(expires_at);

CREATE TABLE api_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE monitors (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    type TEXT NOT NULL CHECK (type IN ('http', 'tcp', 'ping', 'heartbeat')),
    name TEXT NOT NULL,
    config TEXT NOT NULL,
    interval_seconds INTEGER NOT NULL,
    timeout_seconds INTEGER NOT NULL,
    retries INTEGER NOT NULL DEFAULT 0,
    failure_threshold INTEGER NOT NULL DEFAULT 3,
    cooldown_seconds INTEGER NOT NULL DEFAULT 300,
    enabled INTEGER NOT NULL DEFAULT 1,
    is_public INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'up' CHECK (status IN ('up', 'down')),
    ssl_expires_at INTEGER NOT NULL DEFAULT 0,
    last_check_at INTEGER NOT NULL DEFAULT 0,
    next_run_at INTEGER NOT NULL DEFAULT 0,
    last_ping_at INTEGER NOT NULL DEFAULT 0,
    next_deadline_at INTEGER NOT NULL DEFAULT 0,
    heartbeat_token TEXT UNIQUE,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX idx_monitors_next_run ON monitors(enabled, next_run_at);
CREATE INDEX idx_monitors_deadline ON monitors(enabled, status, next_deadline_at);

CREATE TABLE check_results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    monitor_id INTEGER NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    ok INTEGER NOT NULL,
    status_code INTEGER NOT NULL DEFAULT 0,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_results_monitor_time ON check_results(monitor_id, created_at);
CREATE INDEX idx_results_time ON check_results(created_at);

CREATE TABLE hourly_summaries (
    monitor_id INTEGER NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    hour INTEGER NOT NULL,
    total INTEGER NOT NULL,
    ok INTEGER NOT NULL,
    latency_sum INTEGER NOT NULL,
    PRIMARY KEY (monitor_id, hour)
);

CREATE TABLE incidents (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    monitor_id INTEGER NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    started_at INTEGER NOT NULL,
    ended_at INTEGER,
    reason TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_incidents_monitor ON incidents(monitor_id, started_at);

CREATE TABLE channels (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    type TEXT NOT NULL CHECK (type IN ('telegram', 'slack', 'discord', 'smtp', 'webhook')),
    name TEXT NOT NULL,
    config TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE monitor_channels (
    monitor_id INTEGER NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    PRIMARY KEY (monitor_id, channel_id)
);

CREATE TABLE alert_state (
    monitor_id INTEGER NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    last_sent_at INTEGER NOT NULL,
    last_incident_id INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (monitor_id, channel_id)
);

CREATE TABLE ssl_alerts (
    monitor_id INTEGER NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    days INTEGER NOT NULL,
    sent_at INTEGER NOT NULL,
    PRIMARY KEY (monitor_id, days)
);
