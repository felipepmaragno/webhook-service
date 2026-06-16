DROP INDEX IF EXISTS idx_deliveries_retry_claimable;
DROP INDEX IF EXISTS idx_deliveries_subscription;
DROP INDEX IF EXISTS idx_deliveries_event;
DROP INDEX IF EXISTS idx_delivery_attempts_delivery;

ALTER TABLE delivery_attempts
    DROP CONSTRAINT IF EXISTS delivery_attempts_attribution_consistent,
    DROP CONSTRAINT IF EXISTS delivery_attempts_subscription_fk,
    DROP CONSTRAINT IF EXISTS delivery_attempts_delivery_fk;

ALTER TABLE delivery_attempts
    DROP COLUMN IF EXISTS subscription_id,
    DROP COLUMN IF EXISTS delivery_id;

DROP TABLE IF EXISTS deliveries;
DROP TYPE IF EXISTS delivery_status;
