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

CREATE TABLE delivery_attempts (
    id              SERIAL PRIMARY KEY,
    event_id        TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    delivery_id     TEXT,
    subscription_id TEXT,
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
    burst_size      INT NOT NULL DEFAULT 10,
    concurrency_limit INT NOT NULL DEFAULT 100,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    active          BOOLEAN NOT NULL DEFAULT TRUE
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
    rate_limit          INT NOT NULL DEFAULT 100,
    burst_size          INT NOT NULL DEFAULT 10,
    concurrency_limit   INT NOT NULL DEFAULT 100,
    status              delivery_status NOT NULL DEFAULT 'pending',
    attempts            INT NOT NULL DEFAULT 0,
    max_attempts        INT NOT NULL DEFAULT 5,
    next_attempt_at     TIMESTAMPTZ,
    last_error          TEXT,
    processing_owner    TEXT,
    processing_deadline TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at        TIMESTAMPTZ,
    UNIQUE (event_id, subscription_id)
);

ALTER TABLE delivery_attempts
    ADD CONSTRAINT delivery_attempts_delivery_fk
    FOREIGN KEY (delivery_id) REFERENCES deliveries(id) ON DELETE SET NULL,
    ADD CONSTRAINT delivery_attempts_subscription_fk
    FOREIGN KEY (subscription_id) REFERENCES subscriptions(id) ON DELETE SET NULL,
    ADD CONSTRAINT delivery_attempts_attribution_consistent
    CHECK (
        (delivery_id IS NULL AND subscription_id IS NULL)
        OR (delivery_id IS NOT NULL AND subscription_id IS NOT NULL)
    );

CREATE INDEX idx_events_pending ON events(next_attempt_at) 
    WHERE status IN ('pending', 'retrying', 'throttled');

CREATE INDEX idx_events_created ON events(created_at);

CREATE INDEX idx_delivery_attempts_event ON delivery_attempts(event_id);
CREATE INDEX idx_delivery_attempts_delivery ON delivery_attempts(delivery_id) WHERE delivery_id IS NOT NULL;
CREATE INDEX idx_deliveries_event ON deliveries(event_id);
CREATE INDEX idx_deliveries_subscription ON deliveries(subscription_id);
CREATE INDEX idx_deliveries_retry_claimable
    ON deliveries (processing_deadline, next_attempt_at, created_at)
    WHERE status IN ('retrying', 'throttled', 'processing');

CREATE INDEX idx_subscriptions_active ON subscriptions(active) WHERE active = TRUE;
