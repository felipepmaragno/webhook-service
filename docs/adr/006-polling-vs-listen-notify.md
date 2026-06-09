# ADR 006: Polling vs LISTEN/NOTIFY

## Status
Accepted — **superseded by [ADR 012](012-kafka-event-queue.md)** for primary event dispatch

The decision to use DB polling as the *primary* mechanism for picking up new events was replaced by Kafka consumer groups (ADR 012). DB polling with `FOR UPDATE SKIP LOCKED` is retained for the **retry poller** only — it handles `status=retrying/throttled` events that need re-delivery after their backoff delay. The arguments in this ADR apply to that narrower scope.

## Context
Workers need to know when new events are available for processing. Two main approaches:

1. **Polling**: Workers periodically query the database
2. **Push (LISTEN/NOTIFY)**: Database notifies workers of new events

## Decision
Use **polling with FOR UPDATE SKIP LOCKED** for the retry poller.

Configuration (retry poller defaults):
- Poll interval: 5s (configurable via `RETRY_POLL_INTERVAL`)
- Batch size: 100 events per poll (configurable via `RETRY_BATCH_SIZE`)

## Alternatives Considered

### PostgreSQL LISTEN/NOTIFY
```sql
-- On event insert
NOTIFY new_event, 'evt_123';

-- Worker listens
LISTEN new_event;
```

**Pros:**
- Near-instant delivery
- No wasted queries

**Cons:**
- Connection management complexity
- Lost notifications on disconnect
- Still need polling as fallback
- Harder to scale (all instances get all notifications)

### Hybrid (LISTEN/NOTIFY + Polling)
**Pros:**
- Best of both worlds
- Fast delivery with fallback

**Cons:**
- More complex implementation
- Two code paths to maintain
- Marginal benefit for our use case

### External Queue (Redis, RabbitMQ)
**Pros:**
- Purpose-built for pub/sub
- Better scaling characteristics

**Cons:**
- Additional infrastructure
- Data synchronization complexity
- See ADR 002

## Rationale

### 1. Simplicity
Polling is straightforward:
```go
func (p *Pool) worker(ctx context.Context, id int) {
    ticker := time.NewTicker(p.config.PollInterval)
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            p.processBatch(ctx)
        }
    }
}
```

No connection state management, no notification handling, no fallback logic.

### 2. FOR UPDATE SKIP LOCKED
PostgreSQL's `FOR UPDATE SKIP LOCKED` makes polling efficient:

```sql
SELECT * FROM events
WHERE status IN ('pending', 'retrying')
  AND (next_attempt_at IS NULL OR next_attempt_at <= NOW())
ORDER BY created_at
FOR UPDATE SKIP LOCKED
LIMIT 10
```

Benefits:
- Workers don't block each other
- No duplicate processing
- Scales horizontally (add more workers/instances)
- Atomic claim of events

### 3. Latency is Acceptable for Retries
With 5s poll interval for the retry poller:
- Average latency added: ~2.5s
- Maximum latency added: 5s

This is negligible for retry delivery since events already have multi-second backoff delays (1s, 2s, 4s, 8s...).

### 4. Predictable Load
Polling creates predictable database load:
- N worker instances × 1 query per 5 seconds = very low DB load
- Easy to capacity plan
- No thundering herd on burst of events

### 5. Resilience
Polling is inherently resilient:
- No state to lose on disconnect
- Automatic recovery after database restart
- No missed events

## Performance Considerations

### Efficient Indexing
Partial index for pending events:
```sql
CREATE INDEX idx_events_pending 
ON events (next_attempt_at) 
WHERE status IN ('pending', 'retrying');
```

This index is small (only pending events) and fast to scan.

### Batch Processing
Fetch multiple events per query:
```go
events, err := p.eventRepo.GetPendingBatch(ctx, p.config.BatchSize)
```

Reduces query overhead, amortizes connection cost.

## Consequences

### Positive
- Simple implementation
- Robust and predictable
- Easy to debug
- Scales horizontally

### Negative
- Slight latency (50ms average)
- Continuous database queries (even when idle)
- Not suitable for sub-millisecond requirements

### Future Considerations
- Adaptive poll interval (slower when idle)
- LISTEN/NOTIFY for latency-critical use cases
- Metrics on poll efficiency (events per poll)
