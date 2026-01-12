# ADR 004: Rate Limiting

## Status
Accepted

## Context
Webhook destinations have varying capacity:
- Some can handle thousands of requests/second
- Others may be rate-limited or have low capacity
- We must not overwhelm destination servers

Requirements:
- Per-destination rate limiting
- Allow bursts for spiky traffic
- Don't count rate-limited requests as failures

## Decision
Use **token bucket algorithm** via `golang.org/x/time/rate`, per subscription.

Default configuration:
- Rate: 100 requests/second
- Burst: 10 requests

## Alternatives Considered

### Leaky Bucket
**Pros:**
- Smooth, constant output rate
- Simple to understand

**Cons:**
- No burst allowance
- Can introduce unnecessary latency

### Fixed Window Counter
```
if requests_this_minute < limit:
    allow()
```
**Cons:**
- Boundary problem (2x burst at window edges)
- Less precise

### Sliding Window Log
**Pros:**
- Most accurate
- No boundary issues

**Cons:**
- Memory intensive (stores all timestamps)
- More complex implementation

### Sliding Window Counter
**Pros:**
- Good accuracy
- Lower memory than log

**Cons:**
- More complex than token bucket
- Still has some boundary effects

## Rationale

### 1. Token Bucket Allows Bursts
Real traffic is bursty. Token bucket handles this naturally:
```
Bucket capacity: 10 tokens
Refill rate: 100 tokens/second

- Burst of 10 requests: all pass immediately
- Sustained 100 req/s: all pass
- Sustained 150 req/s: 100 pass, 50 rejected
```

### 2. golang.org/x/time/rate
Official Go extended library:
- Battle-tested implementation
- Thread-safe
- Simple API: `Allow()`, `Wait()`, `Reserve()`
- Zero external dependencies

```go
limiter := rate.NewLimiter(rate.Limit(100), 10) // 100/s, burst 10

if !limiter.Allow() {
    // Rate limited, reschedule without counting as failure
    return ErrRateLimited
}
```

### 3. Per-Subscription Isolation
Each subscription gets independent rate limiter:
```go
type RateLimiterManager struct {
    limiters map[string]*rate.Limiter
    mu       sync.RWMutex
}
```

Benefits:
- One slow destination doesn't affect others
- Different limits per subscription (future)
- Clean resource isolation

### 4. Rate-Limited ≠ Failed
When rate limited:
- Event is rescheduled (not marked as failed)
- Attempt counter is NOT incremented
- Metric `rate_limiter_rejections_total` is incremented

```go
func (e *Event) RescheduleWithoutAttemptIncrement(nextAttempt time.Time) {
    e.Status = EventStatusRetrying
    e.NextAttemptAt = &nextAttempt
    // Note: e.Attempts is NOT incremented
}
```

## Implementation

```go
func (m *RateLimiterManager) Allow(subscriptionID string) bool {
    m.mu.RLock()
    limiter, exists := m.limiters[subscriptionID]
    m.mu.RUnlock()
    
    if !exists {
        limiter = m.getOrCreate(subscriptionID)
    }
    
    return limiter.Allow()
}
```

## Consequences

### Positive
- Protects destination servers
- Handles bursty traffic gracefully
- Simple, proven algorithm
- Official Go library

### Negative
- In-memory state (lost on restart)
- No coordination between instances

### Future Considerations
- Distributed rate limiting (Redis-based)
- Per-subscription configurable limits
- Adaptive rate limiting based on 429 responses
