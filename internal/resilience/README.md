# Resilience Controls

> Local implementation context for engineers and coding agents. Read this file before
> changing rate limiting, circuit breaking, distributed semaphores, or Redis fallback behavior.

## Current rate-limiting behavior

The production Redis path uses an exact sliding-window log per subscription:

1. Remove sorted-set members older than one second.
2. Count remaining members.
3. If fewer than the subscription's `rate_limit` remain, insert the current request timestamp and allow it.
4. Otherwise reject it.

The Lua script makes this decision atomic across workers. The key is `ratelimit:{subscription_id}`.
Each accepted request consumes one sorted-set member until the window expires.

The fallback path uses `golang.org/x/time/rate`, which is a local token bucket configured from
the subscription's `rate_limit` and `burst_size`. Redis and fallback still do not enforce the
same traffic shape because Redis is a sliding-window log and the fallback is token bucket.
Fallback is not globally coordinated.

## Current contract

- `rate_limit` is sustained requests per second.
- `burst_size` is local token-bucket burst capacity. The current Redis sliding-window path receives
  the policy but does not provide independent burst semantics.
- `concurrency_limit` is simultaneous HTTP calls and is enforced by the semaphore path.
- Rate-limiter, circuit-open, and semaphore-full decisions are persisted as `throttled` without
  consuming delivery attempts.
- The `RateLimiter` interface returns an allow/deny decision, retry delay, and degraded-mode flag.
- Redis failure silently changes global enforcement into one independent limiter per worker.
- The sliding-window log has per-request Redis state and work; it is exact but not constant-space.

`contracts_test.go` holds the shared behavior expected from Redis and in-memory implementations:
policy limits are enforced, denied requests include retry delay, subscription state is isolated,
zero-valued policies use defaults, and circuit breakers move closed -> open -> half-open -> closed
under the configured thresholds. Keep implementation-specific tests for algorithm details that are
not shared, especially Redis Lua atomicity and token-bucket burst behavior.

## Planned sequence

- v0.9.0 normalizes rate, burst, concurrency, throttling, retry delay, and degradation semantics while retaining Redis sliding-window log.
- Token bucket is deferred to `docs/spikes/distributed-token-bucket.md` and is not required for v1 unless measurements justify it.

Do not promote the token-bucket migration before v0.9.0 evidence exists. Otherwise algorithm code
would be forced to encode unresolved API, schema, concurrency, and failure-policy decisions.

## Invariants

1. Controls are per subscription; one receiver must not consume another receiver's allowance.
2. Rejection before HTTP does not count as a delivery attempt or circuit-breaker failure.
3. Rate limiting and concurrency limiting are separate controls.
4. Redis-backed controls coordinate workers; in-memory fallback cannot claim a global guarantee.
5. Resilience errors and degraded operation must be observable without high-cardinality metric labels.

## Verification

```bash
go test ./internal/resilience/...
go test -race ./internal/resilience/...
go test -race ./internal/kafka/...
go test ./internal/app/...
```

Use Redis Testcontainers tests for Lua atomicity and distributed behavior. Update this file whenever
the active algorithm, policy contract, key format, fallback behavior, or control ownership changes.
