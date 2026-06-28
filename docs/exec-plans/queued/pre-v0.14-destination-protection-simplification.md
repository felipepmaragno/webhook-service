# Execution Plan: Pre-v0.14 Destination Protection Simplification

> **Status:** Queued
> **Target:** Simplify destination protection before v0.14 operational readiness.
> **Depends on:** v0.13.0 terminal replay and retention
> **Followed by:** v0.14.0 operational readiness and measured capacity

## Goal

Replace the current destination-protection model with one understandable product concept:
per-destination maximum delivery rate.

This increment intentionally shrinks the v1 contract before operational readiness. Dispatch should
still protect destinations before HTTP delivery, but v1 should not expose or operate separate
`rate_limit`, `burst_size`, `concurrency_limit`, circuit-breaker state, Redis fallback/degradation,
or distributed semaphore behavior.

## Context

The completed v0.13.0 plan is recorded in `docs/exec-plans/done/v0.13.0.md`. This plan is a new
increment because simplification is independently reviewable and should not be bundled into v0.14.

The product direction is a simple project with deep execution: fewer features, stronger tests,
stronger docs, clearer behavior, and better operability. Destination protection remains valuable,
but the current policy surface is too broad for the v1 study goal.

## Accepted Contract

- Subscriptions expose one optional protection knob: `max_delivery_rate`.
- `max_delivery_rate` means sustained delivery attempts per second for that destination.
- The default is `100`.
- The value must be positive when provided.
- The selected value is frozen into delivery rows when a delivery is initialized.
- Retry and replay use the frozen delivery value, not the current subscription value.
- Rate-limited deliveries become `throttled` and are rescheduled through the normal retry path.
- A rate-limited decision does not create an HTTP delivery attempt.
- The limiter is a guardrail, not a precise cross-worker guarantee.

## Non-goals

- No circuit breaker in v1.
- No distributed semaphore in v1.
- No separate burst-size semantics in v1.
- No Redis-backed destination-protection coordination in v1.
- No token-bucket algorithm migration unless a later measurement proves the simplified limiter
  cannot satisfy the accepted v1 contract.
- No change to replay, retention, signed webhooks, retry leases, Kafka offset safety, or
  PostgreSQL outcome persistence except replacing frozen policy fields.

## Phase 1: Product and Spec Contract

- [ ] Update `product.md` so destination protection is one max-delivery-rate concept.
- [ ] Update `spec.md` API examples, field descriptions, throttling behavior, and v1 limitations.
- [ ] Update `v1-roadmap.md` release-gate wording from rate/burst/concurrency/degraded behavior to
  max-delivery-rate behavior.
- [ ] Mark ADRs for rate control, circuit breaker, Redis scaling, and distributed semaphore as
  superseded or narrowed by this simplification.
- **Verify:** docs no longer describe circuit breaker, Redis degradation, distributed semaphore,
  burst size, or concurrency limit as current v1 behavior.

## Phase 2: Simplify API, Domain, and Schema

- [ ] Replace subscription API fields `rate_limit`, `burst_size`, and `concurrency_limit` with
  `max_delivery_rate`.
- [ ] Replace domain policy fields with one max-delivery-rate value on subscriptions and deliveries.
- [ ] Update the fresh PostgreSQL schema baseline to remove obsolete policy columns and add the new
  delivery-rate column where needed.
- [ ] Update repository scans/inserts and seed data to use the simplified field.
- **Verify:** schema and repository tests prove obsolete columns are gone and delivery rows freeze
  the chosen max-delivery-rate value.

## Phase 3: Simplify Delivery Protection

- [ ] Keep one local rate limiter path for max-delivery-rate decisions.
- [ ] Remove circuit-breaker checks, success/failure recording, state callbacks, and metrics wiring.
- [ ] Remove distributed semaphore checks and Redis semaphore fallback behavior.
- [ ] Remove Redis rate-limiter wiring from worker startup if no remaining production path needs it.
- [ ] Keep `throttled` outcome scheduling for rate-limited deliveries.
- **Verify:** delivery tests cover allowed delivery, throttled delivery without attempt, retry after
  throttling, and replay using the frozen delivery limit.

## Phase 4: Remove Obsolete Runtime Surface

- [ ] Remove unused Redis resilience implementations and tests when no current path imports them.
- [ ] Remove circuit-breaker and semaphore interfaces, options, errors, metrics, docs, and examples.
- [ ] Remove `go-redis` dependency only after confirming no production or test path still needs it.
- [ ] Update Docker Compose, Kubernetes manifests, and deployment docs so Redis is no longer part of
  the v1 runtime stack.
- **Verify:** `rg` finds no current-state references to Redis destination protection, circuit
  breaker, distributed semaphore, `burst_size`, or `concurrency_limit`.

## Phase 5: Documentation and Harness Closure

- [ ] Update README, architecture, limitations, next steps, package READMEs, performance docs, and
  deployment security docs to match the simplified runtime.
- [ ] Add a learning note explaining why one destination-protection knob was kept and why the richer
  controls were removed.
- [ ] Update `PROGRESS.md` with the simplified v1 contract, validation evidence, and v0.14 as the
  next active planning target.
- [ ] Move this plan to `done/` only after implementation and validation pass.
- **Verify:** product, spec, architecture, README, package READMEs, ADRs, and progress agree about
  the simplified destination-protection model.

## Required Test Matrix

- [ ] API tests cover create/list subscription DTOs with `max_delivery_rate` and without obsolete
  policy fields.
- [ ] Domain tests cover defaulting, validation, delivery freezing, and replay/retry preservation.
- [ ] PostgreSQL tests cover fresh schema, inserts, reads, delivery initialization, and rollback.
- [ ] Kafka delivery tests cover throttling, no-attempt semantics, retry scheduling, and successful
  delivery without circuit/semaphore behavior.
- [ ] E2E tests cover happy path, retry path, replay path, and retention path without Redis.
- [ ] Config and app assembly tests prove the worker starts without Redis configuration.

## Final Verification

- [ ] Run `gofmt` on changed Go files.
- [ ] Run `go build ./...`.
- [ ] Run `go test ./...` with Docker available.
- [ ] Run the harness race-gated package suite.
- [ ] Run `golangci-lint run ./... --timeout=5m`.
- [ ] Run `git diff --check`.
- [ ] Run relative Markdown link validation.
- [ ] Run Compose rendering and Kubernetes YAML validation if those manifests remain.

## Progress Log

| Date | Step | Note |
|------|------|------|
| 2026-06-28 | Plan created | One per-destination max-delivery-rate knob accepted as the simplification target before v0.14. |
