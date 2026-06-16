DROP INDEX IF EXISTS idx_subscriptions_active;
DROP INDEX IF EXISTS idx_deliveries_retry_claimable;
DROP INDEX IF EXISTS idx_deliveries_subscription;
DROP INDEX IF EXISTS idx_deliveries_event;
DROP INDEX IF EXISTS idx_delivery_attempts_delivery;
DROP INDEX IF EXISTS idx_delivery_attempts_event;
DROP INDEX IF EXISTS idx_events_created;
DROP INDEX IF EXISTS idx_events_pending;

DROP TABLE IF EXISTS delivery_attempts;
DROP TABLE IF EXISTS deliveries;
DROP TABLE IF EXISTS subscriptions;
DROP TABLE IF EXISTS events;

DROP TYPE IF EXISTS delivery_status;
DROP TYPE IF EXISTS event_status;
