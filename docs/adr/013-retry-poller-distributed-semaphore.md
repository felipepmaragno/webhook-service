# ADR 013: Retry Poller and Distributed Semaphore

## Status
Accepted (Implemented)

## Context

Two gaps were identified in the horizontal scaling architecture:

### 1. Retry Processing Gap

Events that failed delivery were correctly persisted to PostgreSQL with `status=retrying` and `next_attempt_at`, but **no component was polling the database to reprocess them**.

The existing flow was incomplete:
```
Event fails → status=retrying → PostgreSQL → ??? (nothing fetched retries)
```

### 2. Local Semaphore Limitation

Per-subscription semaphores were created locally in each worker:
```go
subSemaphores[sub.ID] = make(chan struct{}, sub.RateLimit)
```

**Problem:** With N workers, each subscription could have N × RateLimit concurrent connections, potentially overwhelming destinations.

## Decision

### 1. Retry Poller

Add a **retry poller** that runs alongside the Kafka consumer in the same worker process:

```go
// Two goroutines in same process:
go kafkaConsumer.Start()     // Primary delivery
go retryPoller.Start()       // Fetch retries from DB
```

**Why same process?**
- Simpler operations (one binary, one deployment)
- Retry volume is low (<1% of events typically)
- Shared resilience components (rate limiter, circuit breaker)
- Easy to extract later if needed

### 2. Distributed Semaphore

Replace local semaphores with **Redis-backed distributed semaphore**:

```go
// Lua script for atomic acquire
if current < limit then
    redis.call('INCR', key)
    redis.call('PEXPIRE', key, ttl_ms)
    return 1  // acquired
else
    return 0  // limit reached
end
```

**Features:**
- Coordinates concurrency across all workers
- TTL-based auto-release (prevents deadlocks on crash)
- Falls back to local semaphore if Redis unavailable

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                           Worker Process                             │
│                                                                      │
│  ┌──────────────────┐              ┌──────────────────┐             │
│  │  Kafka Consumer  │              │   Retry Poller   │             │
│  │                  │              │                  │             │
│  │  events.pending  │              │  Poll every 5s   │             │
│  │      topic       │              │  GetPendingEvents│             │
│  └────────┬─────────┘              └────────┬─────────┘             │
│           │                                  │                       │
│           └──────────────┬───────────────────┘                       │
│                          │                                           │
│                          ▼                                           │
│                 ┌─────────────────┐                                  │
│                 │ DeliveryHandler │                                  │
│                 │  ProcessBatch() │                                  │
│                 │  ProcessEvents()│                                  │
│                 └────────┬────────┘                                  │
│                          │                                           │
└──────────────────────────┼───────────────────────────────────────────┘
                           │
           ┌───────────────┼───────────────┐
           │               │               │
           ▼               ▼               ▼
    ┌────────────┐  ┌────────────┐  ┌────────────┐
    │Rate Limiter│  │  Circuit   │  │ Semaphore  │
    │ (Redis)    │  │  Breaker   │  │  (Redis)   │
    │ 100 req/s  │  │  (Redis)   │  │ 100 conc.  │
    └────────────┘  └────────────┘  └────────────┘
           │               │               │
           └───────────────┼───────────────┘
                           │
                           ▼
                    ┌─────────────┐
                    │  Webhook    │
                    │  Delivery   │
                    └──────┬──────┘
                           │
                           ▼
                    ┌─────────────┐
                    │ PostgreSQL  │
                    │ (outcomes)  │
                    └─────────────┘
```

## Implementation

### Retry Poller

```go
type Poller struct {
    config    PollerConfig
    eventRepo repository.EventRepository
    processor EventProcessor
    logger    *slog.Logger
}

type PollerConfig struct {
    PollInterval time.Duration  // default: 5s
    BatchSize    int            // default: 100
}
```

**Database Query (existing):**
```sql
SELECT * FROM events
WHERE status IN ('pending', 'retrying', 'throttled')
  AND (next_attempt_at IS NULL OR next_attempt_at <= NOW())
FOR UPDATE SKIP LOCKED
LIMIT $1
```

### Distributed Semaphore

```go
type RedisSemaphore struct {
    client   *redis.Client
    limit    int           // default: 100
    ttl      time.Duration // default: 30s
    fallback *LocalSemaphoreManager
}

func (s *RedisSemaphore) Acquire(ctx context.Context, key string) (bool, error)
func (s *RedisSemaphore) Release(ctx context.Context, key string) error
```

**Redis Key Pattern:** `sem:{subscription_id}`

### Configuration

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `RETRY_POLL_INTERVAL` | `5s` | How often to poll for retries |
| `RETRY_BATCH_SIZE` | `100` | Max events per poll |

## Consequences

### Positive

- **Complete retry flow** — Events actually get retried now
- **True concurrency control** — Destinations protected across all workers
- **Graceful degradation** — Falls back to local semaphore if Redis down
- **Simple operations** — Single binary, no new deployments
- **Safe horizontal scaling** — Add workers without overwhelming destinations

### Negative

- **Polling overhead** — One query every 5s per worker (minimal)
- **Redis dependency** — Semaphore requires Redis for full coordination
- **TTL tuning** — 30s TTL may need adjustment for long requests

## Alternatives Considered

### Separate Retry Worker

**Rejected:** Adds operational complexity for low-volume workload. Easy to extract later if retry volume grows.

### Kafka Retry Topic

**Rejected:** Would require DLQ management, adds complexity. PostgreSQL already tracks retry state.

### Distributed Lock (Redlock)

**Rejected:** Overkill for semaphore use case. Simple counter with TTL is sufficient.

## References

- [ADR 012: Kafka Event Queue](./012-kafka-event-queue.md)
- [ADR 011: Redis Horizontal Scaling](./011-redis-horizontal-scaling.md)
- [Redis INCR with Lua](https://redis.io/docs/manual/programmability/eval-intro/)
