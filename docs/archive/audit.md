# Audit — Dispatch

> Audited: 2026-06-08
> Baseline commit: `e6227c38d4d3eba91988466860211ecc0932a6b8`
>
> **This document is frozen.** It captures the state of the codebase at the time
> of the initial archaeology pass. Do not update coverage numbers here —
> current numbers live exclusively in `PROGRESS.md`.

---

## External dependencies

Services required to run the project and its full test suite:

| Dependency | Production | Tests | How to start |
|------------|------------|-------|--------------|
| PostgreSQL | Yes | Yes | `docker compose up postgres` / testcontainers (automatic) |
| Redis | Yes (optional — in-memory fallback) | Yes | `docker compose up redis` / testcontainers (automatic) |
| Kafka | Yes | No (mocks) | `docker compose up` (docker-compose.kafka.yaml) |

Tests that spin up infrastructure via testcontainers (require Docker):
- `internal/repository/postgres/` — PostgreSQL via testcontainers
- `internal/resilience/redis_circuitbreaker_test.go` — Redis via testcontainers
- `internal/resilience/redis_ratelimiter_test.go` — same

---

## Gaps vs spec (at archaeology time)

Coverage numbers below reflect the baseline (2026-06-08). See `PROGRESS.md` for current state.

| Feature in spec | Real status at baseline |
|-----------------|-------------------------|
| POST /events | Implemented, partially tested (60%) |
| GET /events/{id} | Implemented, partially tested (61.5%) |
| GET /events/{id}/attempts | Implemented, **no test** |
| POST /subscriptions | Implemented, partially tested (56.2%) |
| GET /subscriptions | Implemented, **no test** |
| DELETE /subscriptions/{id} | Implemented, partially tested (33.3%) |
| GET /health | Implemented, tested |
| GET /metrics | Implemented |
| Retry with exponential backoff | Implemented, tested (95.7%) |
| Rate limiting per subscription (in-memory) | Implemented, tested |
| Rate limiting per subscription (Redis) | Implemented, tested |
| Circuit breaker (in-memory) | Implemented, tested |
| Circuit breaker (Redis distributed) | Implemented, tested |
| Idempotency by event ID | Implemented (PostgreSQL PRIMARY KEY) — no explicit test |
| HMAC-SHA256 signature | Implemented, tested indirectly |
| Graceful shutdown | Implemented in both binaries — no automated test |
| Redis distributed semaphore | Implemented — `redis_semaphore.go` has no dedicated test |
| Kafka as event queue | Implemented — consumer/producer had no integration tests at baseline |
| `EventStatus.throttled` | Implemented in domain — **not documented in spec** (divergence) |

---

## Implicit design decisions

Decisions the code makes that are not in an ADR or diverge from the spec:

- **`throttled` as an extra status** — The spec defines 5 states (pending, processing, delivered, retrying, failed). The code has 6 — adds `throttled` for rate limit / open circuit breaker. Correct design, but not in the spec.
- **Interfaces defined at the consumer side** — `EventPublisher` lives in `internal/api/`, `EventHandler` in `internal/kafka/`, `EventProcessor` in `internal/retry/`. Idiomatic Go — but inconsistent: `EventRepository` and `SubscriptionRepository` live in `internal/repository/` (a separate package), not at the consumer.
- **Redis optional with in-memory fallback** — the worker starts without Redis and uses in-memory implementations. The spec does not document this degradation behavior.
- **Two separate binaries** (`cmd/dispatch` and `cmd/worker`) — the spec implies a single system, but the implementation has evolved toward microservices.
- **`semaphore` is `nil` when Redis is unavailable** — `initResilience` returns semaphore as `nil` in the in-memory fallback. `buildDeliveryHandler` handles this with `if semaphore != nil`. Correct behavior, undocumented.

---

## Code conventions (extracted from codebase)

Based on direct reading and greps — only what was mechanically verified:

- **Logging:** `log/slog` with `slog.NewJSONHandler` — 100% consistent across all entry points and packages
- **Error handling:** `fmt.Errorf("context: %w", err)` — 83% of occurrences (25 of 30). `errors.New` used in 5 cases for errors without wrapping.
- **Context:** first parameter — 64 of ~195 functions have `ctx context.Context` as first parameter. Consistent in I/O, absent in utilities/helpers (correct).
- **Interfaces:** majority at the consumer side (`api`, `kafka`, `retry`) — except repositories which live in `internal/repository/` (minor inconsistency).
- **Functional options pattern:** used in `kafka.DeliveryHandler` (`WithXxx` options) — consolidated pattern in this package.
- **Return structs, accept interfaces:** followed consistently.
- **Local mocks in test files:** mocks defined in the `_test.go` file itself, not in a separate package — consistent pattern.

---

## Known bugs at archaeology time

- `PublishBatch` in `kafka/producer.go` does not propagate trace ID via Kafka headers, but `Publish` does. Inconsistency — documented and captured by `TestProducer_PublishBatch_doesNotPropagateTraceID`.
