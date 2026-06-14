ALTER TABLE events
    ADD COLUMN processing_owner TEXT,
    ADD COLUMN processing_deadline TIMESTAMPTZ;

-- Existing processing rows predate leases and may have been abandoned. Mark them
-- immediately reclaimable instead of leaving NULL metadata that no worker can own.
UPDATE events
SET processing_owner = 'migration-recovery',
    processing_deadline = NOW()
WHERE status = 'processing';

CREATE INDEX idx_events_retry_claimable
    ON events (processing_deadline, next_attempt_at, created_at)
    WHERE status IN ('retrying', 'throttled', 'processing');
