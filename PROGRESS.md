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
| `docs/exec-plans/done/` | Completed plans (historical reference) |
| `docs/next-steps.md` | Strategic direction options — only relevant before an exec plan is chosen |
| `docs/audit.md` | Archaeology snapshot — frozen after v0.1.0. Do not update coverage numbers here. |

**Workflow:**
1. Read this file.
2. If there is a file in `docs/exec-plans/active/` → continue from the first unchecked step.
3. If `active/` is empty → read `docs/next-steps.md`, choose a direction, create a new exec plan in `active/`.
4. When an exec plan is fully done → check all boxes, move it to `done/`, update this file.

---

## Verified state — 2026-06-09 (after exec plan v0.2.0)

| Check | Result |
|-------|--------|
| `go build ./...` | PASS |
| `go test ./...` | PASS — 0 failures |
| `go test -race ./...` | PASS (api + kafka) |
| golangci-lint | Not installed — not run |

### Coverage per package

| Package | Coverage |
|---------|----------|
| `internal/config` | 98.0% |
| `internal/retry` | 95.7% |
| `internal/domain` | 90.0% |
| `internal/repository/postgres` | 89.8% |
| `internal/kafka` | 64.2% |
| `internal/resilience` | 56.8% |
| `internal/api` | 55.4% |
| `internal/observability` | 39.1% |
| `internal/clock` | 0.0% |
| `cmd/*` | 0.0% |
| **Total** | **51.9%** |

---

## What is mechanically verified to work

- Event state machine: pending → processing → delivered / retrying / throttled / failed
- Subscription wildcard matching (`order.*`, `*`)
- Config parsing for API and Worker
- Delivery pipeline: `ProcessBatch` → `deliverEvent` → `deliverWebhook`
- HMAC-SHA256 signature on delivery
- Retry with exponential backoff
- Rate limiting per subscription (in-memory and Redis)
- Circuit breaker per subscription (in-memory and Redis)
- EventBatcher batch inserts to PostgreSQL
- Retry poller
- Health and readiness handlers
- Prometheus metrics
- All EventRepository operations tested against real PostgreSQL (testcontainers)
- All SubscriptionRepository operations tested against real PostgreSQL
- Kafka consumer: collect/process/commit tested with injected fakeReader
- Kafka producer: Publish/PublishBatch tested with injected fakeWriter
- API handlers: all endpoints covered including error paths

---

## Known gaps (residual after v0.2.0)

| Gap | Risk | Notes |
|-----|------|-------|
| `observability` at 39.1% | Medium | Logging middleware untested |
| `PublishBatch` does not propagate trace ID | Low | Documented — `producer_test.go` captures it |
| Redis semaphore has no dedicated test | Low | Covered indirectly |
| `NewRouter` not tested | Low | Route wiring untested |
| `cmd/*` bootstrap untested | Low | Acceptable |

---

## Active exec plan

None. `docs/exec-plans/active/` is empty.

Next session: read `docs/next-steps.md` and choose a direction (Direction 2: kafka/ reorganization, or Direction 3: observability).
