DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'delivery_status') THEN
        CREATE TYPE delivery_status AS ENUM (
            'pending',
            'processing',
            'delivered',
            'retrying',
            'throttled',
            'failed'
        );
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS deliveries (
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
    ADD COLUMN IF NOT EXISTS delivery_id TEXT,
    ADD COLUMN IF NOT EXISTS subscription_id TEXT;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'delivery_attempts_delivery_fk') THEN
        ALTER TABLE delivery_attempts
            ADD CONSTRAINT delivery_attempts_delivery_fk
            FOREIGN KEY (delivery_id) REFERENCES deliveries(id) ON DELETE SET NULL;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'delivery_attempts_subscription_fk') THEN
        ALTER TABLE delivery_attempts
            ADD CONSTRAINT delivery_attempts_subscription_fk
            FOREIGN KEY (subscription_id) REFERENCES subscriptions(id) ON DELETE SET NULL;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'delivery_attempts_attribution_consistent') THEN
        ALTER TABLE delivery_attempts
            ADD CONSTRAINT delivery_attempts_attribution_consistent
            CHECK (
                (delivery_id IS NULL AND subscription_id IS NULL)
                OR (delivery_id IS NOT NULL AND subscription_id IS NOT NULL)
            );
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_delivery_attempts_delivery
    ON delivery_attempts(delivery_id)
    WHERE delivery_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_deliveries_event ON deliveries(event_id);
CREATE INDEX IF NOT EXISTS idx_deliveries_subscription ON deliveries(subscription_id);
CREATE INDEX IF NOT EXISTS idx_deliveries_retry_claimable
    ON deliveries (processing_deadline, next_attempt_at, created_at)
    WHERE status IN ('retrying', 'throttled', 'processing');
