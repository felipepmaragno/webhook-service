-- Note: PostgreSQL doesn't support removing enum values directly
-- Convert throttled events back to retrying before removing the type
UPDATE events SET status = 'retrying' WHERE status = 'throttled';

-- Recreate index without throttled
DROP INDEX IF EXISTS idx_events_pending;
CREATE INDEX idx_events_pending ON events(next_attempt_at) 
    WHERE status IN ('pending', 'retrying');
