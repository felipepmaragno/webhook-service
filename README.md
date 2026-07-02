# Dispatch

Dispatch is a self-hosted webhook delivery system for one trusted environment. Producer services
submit events to an HTTP API; Dispatch publishes them to Kafka, freezes matching subscriptions into
durable delivery rows, and workers deliver those rows as signed HTTP webhooks with retries,
per-subscription rate protection, and operational visibility.

The v1 focus is reliable asynchronous delivery with understandable recovery, not broad product
scope. Dispatch is intentionally not a managed webhook SaaS, multi-tenant platform, or exactly-once
delivery system. See the [V1 summary](docs/v1-summary.md) for the final guarantees and boundaries.

## At A Glance

| Area | Current v1 shape |
|------|------------------|
| Core flow | HTTP event ingestion → Kafka → worker delivery → PostgreSQL state |
| Reliability model | At-least-once delivery; Kafka offsets commit after durable PostgreSQL outcome |
| Runtime state | Per-event/per-subscription delivery rows own status, attempts, retry leases, and replay generation |
| Recovery | Exponential retry, owner-fenced retry claims, failed-delivery replay, bounded retention |
| Destination protection | Redis-backed per-subscription `max_delivery_rate`; local limiter for single-worker development |
| Security boundary | Signed outbound webhooks; API authentication and network isolation are deployment responsibilities |
| Operability | Health/readiness, Prometheus metrics, Grafana dashboards, structured logs, deterministic smoke validation |

Start here:

```bash
make validate-basic  # clean stack, seed data, drain delivery/retry work, assert PostgreSQL state
```

The latest release evidence is recorded in [PROGRESS.md](PROGRESS.md). For product intent, behavior,
and operation details, use [product.md](docs/product.md), [spec.md](docs/spec.md), and
[operations.md](docs/operations.md).

## Documentation

- [V1 Summary](docs/v1-summary.md)
- [Product Definition](docs/product.md)
- [System Behavior Specification](docs/spec.md)
- [Architecture](docs/architecture.md)
- [Minimal Operations Guide](docs/operations.md)
- [Current Limitations and Opportunities](docs/LIMITATIONS.md)
- [V1 Roadmap and Release Gate](docs/v1-roadmap.md)
- [Verified Engineering State](PROGRESS.md)
- [Strategic Next Steps](docs/next-steps.md)

### Architecture Decision Records

| ADR | Title |
|-----|-------|
| [001](docs/adr/001-why-go.md) | Why Go |
| [002](docs/adr/002-postgresql-storage.md) | PostgreSQL as Storage |
| [003](docs/adr/003-retry-strategy.md) | Retry Strategy |
| [004](docs/adr/004-rate-limiting.md) | Rate Limiting |
| [005](docs/adr/005-circuit-breaker.md) | Circuit Breaker |
| [006](docs/adr/006-polling-vs-listen-notify.md) | Polling vs LISTEN/NOTIFY |
| [007](docs/adr/007-observability.md) | Observability |
| [008](docs/adr/008-graceful-shutdown.md) | Graceful Shutdown |
| [009](docs/adr/009-testing-strategy.md) | Testing Strategy |
| [010](docs/adr/010-library-choices.md) | Library Choices |
| [011](docs/adr/011-redis-horizontal-scaling.md) | Redis for Horizontal Scaling |
| [012](docs/adr/012-kafka-event-queue.md) | Kafka as Event Queue |
| [013](docs/adr/013-retry-poller-distributed-semaphore.md) | Retry Poller and Distributed Semaphore |
| [014](docs/adr/014-microservices-decomposition.md) | Microservices Decomposition — API vs Worker |
| [015](docs/adr/015-atomic-outcome-persistence.md) | Atomic Outcome Persistence and Kafka Commit Safety |
| [016](docs/adr/016-owner-fenced-retry-leases.md) | Owner-Fenced Retry Claim Leases |
| [017](docs/adr/017-rate-control-contract.md) | Rate-Control Contract Normalization |
| [018](docs/adr/018-per-subscription-delivery-identity.md) | Per-Subscription Delivery Identity |
| [019](docs/adr/019-per-delivery-runtime-ownership.md) | Per-Delivery Runtime Ownership |
| [020](docs/adr/020-webhook-signatures-and-secret-rotation.md) | Webhook Signatures and Secret Rotation |
| [021](docs/adr/021-delivery-replay-generations.md) | Delivery Replay Generations and Clean Runtime Cutover |
| [022](docs/adr/022-bounded-data-retention.md) | Bounded Delivery Data Retention |

## Project Structure

```
dispatch/
├── cmd/
│   ├── dispatch/       # API server
│   ├── worker/         # Kafka consumer worker
│   ├── migrate/        # Database migrations CLI
│   ├── producer/       # Kafka load-test producer (direct, bypasses API)
│   └── seed/           # Demo seeder — creates subs + events via HTTP API
├── internal/
│   ├── api/            # HTTP handlers and routes
│   ├── domain/         # Core business entities and errors
│   ├── kafka/          # Kafka consumer and delivery handler
│   ├── observability/  # Metrics, logging, health checks
│   ├── repository/     # Data access layer (PostgreSQL)
│   ├── resilience/     # Local and Redis-backed max-delivery-rate limiters
│   ├── retry/          # Exponential backoff policy
│   └── clock/          # Time abstraction for testing
├── migrations/         # SQL migrations
├── deploy/             # Prometheus/Grafana configs
├── scripts/
│   └── testserver/     # Webhook receiver (demo endpoint, /control for live config)
└── docs/
    ├── product.md      # Product purpose, users, promises, and boundaries
    ├── spec.md         # Observable behavior and system invariants
    ├── architecture.md # Architecture diagrams
    ├── LIMITATIONS.md  # Known limitations and opportunities
    ├── PERFORMANCE.md  # Benchmark results
    └── adr/            # Architecture Decision Records
```

## Services

| Service | Binary | Role | Scales |
|---------|--------|------|--------|
| **dispatch-api** | `cmd/dispatch` | HTTP API — event ingestion, subscription management | Freely (stateless) |
| **dispatch-worker** | `cmd/worker` | Kafka consumer + retry poller — webhook delivery | Up to partition count (12) |
| **receiver** | `scripts/testserver` | Local webhook endpoint for demo/testing — configurable fail rate and latency | — |
| **kafka-exporter** | `danielqsj/kafka-exporter` | Exposes consumer group lag as Prometheus metrics | — |

## Features

- **Asynchronous delivery pipeline** — API and worker are separate processes connected by Kafka.
- **Durable delivery state** — PostgreSQL stores events, frozen delivery rows, attempts, retry leases,
  replay generations, and retention state.
- **At-least-once recovery boundary** — Kafka offsets are committed only after delivery work reaches a
  durable PostgreSQL boundary.
- **Per-destination outcomes** — Each event/subscription delivery has independent status and attempt
  history.
- **Retry and replay** — Retryable work is scheduled with backoff and owner-fenced claims; failed
  deliveries can be replayed deliberately.
- **Webhook authenticity** — Subscriptions can use timestamped HMAC-SHA256 signatures over the exact
  transmitted body.
- **Destination protection** — Redis-backed `max_delivery_rate` coordinates delivery pacing across
  workers when Redis is configured.
- **Operational validation** — Unit/component tests, Testcontainers integration tests, thin E2E tests,
  and a full-stack smoke harness validate the main paths.

## Quick Start

```bash
make up          # build images + start full stack; prints all service URLs
make seed        # create 3 subscriptions, publish 50 events — watch Grafana fill up
make seed-retry  # 70% fail rate — watch retrying_total climb, then delivered_total follow
make logs        # tail dispatch-api + dispatch-worker
make down        # stop everything and wipe volumes
```

For the concise operational walkthrough, failure notes, and capacity smoke commands, see the
[minimal operations guide](docs/operations.md).

**Service URLs after `make up`:**

| Service | URL | Credentials |
|---------|-----|-------------|
| API | http://localhost:8090 | — |
| Grafana | http://localhost:3000 | admin / admin |
| Prometheus | http://localhost:9090 | — |
| Receiver (test webhook endpoint) | http://localhost:9000 | — |
| Kafka metrics | http://localhost:9308/metrics | — |

### Local Development (no Docker for app services)

```bash
# Start infrastructure only
docker compose up -d postgres redis kafka kafka-init

# Run migrations
export DATABASE_URL="postgres://postgres:postgres@localhost:5432/dispatch?sslmode=disable"
make migrate-up

# Kafka is reachable from host on port 29092
export KAFKA_BROKERS=localhost:29092

# Run API server (port 8080)
go run ./cmd/dispatch

# Run worker (port 8081 for metrics)
go run ./cmd/worker
```

## API

### Events

```bash
# Create event
curl -X POST http://localhost:8080/events \
  -H "Content-Type: application/json" \
  -d '{
    "id": "evt_123",
    "type": "order.created",
    "source": "billing-service",
    "data": {"order_id": "12345", "amount": 99.90}
  }'

# Get event status
curl http://localhost:8080/events/evt_123

# Get delivery attempts
curl http://localhost:8080/events/evt_123/attempts

# Get per-subscription delivery rows when initialized by the per-delivery model
curl http://localhost:8080/events/evt_123/deliveries
```

### Subscriptions

```bash
# Create subscription
curl -X POST http://localhost:8080/subscriptions \
  -H "Content-Type: application/json" \
  -d '{
    "id": "sub_123",
    "url": "https://example.com/webhook",
    "event_types": ["order.*"],
    "secret": "my-secret-key",
    "max_delivery_rate": 100
  }'

# List subscriptions
curl http://localhost:8080/subscriptions

# Rotate a subscription secret (response never includes the secret)
curl -X PUT http://localhost:8080/subscriptions/sub_123/secret \
  -H "Content-Type: application/json" \
  -d '{"secret":"replacement-secret"}'

# Delete subscription
curl -X DELETE http://localhost:8080/subscriptions/sub_123

# Replay one failed delivery (202 means durably scheduled, not delivered)
curl -X POST http://localhost:8080/deliveries/dlv_123/replay
```

## Configuration

### dispatch-api

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `DATABASE_URL` | `postgres://...` | PostgreSQL connection string |
| `KAFKA_BROKERS` | `localhost:9092` | Kafka broker addresses (comma-separated) |
| `KAFKA_TOPIC` | `events.pending` | Kafka topic for events |
| `ADDR` | `:8080` | HTTP server address |

### dispatch-worker

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `DATABASE_URL` | `postgres://...` | PostgreSQL connection string |
| `REDIS_URL` | unset | Redis connection string for distributed `max_delivery_rate`; unset uses local limiter |
| `KAFKA_BROKERS` | `localhost:9092` | Kafka broker addresses |
| `KAFKA_TOPIC` | `events.pending` | Kafka topic to consume |
| `KAFKA_CONSUMER_GROUP` | `dispatch-workers` | Consumer group ID |
| `INSTANCE_ID` | `worker-1` | Unique worker instance ID (use pod name in k8s) |
| `METRICS_ADDR` | `:8081` | Metrics HTTP server address |
| `DB_MAX_CONNS` | `30` | Database connection pool size |
| `RETRY_POLL_INTERVAL` | `5s` | Retry poller interval |
| `RETRY_BATCH_SIZE` | `100` | Max events per retry poll |
| `RETRY_MAX_CONCURRENT_BATCHES` | `1` | Maximum retry batches processed concurrently |
| `RETRY_LEASE_DURATION` | `30s` | Claim lifetime; keep above expected delivery processing time |
| `ATTEMPT_BODY_RETENTION` | `168h` | Age after which attempt response bodies are redacted |
| `EVENT_RETENTION` | `720h` | Age after which terminal event history is deleted |
| `RETENTION_CLEANUP_INTERVAL` | `1h` | Interval between bounded cleanup cycles |
| `RETENTION_BATCH_SIZE` | `1000` | Maximum rows processed by each cleanup operation |

## Development

```bash
# Full local suite
make test

# Fast unit/component validation
make test-unit

# Docker-backed integration tests
make test-integration

# End-to-end smoke tests (API + Kafka + worker + retry flow)
make test-e2e

# Run tests with coverage
make test-cover

# Lint
make lint

# Local CI-style validation
make validate-ci-local
```

### Validation Pipeline

The automated validation pipeline is layered:

- `test-unit` — fast unit/component coverage with race detector
- `test-integration` — Testcontainers-backed PostgreSQL and Redis validation
- `test-e2e` — thin smoke coverage for API → Kafka → delivery/retry flow
- `validate-ci-local` — local build, lint, unit, integration, and e2e validation
- `validate-basic` — full-stack smoke flow using Docker Compose, deterministic seed data,
  database assertions, evidence capture, and cleanup
- `load-test` — non-blocking heavier validation on `main`

`test-integration` and `test-e2e` require Docker because they start real dependencies with Testcontainers.

For local capacity characterization, including deterministic setup, seeding, PostgreSQL checks,
metrics capture, and retry-backlog validation, use `make perf-smoke` for an explicit performance
smoke run or `make perf-baseline` for the complete run. `make validate-basic` intentionally reuses
the smoke harness as the default functional full-stack check. See the
[performance validation guide](docs/performance-validation-guide.md) for generated evidence and
configuration. Historical measurements are retained in the [performance report](docs/PERFORMANCE.md).

## Architecture

The diagram below is an operational summary. [Architecture](docs/architecture.md) is the
technical authority; [the system specification](docs/spec.md) defines observable behavior.

```mermaid
flowchart LR
    Producer[Producer Service] -->|POST /events| API[dispatch-api]
    API -->|publish event| Kafka[(Kafka)]

    subgraph WorkerRuntime[dispatch-worker]
        Consumer[Kafka consumer]
        Retry[Retry poller]
        Delivery[Delivery handler]
        WorkerObs[metrics/readiness]
    end

    Kafka -->|consume| Consumer
    Consumer --> Delivery
    Retry --> Delivery
    Delivery -->|claim/persist outcome| DB[(PostgreSQL)]
    Delivery -->|check max_delivery_rate| Redis[(Redis)]
    Delivery -->|signed HTTP POST| Receiver[Webhook Receiver]
    API -->|queries, subscriptions, replay| DB

    API -->|/metrics /ready| Prom[Prometheus]
    WorkerObs -->|/metrics /ready| Prom
    Prom --> Grafana[Grafana]
```

### Event Lifecycle

```mermaid
stateDiagram-v2
    [*] --> pending: Event received
    pending --> processing: Worker picks up
    processing --> delivered: 2xx response
    processing --> retrying: Error + retries left
    processing --> throttled: Rate limited
    processing --> failed: Error + no retries
    retrying --> processing: Retry scheduled
    throttled --> processing: Rescheduled (attempt not incremented)
    delivered --> [*]
    failed --> [*]
```

## Delivery Contract

- **Success:** HTTP status `2xx` (200-299)
- **Failure:** HTTP status `4xx`, `5xx`, timeout, connection error
- **Timeout:** 10 seconds (configurable)

### Headers sent to endpoints

| Header | Description |
|--------|-------------|
| `X-Event-ID` | Event ID |
| `X-Event-Type` | Event type |
| `X-Trace-ID` | Trace ID for end-to-end correlation |
| `X-Dispatch-Timestamp` | Unix seconds used in the signed message when a secret is configured |
| `X-Dispatch-Signature` | `v1=` plus HMAC-SHA256 over `timestamp + "." + raw body` |

Receivers must verify the exact raw body in constant time, enforce an appropriate timestamp
tolerance, and still deduplicate by event ID. See [deployment security](docs/deployment-security.md).

## Metrics

Prometheus metrics scraped from two endpoints: `dispatch-api:8080/metrics` and `dispatch-worker:8081/metrics`. Consumer group lag from `kafka-exporter:9308/metrics`.

**API** (`dispatch_` prefix):

| Metric | Type | Description |
|--------|------|-------------|
| `dispatch_events_received_total` | Counter | Events published via API |
| `dispatch_http_requests_total` | Counter | HTTP requests by method, path, status |
| `dispatch_http_request_duration_seconds` | Histogram | HTTP latency by method, path |

**Worker** (`dispatch_worker_` prefix):

| Metric | Type | Description |
|--------|------|-------------|
| `dispatch_worker_events_delivered_total` | Counter | Successfully delivered events |
| `dispatch_worker_events_failed_total` | Counter | Permanently failed events |
| `dispatch_worker_events_retrying_total` | Counter | Events scheduled for retry |
| `dispatch_worker_events_throttled_total` | Counter | Throttled by destination max-delivery-rate |
| `dispatch_worker_delivery_duration_seconds` | Histogram | Per-attempt HTTP latency |
| `dispatch_worker_delivery_attempts_total` | Counter | Total HTTP attempts (includes retries) |
| `dispatch_worker_rate_limiter_rejections_total` | Counter | Rate limit hits per subscription |
| `dispatch_worker_retry_events_claimed_total` | Counter | Retry events claimed for processing |
| `dispatch_worker_retry_events_reclaimed_total` | Counter | Expired retry claims recovered |
| `dispatch_worker_retry_active_batches` | Gauge | Retry batches currently processing on this worker |
| `dispatch_worker_retry_due_events` | Gauge | Due retry/throttled events awaiting claim |
| `dispatch_worker_retry_expired_claims` | Gauge | Processing rows whose lease expired |
| `dispatch_worker_retry_leased_events` | Gauge | Processing rows with an active lease |
| `dispatch_worker_retry_oldest_due_age_seconds` | Gauge | Age of the oldest due retry or expired claim |
| `dispatch_worker_retry_scheduling_lag_seconds` | Histogram | Delay from eligibility to claim |
| `dispatch_worker_retry_claim_failures_total` | Counter | Failed retry claim operations |
| `dispatch_worker_retry_persistence_failures_total` | Counter | Retry outcome batches that failed persistence |

**Kafka exporter:**

| Metric | Type | Description |
|--------|------|-------------|
| `kafka_consumergroup_lag` | Gauge | Per-partition consumer lag for `dispatch-workers` group |

## Resilience

Destination protection is intentionally narrow for v1. Each subscription has one optional
`max_delivery_rate` value, defaulting to 100 delivery attempts per second. When `REDIS_URL` is
configured, workers enforce that value through a Redis sliding-window limiter shared across
instances. When `REDIS_URL` is absent, workers use a local limiter for development and
single-worker operation.

If Redis is configured but unavailable, rate-limit decisions fail closed as `throttled`, are
scheduled through the normal retry path, and do not consume an HTTP attempt. V1 does not operate
circuit breakers, distributed semaphores, or separate burst/concurrency subscription knobs.

## Health And Readiness

`/health` is a shallow liveness endpoint. It returns success when the process can serve HTTP and
does not check PostgreSQL, Kafka, or Redis.

`/ready` is dependency-aware. API readiness checks PostgreSQL and Kafka topic metadata. Worker
readiness checks PostgreSQL, Kafka topic metadata, and Redis when `REDIS_URL` is configured.
Readiness responses expose only safe dependency names and statuses.

### Retry Scheduler Capacity

The retry poll interval controls how quickly idle workers discover new due work; it is not
a throughput limit. After a full claim, the scheduler immediately claims another batch
while `RETRY_MAX_CONCURRENT_BATCHES` has capacity. An empty or partial claim returns it to
interval-based waiting. Database pool, destination rate limits, worker count, Kafka partitions,
and HTTP latency remain the practical delivery boundaries.

## License

MIT
