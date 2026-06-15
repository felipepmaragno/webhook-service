ALTER TABLE subscriptions
    ADD COLUMN IF NOT EXISTS burst_size INT NOT NULL DEFAULT 10,
    ADD COLUMN IF NOT EXISTS concurrency_limit INT NOT NULL DEFAULT 100;

ALTER TABLE subscriptions
    ADD CONSTRAINT subscriptions_rate_limit_positive CHECK (rate_limit > 0),
    ADD CONSTRAINT subscriptions_burst_size_positive CHECK (burst_size > 0),
    ADD CONSTRAINT subscriptions_concurrency_limit_positive CHECK (concurrency_limit > 0);
