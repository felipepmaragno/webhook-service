ALTER TABLE subscriptions
    DROP CONSTRAINT IF EXISTS subscriptions_concurrency_limit_positive,
    DROP CONSTRAINT IF EXISTS subscriptions_burst_size_positive,
    DROP CONSTRAINT IF EXISTS subscriptions_rate_limit_positive;

ALTER TABLE subscriptions
    DROP COLUMN IF EXISTS concurrency_limit,
    DROP COLUMN IF EXISTS burst_size;
