# Resilience Controls

> Local implementation context for engineers and coding agents. Read this file before
> changing destination protection or rate limiting.

## Current responsibility

This package owns the v1 destination-protection guardrail: a local per-subscription rate limiter
driven by `Subscription.MaxDeliveryRate` or the frozen `Delivery.MaxDeliveryRate`.

It does not own retry scheduling, circuit breaking, distributed semaphores, Redis coordination, or
receiver health classification. Those features were removed from the v1 contract during the
pre-v0.14 simplification.

## Current contract

- `max_delivery_rate` is sustained delivery attempts per second for one subscription.
- A missing or non-positive value falls back to `domain.DefaultSubscriptionMaxDeliveryRate`.
- The limiter is checked before an HTTP request is created.
- A rejected decision returns a retry delay and is persisted by the Kafka package as `throttled`.
- Throttling does not create a delivery attempt and does not consume the delivery attempt count.
- The implementation is local to the worker process. It is a guardrail, not a precise global
  cross-worker guarantee.

## Invariants

1. Controls are keyed by subscription ID; one receiver must not consume another receiver's allowance.
2. Rejection before HTTP does not count as a delivery attempt.
3. Delivery rows freeze the selected max-delivery-rate value so retry and replay behavior is stable.
4. The package exposes a small `RateLimiter` interface; delivery code should not depend on concrete
   limiter internals.
5. If stronger global coordination is reintroduced later, it needs a new exec plan and explicit
   product justification.

## Verification

```bash
go test ./internal/resilience/...
go test -race ./internal/resilience/...
go test -race ./internal/kafka/...
go test ./internal/app/...
```

Update this file whenever the active algorithm, policy contract, fallback behavior, or control
ownership changes.
