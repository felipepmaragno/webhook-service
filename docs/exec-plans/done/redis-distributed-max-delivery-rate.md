# Execution Plan: Redis Distributed Max Delivery Rate

> **Status:** Done
> **Target:** Restore Redis only for distributed `max_delivery_rate` enforcement.
> **Depends on:** Pre-v0.14 destination-protection simplification

## Goal

Make `max_delivery_rate` globally meaningful across worker instances while preserving the simplified
v1 destination-protection contract.

Redis returns as a rate-limiter-only dependency. Circuit breakers, distributed semaphores,
`burst_size`, and `concurrency_limit` stay out of the v1 runtime.

## Accepted Contract

- Subscriptions still expose one public knob: `max_delivery_rate`.
- `max_delivery_rate` means delivery attempts per second for one subscription.
- When `REDIS_URL` is configured, workers use Redis sliding-window enforcement shared across
  instances.
- When `REDIS_URL` is absent, workers use local in-memory enforcement for dev and single-worker
  operation.
- When `REDIS_URL` is configured but Redis is unavailable, delivery decisions fail closed as
  `throttled` with a retry delay.
- Redis outage does not crash the worker after startup; it creates observable throttling/backlog.
- No HTTP attempt is created for a Redis-denied or Redis-unavailable decision.

## Non-goals

- No circuit breaker.
- No semaphore.
- No separate burst semantics.
- No `concurrency_limit`.
- No NATS or messaging abstraction work.
- No performance tuning beyond validating correctness and startup behavior.

## Phase 1: Redis Limiter

- [x] Add back `go-redis` and Redis Testcontainers dependencies.
- [x] Implement Redis sliding-window limiter against the current `RateLimiter` interface.
- [x] Use `domain.DefaultSubscriptionMaxDeliveryRate` for missing policies.
- [x] Return `Allowed=false` and positive `RetryAfter` on Redis errors when Redis mode is active.
- [x] Keep the local limiter unchanged for absent `REDIS_URL`.
- **Verify:** unit/contract tests cover allowed, denied, default policy, subscription isolation, and
  Redis error fail-closed behavior.

## Phase 2: Worker Wiring

- [x] Add `REDIS_URL` back to worker config.
- [x] If `REDIS_URL` is absent, wire the local limiter and log local mode.
- [x] If `REDIS_URL` is present and ping succeeds, wire Redis limiter and close Redis on shutdown.
- [x] If `REDIS_URL` is present and ping fails, still wire Redis limiter so decisions fail closed.
- [x] Preserve worker startup and shutdown behavior without circuit/semaphore wiring.
- **Verify:** config and app assembly tests prove local mode, Redis mode, and Redis-unavailable mode.

## Phase 3: Runtime Manifests and Docs

- [x] Restore Redis services/env vars in Docker Compose where multi-worker rate enforcement matters.
- [x] Restore `REDIS_URL` in Kubernetes worker deployment and deployment docs.
- [x] Update README, product/spec, architecture, limitations, package READMEs, and ADR notes to
  describe Redis-backed `max_delivery_rate` only.
- [x] Update `PROGRESS.md` with the new contract and validation evidence.
- **Verify:** current-state docs do not describe circuit breaker, semaphore, burst size, or
  concurrency limit as active behavior.

## Required Test Matrix

- [x] Local in-memory limiter contract tests.
- [x] Redis limiter contract tests with Testcontainers.
- [x] Redis unavailable fail-closed test without host-local Redis assumptions.
- [x] Kafka delivery test proving rate-limit denial writes `throttled` and no attempt.
- [x] App/config tests proving startup wiring decisions.

## Final Verification

- [x] `gofmt` on changed Go files.
- [x] `go build ./...`.
- [x] `go test ./...`.
- [x] Race-gated harness package suite where environment permits local listeners.
- [x] `golangci-lint run ./... --timeout=5m`.
- [x] `git diff --check`.
- [x] Relative Markdown link validation.
- [x] Compose rendering and Kubernetes YAML parse.

## Progress Log

| Date | Step | Note |
|------|------|------|
| 2026-06-29 | Plan created | Redis returns only to make `max_delivery_rate` distributed across worker instances. |
| 2026-06-29 | Implemented | Redis sliding-window limiter, worker wiring, runtime manifests, and current-state docs updated. |
| 2026-06-29 | Verified | Build, full tests, race-gated suite, lint, Compose rendering, Kubernetes YAML parse, Markdown links, and diff check passed. |
