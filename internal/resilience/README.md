# Resilience Controls

> Local implementation context for engineers and coding agents. Read this file before
> changing destination protection or rate limiting.

## Current responsibility

This package owns the v1 destination-protection control: per-subscription `max_delivery_rate`
driven by `Subscription.MaxDeliveryRate` or the frozen `Delivery.MaxDeliveryRate`.

It does not own retry scheduling, circuit breaking, distributed semaphores, or receiver health
classification. Redis is used only for distributed rate limiting.

## Current contract

- `max_delivery_rate` is sustained delivery attempts per second for one subscription.
- A missing or non-positive value falls back to `domain.DefaultSubscriptionMaxDeliveryRate`.
- If Redis is configured, a Redis sliding-window limiter coordinates the decision across workers.
- If Redis is not configured, a local token-bucket limiter is used for development and single-worker
  operation.
- If Redis is configured but unavailable, decisions fail closed as `throttled`.
- The limiter is checked before an HTTP request is created.
- A rejected decision returns a retry delay and is persisted by the Kafka package as `throttled`.
- Throttling does not create a delivery attempt and does not consume the delivery attempt count.

## Invariants

1. Controls are keyed by subscription ID; one receiver must not consume another receiver's allowance.
2. Rejection before HTTP does not count as a delivery attempt.
3. Delivery rows freeze the selected max-delivery-rate value so retry and replay behavior is stable.
4. The package exposes a small `RateLimiter` interface; delivery code should not depend on concrete
   limiter internals.
5. Redis coordination is limited to max-delivery-rate; do not add circuit breaker or semaphore
   behavior without a new exec plan and explicit product justification.

## Verification

```bash
go test ./internal/resilience/...
go test -race ./internal/resilience/...
go test -race ./internal/kafka/...
go test ./internal/app/...
```

Update this file whenever the active algorithm, policy contract, fallback behavior, or control
ownership changes.
