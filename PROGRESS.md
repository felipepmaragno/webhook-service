# Progress — Dispatch

> **Read this file first every session.**
> It is the single source of truth for current project state.
> Do not duplicate the coverage table in audit.md or anywhere else.

---

## How the harness works

| File | Purpose |
|------|---------|
| `PROGRESS.md` (this file) | Verified state + coverage + where to start next session |
| `docs/exec-plans/active/` | The active step-by-step plan with checkboxes. One file at a time. |
| `docs/exec-plans/queued/` | Decision-complete follow-up plans, ordered by dependency. Not executable until promoted to `active/`. |
| `docs/exec-plans/done/` | Completed plans (historical reference) |
| `docs/next-steps.md` | Strategic direction options — only relevant before an exec plan is chosen |
| `docs/audit.md` | Archaeology snapshot — frozen after v0.1.0. Do not update coverage numbers here. |
| Critical package `README.md` files | Local implementation map, invariants, hazards, and verification guidance for coding agents |

**Workflow:**
1. Read this file.
2. If there is a file in `docs/exec-plans/active/` → continue from the first unchecked step.
3. If `active/` is empty and `queued/` contains a dependency-ready plan → promote the next queued plan to `active/`.
4. If both are empty → read `docs/next-steps.md`, choose a direction, create a new exec plan in `active/`.
5. When an exec plan is fully done → check all boxes, move it to `done/`, update this file.

---

## Verified state — 2026-06-12 (after exec plan v0.6.0)

| Check | Result |
|-------|--------|
| `GOCACHE=/tmp/dispatch-gocache go build ./...` | PASS |
| `GOCACHE=/tmp/dispatch-gocache go test -race ./internal/api/... ./internal/config/... ./internal/domain/... ./internal/kafka/... ./internal/observability/... ./internal/retry/...` | PASS |
| `GOCACHE=/tmp/dispatch-gocache go test ./internal/repository/postgres/... ./internal/resilience/...` | PASS — Testcontainers PostgreSQL + Redis |
| `GOCACHE=/tmp/dispatch-gocache go test ./internal/app/...` | PASS — Testcontainers E2E |
| `GOCACHE=/tmp/dispatch-gocache GOLANGCI_LINT_CACHE=/tmp/dispatch-golangci-cache /tmp/dispatch-bin/golangci-lint run --timeout=5m` | PASS — 0 issues |

Coverage updated after the new infra-backed and E2E suites: 49.7% total.

### Coverage per package

| Package | Coverage |
|---------|----------|
| `internal/app` | 38.0% |
| `internal/config` | 98.0% |
| `internal/retry` | 95.7% |
| `internal/domain` | 90.0% |
| `internal/repository/postgres` | 89.8% |
| `internal/kafka` | 57.8% |
| `internal/resilience` | 65.4% |
| `internal/api` | 55.4% |
| `internal/observability` | 39.1% |
| `internal/clock` | 0.0% |
| `cmd/*` | 0.0% |
| **Total** | **49.7%** |

---

## What is mechanically verified to work

- Event state machine: pending → processing → delivered / retrying / throttled / failed
- Subscription wildcard matching (`order.*`, `*`)
- Config parsing for API and Worker
- Delivery pipeline: `ProcessBatch` → `deliverEvent` → `deliverWebhook`
- Optional `X-Signature` delivery header (currently a non-cryptographic placeholder)
- Retry with exponential backoff
- Rate limiting per subscription (in-memory and Redis)
- Circuit breaker per subscription (in-memory and Redis)
- Redis-backed resilience tests run with Testcontainers instead of host-local Redis assumptions
- `StateChangeNotifier` wired to Prometheus: CB state gauge + trip counter live
- Rate limiter rejection counter live (per subscription ID label)
- Delivery attempts counter live
- Consumer group lag exposed via kafka-exporter → Prometheus → Grafana
- EventBatcher batch inserts to PostgreSQL
- Retry poller
- Health and readiness handlers
- Prometheus metrics (all 12 registered metrics now have live data in worker)
- All EventRepository operations tested against real PostgreSQL (testcontainers)
- All SubscriptionRepository operations tested against real PostgreSQL
- Kafka consumer: collect/process/commit tested with injected fakeReader
- Kafka producer: Publish/PublishBatch tested with injected fakeWriter
- API handlers: all endpoints covered including error paths
- Thin E2E smoke path: API → Kafka → delivery → persisted status `delivered`
- Thin E2E retry path: first delivery fails, retry poller reprocesses, event becomes `delivered`
- Event outcome and its generated delivery attempts commit or roll back in one PostgreSQL transaction
- Kafka offsets are not committed when outcome persistence fails
- The same Kafka batch can be processed and committed after persistence recovers
- Duplicate event delivery keeps one event row while retaining durably committed repeated attempts
- Retry-poller processing surfaces persistence failures instead of reporting a successful batch
- `make up` → `make seed` → Grafana dashboard flow verified (build-level)

---

## Known gaps (residual after v0.3.0)

| Gap | Risk | Notes |
|-----|------|-------|
| `observability` at 39.1% | Medium | Logging middleware still untested |
| `PublishBatch` does not propagate trace ID | Low | Documented — `producer_test.go` captures it |
| Redis semaphore has no dedicated test | Low | Covered indirectly |
| `NewRouter` not tested | Low | Route wiring untested |
| `cmd/*` bootstrap untested | Low | Internal app bootstrap is covered; command wrappers still are not |
| Kafka consumer-group coordination not covered by E2E smoke | Medium | Thin E2E uses a direct partition reader harness for deterministic validation |
| `make up && make seed` compose demo flow not tested in CI | Low | PR gate now has infra-backed integration + E2E smoke, but compose demo remains manual |
| Retry claims can remain `processing` after a worker crash | High | v0.7.0 adds expiring owner-fenced leases and stale-worker rejection |
| HTTP calls may be duplicated after persistence failure | Expected | Required by at-least-once recovery; receivers should deduplicate by event ID |
| HTTP calls followed by database failure may be absent from attempt history | Medium | PostgreSQL cannot record a transaction that did not commit |
| Delivery attempts do not identify the subscription | Medium | Fan-out attempts cannot yet be uniquely audited per destination |
| One persistence failure redelivers the entire Kafka batch | Medium | Preserves safety but may repeat calls that had already succeeded |
| `X-Signature` is not cryptographic HMAC-SHA256 | High | `computeHMAC` is a placeholder; do not rely on it as receiver authentication |

---

## Active exec plan

`docs/exec-plans/active/v0.7.0.md` — retry claim leases and crash recovery.

Queued sequence:

1. `docs/exec-plans/queued/v0.8.0.md` — retry poller throughput and observability

Next session: begin v0.7.0 with its ADR and lease-model contract tests.
