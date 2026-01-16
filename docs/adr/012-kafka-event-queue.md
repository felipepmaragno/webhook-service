# ADR 012: Kafka for Event Queue and Horizontal Scaling

## Status
Accepted (Implemented)

## Context

The original architecture used PostgreSQL as both persistence layer and event queue via `FOR UPDATE SKIP LOCKED`. This created a bottleneck:

**Original Performance (measured):**
| Configuration | Throughput | Latency |
|---------------|------------|---------|
| 1 instance, 10 workers | 6,361/s | 15ms |
| 3 instances, 30 workers | 3,006/s | 33ms |

**Problem:** Adding more instances **decreased** throughput due to lock contention.

## Decision

**Adopt Kafka** for event queue coordination while keeping PostgreSQL for persistence and retry management.

## Architecture

```
┌─────────────┐
│  HTTP API   │ (cmd/dispatch)
│ POST /events│
└──────┬──────┘
       │
       ▼
┌─────────────┐
│    Kafka    │ (events.pending topic)
└──────┬──────┘
       │
       ├──────────────────────────┬──────────────────────────┐
       │                          │                          │
 ┌─────▼─────┐              ┌─────▼─────┐              ┌─────▼─────┐
 │  Worker   │              │  Worker   │              │  Worker   │
 │ Instance 1│              │ Instance 2│              │ Instance 3│
 └─────┬─────┘              └─────┬─────┘              └─────┬─────┘
       │                          │                          │
 Rate Limiter (100 req/s) ←────── Redis ──────→ Circuit Breaker
       │                          │                          │
       ▼                          ▼                          ▼
 [Webhook]                  [Webhook]                  [Webhook]
       │                          │                          │
       └──────────────────────────┼──────────────────────────┘
                                  │
                                  ▼
                           ┌─────────────┐
                           │ PostgreSQL  │
                           │ (outcomes)  │
                           └─────────────┘
```

### Flow

1. **Ingest:** HTTP API (POST /events) → Kafka (events.pending topic)
2. **Consume:** Kafka workers → Webhook delivery → PostgreSQL (status, attempts)
3. **Retry:** Failed deliveries → PostgreSQL (status=retrying)

### Kafka Topics

| Topic | Partitions | Purpose |
|-------|------------|---------|
| `events.pending` | 12 | New events to deliver |

### Resilience

| Mechanism | Description |
|-----------|-------------|
| **Rate Limiter** | Fixed 100 req/s per subscription (Redis-backed) |
| **Circuit Breaker** | Stops requests to failing destinations (Redis-backed) |
| **Semaphores** | Limits concurrent requests per subscription |

### Concurrency Control

Concurrency is controlled by **per-subscription semaphores**:

```go
// Each subscription gets its own semaphore
subSemaphores[sub.ID] = make(chan struct{}, sub.RateLimit)

// Goroutines block until slot available
sem <- struct{}{}        // Acquire (blocks if full)
defer func() { <-sem }() // Release when done
```

This ensures:
- Subscriptions don't compete for global slots
- Each subscription respects its configured rate limit
- Maximum parallelism across different subscriptions

### Batch Processing

Batches are **time-based**, not size-based:
- `BatchTimeout: 100ms` — process all messages that arrived in the interval
- No artificial batch size limit
- Kafka's `MaxBytes` (10MB) and `MaxWait` control actual batch size

## Performance Results

| Metric | Value |
|--------|-------|
| Kafka production | ~100k events/s |
| Delivery (10k subscriptions) | ~8k delivered in ~30s |
| Success rate | 82-99% (depends on destination capacity) |

### Test: 1 Event per Subscription

| Subscriptions | Delivered | Retry | Success Rate |
|---------------|-----------|-------|--------------|
| 1,000 | 1,000 | 0 | 100% |
| 5,000 | 4,982 | 18 | 99.6% |
| 10,000 | 8,221 | 1,779 | 82% |

Retries at 10k subscriptions were due to destination (httpbin.org) overload, not system limitations.

## Implementation

### Components

| Component | Description |
|-----------|-------------|
| `cmd/worker` | Kafka consumer that delivers webhooks |
| `cmd/producer` | Load test producer (for testing) |
| `cmd/migrate` | Database migrations |
| `internal/kafka` | Consumer, handler, producer implementations |
| `internal/resilience` | Rate limiter and circuit breaker (Redis-backed) |
| `internal/repository` | PostgreSQL repositories for events and subscriptions |

### Key Design Decisions

1. **Retries via PostgreSQL** — Simpler architecture, single source of truth
2. **Per-subscription semaphores** — True parallelism across subscriptions
3. **Time-based batching** — No artificial limits, process what arrives
4. **Manual offset commit** — At-least-once delivery guarantee
5. **Graceful shutdown** — Context cancellation unblocks waiting goroutines
6. **Fixed rate limit (100 req/s)** — Safety limit, not configurable per subscription
7. **Redis-backed resilience** — Distributed rate limiting and circuit breaker

## Consequences

### Positive
- True horizontal scaling (tested with 3 workers)
- Parallelism across 10k+ subscriptions
- Predictable behavior under load
- Simpler retry logic (single source of truth in PostgreSQL)

### Negative
- Operational complexity (Kafka cluster)
- Additional infrastructure cost
- Destination capacity becomes the bottleneck

## References

- [Kafka Consumer Groups](https://kafka.apache.org/documentation/#consumerconfigs)
- [ADR 006: Polling vs LISTEN/NOTIFY](./006-polling-vs-listen-notify.md)
