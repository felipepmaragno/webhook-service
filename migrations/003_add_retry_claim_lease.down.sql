DROP INDEX IF EXISTS idx_events_retry_claimable;

ALTER TABLE events
    DROP COLUMN IF EXISTS processing_deadline,
    DROP COLUMN IF EXISTS processing_owner;
