# Progress — Dispatch

> **Read this file first every session.**
> It is the single source of truth for current project state.
> Do not duplicate the coverage table in audit.md or anywhere else.

---

## How the harness works

| File | Purpose |
|------|---------|
| `PROGRESS.md` (this file) | Verified state + coverage + where to start next session |
| `docs/product.md` | Current product problem, users, promises, boundaries, maturity, and accepted v1 direction |
| `docs/v1-roadmap.md` | Accepted finite v1 sequence, release gate, non-goals, and feature-freeze rule |
| `docs/spec.md` | Current externally observable behavior and system invariants |
| `docs/architecture.md` + ADRs | Implementation structure and accepted technical rationale |
| `docs/exec-plans/active/` | The active step-by-step plan with checkboxes. One file at a time. |
| `docs/exec-plans/queued/` | Decision-complete follow-up plans, ordered by dependency. Not executable until promoted to `active/`. |
| `docs/exec-plans/done/` | Completed plans (historical reference) |
| `docs/next-steps.md` | Strategic direction options — only relevant before an exec plan is chosen |
| `docs/spikes/` | Proposed architectural investigations; not accepted decisions or executable plans |
| `docs/audit.md` | Archaeology snapshot — frozen after v0.1.0. Do not update coverage numbers here. |
| Critical package `README.md` files | Local implementation map, invariants, hazards, and verification guidance for coding agents |

**Workflow:**
1. Read this file.
2. If there is a file in `docs/exec-plans/active/` → continue from the first unchecked step.
3. If `active/` is empty and `queued/` contains a dependency-ready plan → promote the next queued plan to `active/`.
4. If both are empty → read `docs/next-steps.md`, choose a direction, create a new exec plan in `active/`.
5. When an exec plan is fully done → check all boxes, move it to `done/`, update this file.

---

## Verified state — 2026-06-20 (v0.13.0 replay, retention, and clean-slate cleanup complete)

| Check | Result |
|-------|--------|
| `GOCACHE=/tmp/dispatch-go-cache go build ./...` | PASS |
| Fast API/domain/config/observability/retention/retry tests | PASS |
| `GOCACHE=/tmp/dispatch-go-cache go test ./...` | PASS — Testcontainers PostgreSQL/Redis and replay E2E included |
| `/tmp/dispatch-bin/golangci-lint run ./... --timeout=5m` | PASS — 0 issues |
| `git diff --check` | PASS |
| CI race-gated API/config/domain/Kafka/observability/retention/retry suite | PASS |
| Compose, Kubernetes/Prometheus YAML, dashboard JSON, relative Markdown links, `git diff --check` | PASS |
| Retry scheduler benchmark, 20 batches × 5 events, 2ms synthetic work | PASS — 44.3ms at concurrency 1; 11.2ms at concurrency 4 |

The last full-suite coverage baseline is 49.7%. Focused v0.12.0 coverage runs updated the
changed API and Kafka packages; recompute total coverage during v1 release hardening.

### Coverage per package

| Package | Coverage |
|---------|----------|
| `internal/app` | 38.0% |
| `internal/config` | 98.0% |
| `internal/retry` | 95.7% |
| `internal/domain` | 90.0% |
| `internal/repository/postgres` | 89.8% |
| `internal/kafka` | 68.5% |
| `internal/resilience` | 65.4% |
| `internal/api` | 69.0% |
| `internal/observability` | 39.1% |
| `internal/clock` | 0.0% |
| `cmd/*` | 0.0% |
| **Total** | **49.7% pre-v0.12 baseline** |

---

## What is mechanically verified to work

- Event state machine: pending → processing → delivered / retrying / throttled / failed
- Subscription wildcard matching (`order.*`, `*`)
- Config parsing for API and Worker
- Delivery pipeline: `ProcessBatch` → frozen delivery initialization → `ProcessDeliveries` → `deliverWebhook`
- Timestamped `X-Dispatch-Signature` HMAC-SHA256 over the exact webhook body
- Published language-independent signing vector and raw-body receiver verification
- Subscription secrets are write-only in API create, list, and rotation responses
- Active subscription secret rotation preserves frozen secrets for existing delivery retries
- Retry with exponential backoff
- Rate limiting per subscription (in-memory and Redis)
- Subscription rate-control contract separates sustained rate, burst capacity, and concurrency limit
- Redis rate limiter receives subscription policy instead of a fixed global rate constant
- Local and distributed semaphores use `concurrency_limit`, not `rate_limit`
- Rate-limit, circuit-open, and semaphore-full decisions persist `throttled` without incrementing attempts
- Redis rate-limiter fallback exposes degraded decisions and transition logs
- Per-subscription `deliveries` table with stable event/subscription identity
- Every delivery attempt has non-null, matching event, delivery, and subscription attribution
- Repository can initialize a frozen event delivery set idempotently before external HTTP calls
- Repository can persist one delivery outcome and attributed attempts atomically
- Repository claims deliveries with owner/deadline fencing for Kafka-initialized and retry-originated work
- API exposes `GET /events/{id}/deliveries` for initialized delivery rows
- Circuit breaker per subscription (in-memory and Redis)
- Redis-backed resilience tests run with Testcontainers instead of host-local Redis assumptions
- `StateChangeNotifier` wired to Prometheus: CB state gauge + trip counter live
- Rate limiter rejection counter live (per subscription ID label)
- Delivery attempts counter live
- Consumer group lag exposed via kafka-exporter → Prometheus → Grafana
- Retry poller
- Health and readiness handlers
- Prometheus delivery, retry, and retention metrics have live worker sources
- PostgreSQL delivery, replay, migration, and retention operations tested against real PostgreSQL (Testcontainers)
- All SubscriptionRepository operations tested against real PostgreSQL
- Kafka consumer: collect/process/commit tested with injected fakeReader
- Kafka producer: Publish/PublishBatch tested with injected fakeWriter
- API handlers: all endpoints covered including error paths
- Thin E2E smoke path: API → Kafka → delivery → persisted status `delivered`
- Thin E2E retry path: first delivery fails, retry poller reprocesses, event becomes `delivered`
- Delivery outcome and its generated delivery attempt commit or roll back in one PostgreSQL transaction
- Kafka offsets are not committed when outcome persistence fails
- Duplicate Kafka processing reuses the frozen delivery set and skips already delivered destinations
- Later subscription changes do not silently add destinations to an initialized event
- Retry-poller processing surfaces persistence failures instead of reporting a successful batch
- API, Kafka, and retry packages depend on role-specific repository interfaces instead of the full concrete event repository contract
- Kafka delivery observability is emitted through `DeliveryObserver`; Prometheus metric names and labels remain owned by app wiring
- Redis and in-memory rate limiter/circuit breaker implementations share contract tests for policy limits, retry delays, subscription isolation, defaults, and state transitions
- The schema has one per-delivery runtime model; aggregate execution, event-level leases, and
  nullable attempt compatibility are absent
- Migrations are a clean fresh-installation baseline because no deployed schema requires upgrades
- Failed deliveries can be replayed through the API as explicit generations without rewriting prior attempts
- Replay schedules the normal fenced retry path; concurrent replay requests serialize to one accepted transition
- Attempt response bodies and terminal event history have bounded, multi-worker-safe retention cleanup
- Retention failures, duration, redaction/deletion counts, and last success are observable
- Delivery retry claims atomically record worker owner and expiration deadline
- Expired processing claims are reclaimed after worker failure
- Owner plus exact deadline fences stale delivery outcome writes, including repeated claims by one instance ID
- Every persisted delivery retry outcome clears lease metadata in the same transaction as state, attempts, and event projection
- Concurrent PostgreSQL claimers cannot own the same current lease
- Retry scheduler enforces `RETRY_MAX_CONCURRENT_BATCHES` and never starts overlapping claim loops
- Full retry batches drain immediately while capacity exists; empty/partial claims return to interval waiting
- Shutdown waits for the claim coordinator and all tracked retry batches
- Retry backlog count, oldest age, active/expired leases, scheduling lag, and scheduler failures are exposed as metrics
- PostgreSQL delivery backlog aggregation uses the retry-claim index under the validated access plan
- Seeded 25-delivery E2E retry backlog drains through the real handler and receiver without stuck leases
- `make up` → `make seed` → Grafana dashboard flow verified (build-level)
- `make validate-basic` runs the full-stack smoke harness with deterministic seed data,
  PostgreSQL correctness assertions, evidence capture, and cleanup

## Documentation model — 2026-06-14

- `docs/product.md` now defines the current product problem, users, promise, boundaries,
  maturity, and accepted v1 direction.
- `docs/spec.md` is limited to observable behavior and system invariants.
- Architecture, ADRs, package READMEs, exec plans, progress, next steps, and spikes have
  explicit non-overlapping authority.
- Current positioning is self-hosted webhook infrastructure for one trusted environment;
  external-product and managed-service directions are explicitly outside v1.
- The spec now records important implementation-backed boundaries: status may lag `202`
  acceptance, delivery is at least once, event state is a delivery projection, every attempt
  identifies its destination, and pre-HTTP backpressure is persisted
  as `throttled`.
- `docs/v1-roadmap.md` defines v0.8.0 through v1.0.0 as the finite remaining sequence;
  completing v1 ends planned feature development for this project.

Verification for the documentation increment:

| Check | Result |
|-------|--------|
| Relative Markdown links | PASS |
| `git diff --check` | PASS |
| `GOCACHE=/tmp/dispatch-gocache go build ./...` | PASS |
| Race-gated unit/component package suite | PASS |

## Performance characterization — 2026-06-15

- `make perf-smoke` automates clean Compose setup, schema preparation, deterministic seeding,
  timed drains, PostgreSQL integrity assertions, evidence capture, and cleanup.
- `make perf-baseline` extends the same harness to 10,000 new events and 100,000 due retries.
- The first baseline accepted 10,000 events at 11,777 events/s and drained all of them from
  Kafka with exactly 10,000 attempts and no remaining leases.
- The due retry backlog drained 100,000 events at 957.58 events/s with no failed rows,
  remaining leases, claim failures, persistence failures, or stale-owner rejections.
- The Kafka drain rate includes worker startup and consumer-group rebalance. It is a cold-start
  recovery diagnostic, not evidence for or against the 1,000/s sustained-delivery target.
- Performance evidence is generated under `artifacts/performance/` and is not committed.

Validation for the automation increment:

| Check | Result |
|-------|--------|
| `make perf-smoke` | PASS |
| `make perf-baseline` | PASS |
| API acceptance reference (10,000/s) | PASS — 11,777/s |
| Kafka delivery integrity | PASS — 10,000 delivered, zero leases |
| Retry backlog integrity | PASS — 100,000 delivered, zero leases/failures |

## Accepted v1 direction — 2026-06-14

- Dispatch v1 is a production-conscious, self-hosted webhook delivery service for one
  trusted organization.
- Reliability and understandable recovery are the differentiator; simplicity limits
  further infrastructure and algorithm complexity.
- Per-subscription delivery identity and runtime ownership replace aggregate fan-out state before v1.
- Token bucket is no longer automatic roadmap work; it requires evidence from v0.9.0 and
  an explicit promotion decision.
- Multi-tenancy, managed operation, UI, transformations, ordering, multi-region, and
  speculative scale are outside v1.
- `docs/v1-roadmap.md` defines the finite sequence, release gate, and feature freeze.
- V1.0.0 adds no product features and completing it ends planned feature development.

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
| HTTP calls may be duplicated after persistence failure | Expected | Required by at-least-once recovery; receivers should deduplicate by event ID |
| HTTP calls followed by database failure may be absent from attempt history | Medium | PostgreSQL cannot record a transaction that did not commit |
| Persistence failure leaves the Kafka batch uncommitted | Expected | Messages are fetched again, but active leases and terminal delivery state suppress blind immediate HTTP repetition |
| Subscription and frozen delivery secrets are plaintext in PostgreSQL/backups | Accepted | Protect datastore transport, access, storage, exports, and backups at deployment level |
| Signed webhook requests can still be replayed or duplicated | Expected | Receivers enforce timestamp tolerance and deduplicate by event ID |
| Retry work may be duplicated after lease expiry | Expected | Lease recovery favors liveness; owner+deadline fencing prevents stale database writes but cannot undo HTTP calls |
| Redis sliding-window rate limiter does not provide independent burst semantics | Low | `burst_size` is in the contract; Redis token-bucket behavior remains a spike candidate |

---

## Active exec plan

None. V0.13.0 is complete. The next harness action is to write and review the v0.14.0 operational
readiness and measured-capacity exec plan before implementation.

Queued sequence: the broader API contract hardening plan remains unversioned. Its secret
redaction slice was completed in v0.12.0; other work requires a later promotion decision.

Next session: continue v0.13.0 from the first unchecked step.
