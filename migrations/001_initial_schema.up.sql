CREATE TYPE event_status AS ENUM (
    'pending',
    'processing',
    'delivered',
    'retrying',
    'failed'
);

CREATE TABLE events (
    id              TEXT PRIMARY KEY,
    type            TEXT NOT NULL,
    source          TEXT NOT NULL,
    data            JSONB NOT NULL,
    status          event_status NOT NULL DEFAULT 'pending',
    attempts        INT NOT NULL DEFAULT 0,
    max_attempts    INT NOT NULL DEFAULT 5,
    next_attempt_at TIMESTAMPTZ,
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at    TIMESTAMPTZ
);

CREATE TABLE delivery_attempts (
    id              SERIAL PRIMARY KEY,
    event_id        TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    attempt_number  INT NOT NULL,
    status_code     INT,
    response_body   TEXT,
    error           TEXT,
    duration_ms     INT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE subscriptions (
    id              TEXT PRIMARY KEY,
    url             TEXT NOT NULL,
    event_types     TEXT[] NOT NULL,
    secret          TEXT,
    rate_limit      INT NOT NULL DEFAULT 100,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    active          BOOLEAN NOT NULL DEFAULT TRUE
);

CREATE INDEX idx_events_pending ON events(next_attempt_at) 
    WHERE status IN ('pending', 'retrying');

CREATE INDEX idx_events_created ON events(created_at);

CREATE INDEX idx_delivery_attempts_event ON delivery_attempts(event_id);

CREATE INDEX idx_subscriptions_active ON subscriptions(active) WHERE active = TRUE;
