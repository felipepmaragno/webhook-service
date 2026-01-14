-- Add throttled status for rate-limited/circuit-breaker events
-- This is internal backpressure, not a delivery failure
ALTER TYPE event_status ADD VALUE IF NOT EXISTS 'throttled' AFTER 'retrying';

-- Update index to include throttled status
DROP INDEX IF EXISTS idx_events_pending;
CREATE INDEX idx_events_pending ON events(next_attempt_at) 
    WHERE status IN ('pending', 'retrying', 'throttled');
