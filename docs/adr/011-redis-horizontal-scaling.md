# ADR 011: Redis for Horizontal Scaling

## Status

Accepted

## Context

The dispatch service uses rate limiting and circuit breakers to protect destination endpoints. The initial implementation stores this state in-memory, which works correctly for single-instance deployments but breaks down with multiple instances:

- **Rate limiting**: Each instance maintains its own token bucket, so effective rate = `configured_limit * num_instances`
- **Circuit breaker**: Each instance tracks failures independently, so circuit may not open correctly

For production deployments requiring horizontal scaling, we need shared state across instances.

## Decision

Use **Redis** as the shared state store for rate limiting and circuit breaker.

### Rate Limiting: Sliding Window Counter

```
Key: ratelimit:{subscription_id}
Type: Sorted Set
Member: unique request ID (timestamp + random)
Score: Unix timestamp in milliseconds
TTL: window size (1 second)
```

**Algorithm:**
1. Remove entries older than window (ZREMRANGEBYSCORE)
2. Count current entries (ZCARD)
3. If count < limit, add new entry (ZADD)
4. All in single Lua script for atomicity

### Circuit Breaker: Distributed State

```
Keys per subscription:
- cb:{sub_id}:state     -> "closed" | "open" | "half-open"
- cb:{sub_id}:failures  -> failure count in current window
- cb:{sub_id}:successes -> success count in half-open state
- cb:{sub_id}:opened_at -> timestamp when circuit opened
```

**State transitions** use Redis transactions (MULTI/EXEC) or Lua scripts.

### Fallback Strategy

When Redis is unavailable:
1. Log warning with `slog.Warn`
2. Increment metric `redis_fallback_active` (gauge)
3. Fall back to in-memory implementation
4. Continue operating with degraded (approximate) rate limiting

**Rationale for fallback**: Webhook delivery should continue even if rate limiting is approximate. Stopping delivery entirely is worse than imprecise rate limiting.

## Alternatives Considered

### 1. PostgreSQL for State

**Rejected because:**
- Higher latency (~5-10ms vs ~1-2ms for Redis)
- Adds load to primary database
- Row-level locking contention on hot keys

### 2. Sticky Sessions (Load Balancer)

**Rejected because:**
- Requires external load balancer configuration
- State lost on instance restart
- Doesn't solve the fundamental problem

### 3. Accept Approximate Behavior

**Rejected because:**
- User explicitly requested precise horizontal scaling
- Circuit breaker behavior becomes unpredictable

### 4. No Fallback (Fail-Fast)

**Rejected because:**
- Redis failure would stop all webhook delivery
- Rate limiting is protection, not core functionality
- Degraded operation is better than no operation

## Consequences

### Positive

- **Precise rate limiting** across any number of instances
- **Consistent circuit breaker** behavior
- **Horizontal scaling** without behavior changes
- **Graceful degradation** when Redis unavailable

### Negative

- **New dependency**: Redis must be deployed and operated
- **Added latency**: ~1-2ms per rate limit check
- **Complexity**: More code to maintain
- **Failure mode**: Redis failure degrades rate limiting precision

### Neutral

- **Library choice**: `github.com/redis/go-redis/v9` (official, well-maintained)
- **Connection pooling**: Built into go-redis
- **Metrics**: Add `redis_operations_total`, `redis_latency_seconds`, `redis_fallback_active`

## Implementation Notes

### Interface Design

```go
type RateLimiter interface {
    Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

type CircuitBreaker interface {
    Allow(ctx context.Context, key string) (bool, error)
    RecordSuccess(ctx context.Context, key string) error
    RecordFailure(ctx context.Context, key string) error
    State(ctx context.Context, key string) (State, error)
}
```

### Configuration

```go
type RedisConfig struct {
    URL          string        // redis://localhost:6379/0
    PoolSize     int           // default: 10
    ReadTimeout  time.Duration // default: 3s
    WriteTimeout time.Duration // default: 3s
}
```

### Docker Compose

```yaml
redis:
  image: redis:7-alpine
  ports:
    - "6379:6379"
  volumes:
    - redis_data:/data
```

## References

- [Redis Rate Limiting Patterns](https://redis.io/glossary/rate-limiting/)
- [Distributed Circuit Breaker](https://docs.microsoft.com/en-us/azure/architecture/patterns/circuit-breaker)
- [go-redis Documentation](https://redis.uptrace.dev/)
