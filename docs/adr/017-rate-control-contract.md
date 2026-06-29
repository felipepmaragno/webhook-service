# ADR 017: Rate-Control Contract Normalization

## Status

Superseded for current v1 behavior by `pre-v0.14` destination-protection simplification.

The normalization work was useful for exposing the complexity of separate rate, burst, concurrency,
and Redis-degradation semantics. Current v1 intentionally narrows the public and runtime contract
to one `max_delivery_rate` value.

## Context

Dispatch had one `rate_limit` value carrying multiple meanings:

- API and database naming implied requests per second.
- Local semaphores used the same value as maximum concurrent HTTP calls.
- Redis rate limiting ignored the subscription value and enforced a fixed default.
- The Redis path used a sliding-window log, while the fallback path used a token bucket.
- Pre-HTTP backpressure was persisted as generic retry behavior instead of explicit throttling.

That ambiguity made it hard to reason about receiver protection and made later algorithm changes
riskier than necessary.

## Decision

Subscriptions now expose three independent controls:

| Field | Meaning | Default |
|-------|---------|---------|
| `rate_limit` | Sustained requests per second | `100` |
| `burst_size` | Immediate burst capacity for token-bucket-compatible implementations | `10` |
| `concurrency_limit` | Simultaneous HTTP calls for the subscription | `100` |

Missing or non-positive API values are defaulted before persistence. Persisted values are expected
to be positive.

Rate limiter implementations receive a `RatePolicy` with `rate_limit` and `burst_size` and return
a decision containing allow/deny, retry delay, and degraded-mode metadata. Delivery code does not
depend on Redis or token-bucket details.

The Redis production path remains an exact sliding-window log. It applies `rate_limit`, but it does
not provide independent token-bucket-style burst semantics yet. The in-memory fallback uses local
token buckets and applies both `rate_limit` and `burst_size`.

Semaphore capacity uses only `concurrency_limit`. Changing `rate_limit` must not change simultaneous
HTTP calls, and changing `concurrency_limit` must not change requests per second.

Rate-limit, circuit-open, and semaphore-full decisions happen before an HTTP attempt. They persist
as `throttled`, schedule a future retry, and do not increment event attempts or write delivery
attempt rows.

When Redis rate limiting is unavailable, Dispatch falls back to local per-worker enforcement,
emits degraded-mode observability, and logs only degraded/recovered transitions. This preserves
liveness but weakens the global rate guarantee by approximately the number of active workers.

## Consequences

- The API is backward compatible for existing `rate_limit` clients.
- Existing rows receive conservative defaults: `burst_size=10` and `concurrency_limit=100`.
- The old implicit coupling between rate and concurrency is intentionally not preserved.
- Redis sliding-window behavior remains a known limitation rather than being hidden behind the
  new contract.
- A distributed token bucket remains a spike candidate, not required v1 behavior.

## Related

- [ADR 004: Rate Limiting](004-rate-limiting.md)
- [ADR 011: Redis for Horizontal Scaling](011-redis-horizontal-scaling.md)
- [ADR 013: Retry Poller and Distributed Semaphore](013-retry-poller-distributed-semaphore.md)
- [Distributed token bucket spike](../spikes/distributed-token-bucket.md)
