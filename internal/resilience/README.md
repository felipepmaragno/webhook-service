# Resilience Controls

> Local implementation context for engineers and coding agents. Read this file before
> changing rate limiting, circuit breaking, distributed semaphores, or Redis fallback behavior.

## Current rate-limiting behavior

The production Redis path uses an exact sliding-window log per subscription:

1. Remove sorted-set members older than one second.
2. Count remaining members.
3. If fewer than 100 remain, insert the current request timestamp and allow it.
4. Otherwise reject it.

The Lua script makes this decision atomic across workers. The key is `ratelimit:{subscription_id}`.
Each accepted request consumes one sorted-set member until the window expires.

The fallback path uses `golang.org/x/time/rate`, which is a local token bucket configured for
100 requests/second and burst 10. Redis and fallback therefore do not currently enforce the same
traffic shape, and fallback is not globally coordinated.

## Known contract debt

- Redis ignores `Subscription.RateLimit` and always uses the fixed default of 100 requests/second.
- `Subscription.RateLimit` is also used as local semaphore capacity, coupling rate and concurrency.
- Rate-limiter rejection currently becomes a generic retry rather than persisted `throttled` state.
- The `RateLimiter` interface returns only allow/deny and cannot communicate retry delay.
- Redis failure silently changes global enforcement into one independent limiter per worker.
- The sliding-window log has per-request Redis state and work; it is exact but not constant-space.

## Planned sequence

- v0.9.0 normalizes rate, burst, concurrency, throttling, retry delay, and degradation semantics while retaining Redis sliding-window log.
- v0.10.0 migrates Redis to distributed token bucket after the policy contract is stable.

Do not implement the token-bucket migration before v0.9.0. Otherwise algorithm code would be forced
to encode unresolved API, schema, concurrency, and failure-policy decisions.

## Invariants

1. Controls are per subscription; one receiver must not consume another receiver's allowance.
2. Rejection before HTTP does not count as a delivery attempt or circuit-breaker failure.
3. Rate limiting and concurrency limiting are separate controls.
4. Redis-backed controls coordinate workers; in-memory fallback cannot claim a global guarantee.
5. Resilience errors and degraded operation must be observable without high-cardinality metric labels.

## Verification

```bash
go test -race ./internal/resilience/...
go test -race ./internal/kafka/...
go test ./internal/app/...
```

Use Redis Testcontainers tests for Lua atomicity and distributed behavior. Update this file whenever
the active algorithm, policy contract, key format, fallback behavior, or control ownership changes.
