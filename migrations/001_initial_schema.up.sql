CREATE TYPE event_status AS ENUM (
    'pending',
    'processing',
    'delivered',
    'retrying',
    'throttled',
    'failed'
);

CREATE TYPE delivery_status AS ENUM (
    'pending',
    'processing',
    'delivered',
    'retrying',
    'throttled',
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

CREATE TABLE subscriptions (
    id                TEXT PRIMARY KEY,
    url               TEXT NOT NULL,
    event_types       TEXT[] NOT NULL,
    secret            TEXT,
    max_delivery_rate INT NOT NULL DEFAULT 100,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    active            BOOLEAN NOT NULL DEFAULT TRUE,
    CONSTRAINT subscriptions_max_delivery_rate_positive CHECK (max_delivery_rate > 0)
);

CREATE TABLE deliveries (
    id                  TEXT PRIMARY KEY,
    event_id            TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    subscription_id     TEXT NOT NULL REFERENCES subscriptions(id),
    event_type          TEXT NOT NULL,
    source              TEXT NOT NULL,
    data                JSONB NOT NULL,
    subscription_url    TEXT NOT NULL,
    subscription_secret TEXT,
    max_delivery_rate   INT NOT NULL DEFAULT 100,
    status              delivery_status NOT NULL DEFAULT 'pending',
    attempts            INT NOT NULL DEFAULT 0,
    max_attempts        INT NOT NULL DEFAULT 5,
    generation          INT NOT NULL DEFAULT 1,
    next_attempt_at     TIMESTAMPTZ,
    last_error          TEXT,
    processing_owner    TEXT,
    processing_deadline TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at        TIMESTAMPTZ,
    CONSTRAINT deliveries_generation_positive CHECK (generation > 0),
    UNIQUE (event_id, subscription_id),
    UNIQUE (id, event_id, subscription_id)
);

CREATE TABLE delivery_attempts (
    id              SERIAL PRIMARY KEY,
    event_id        TEXT NOT NULL,
    delivery_id     TEXT NOT NULL,
    subscription_id TEXT NOT NULL,
    attempt_number  INT NOT NULL,
    generation      INT NOT NULL DEFAULT 1,
    status_code     INT,
    response_body   TEXT,
    error           TEXT,
    duration_ms     INT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT delivery_attempts_generation_positive CHECK (generation > 0),
    CONSTRAINT delivery_attempts_delivery_fk
        FOREIGN KEY (delivery_id, event_id, subscription_id)
        REFERENCES deliveries(id, event_id, subscription_id)
        ON DELETE CASCADE
);

CREATE INDEX idx_delivery_attempts_event ON delivery_attempts(event_id);
CREATE INDEX idx_delivery_attempts_delivery ON delivery_attempts(delivery_id);
CREATE INDEX idx_delivery_attempts_body_retention
    ON delivery_attempts (created_at, id)
    WHERE response_body IS NOT NULL;
CREATE INDEX idx_deliveries_event ON deliveries(event_id);
CREATE INDEX idx_deliveries_subscription ON deliveries(subscription_id);
CREATE INDEX idx_deliveries_retry_claimable
    ON deliveries (processing_deadline, next_attempt_at, created_at)
    WHERE status IN ('retrying', 'throttled', 'processing');
CREATE INDEX idx_events_terminal_retention
    ON events (updated_at, id)
    WHERE status IN ('delivered', 'failed');
CREATE INDEX idx_subscriptions_active ON subscriptions(active) WHERE active = TRUE;
