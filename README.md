# Dispatch

Self-hosted asynchronous webhook delivery for a single trusted environment. Dispatch accepts events, routes them to matching destinations, retries recoverable failures, and exposes delivery state.

Start with the [product definition](docs/product.md) to understand the problem, target users, guarantees, boundaries, and accepted v1 direction. This README is the operator and developer entry point.

The accepted finish line is the [v1 roadmap](docs/v1-roadmap.md). V1 deliberately remains
self-hosted and single-trust-domain; multi-tenancy and managed-service features are out of scope.

## Services

| Service | Binary | Role | Scales |
|---------|--------|------|--------|
| **dispatch-api** | `cmd/dispatch` | HTTP API — event ingestion, subscription management | Freely (stateless) |
| **dispatch-worker** | `cmd/worker` | Kafka consumer + retry poller — webhook delivery | Up to partition count (12) |
| **receiver** | `scripts/testserver` | Local webhook endpoint for demo/testing — configurable fail rate and latency | — |
| **kafka-exporter** | `danielqsj/kafka-exporter` | Exposes consumer group lag as Prometheus metrics | — |

## Features

- **Microservice decomposition** — Independent failure domains, independent scaling, separate dashboards
- **Kafka-based event queue** — High-throughput ingestion with consumer groups
- **End-to-end trace propagation** — `X-Trace-ID` flows: HTTP → Kafka header → worker context → webhook
- **Reliable delivery** — Atomic PostgreSQL outcome/history writes before Kafka offset commit
- **At-least-once processing** — Persistence failure leaves Kafka messages uncommitted for redelivery
- **Retry with backoff** — Exponential backoff with jitter, configurable max attempts
- **Retry poller** — Polls DB for `status=retrying` events; runs alongside Kafka consumer
- **Crash-recoverable retries** — Expiring owner-fenced PostgreSQL claims reject stale worker outcomes
- **Stable event identity** — One persisted event row per ID; duplicate HTTP delivery remains possible
- **Signature header** — Compatibility placeholder; cryptographic HMAC is not implemented yet
- **Rate limiting** — Redis-backed sliding window, 100 req/s per destination
- **Circuit breaker** — Redis-backed automatic failure isolation per destination
- **Distributed semaphore** — Redis-backed concurrency control across all workers
- **Observability** — Separate Prometheus jobs + Grafana dashboards per service
- **Kubernetes-ready** — HPA, ConfigMap, Secret, separate Deployments per service
- **Graceful shutdown** — Drains in-flight work before stopping

## Quick Start

```bash
make up          # build images + start full stack; prints all service URLs
make seed        # create 3 subscriptions, publish 50 events — watch Grafana fill up
make seed-retry  # 70% fail rate — watch retrying_total climb, then delivered_total follow
make seed-circuit-break  # break the receiver → circuit opens → heal → watch recovery
make logs        # tail dispatch-api + dispatch-worker
make down        # stop everything and wipe volumes
```

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
    "secret": "my-secret-key"
  }'

# List subscriptions
curl http://localhost:8080/subscriptions

# Delete subscription
curl -X DELETE http://localhost:8080/subscriptions/sub_123
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
| `REDIS_URL` | `redis://localhost:6379/0` | Redis connection string |
| `KAFKA_BROKERS` | `localhost:9092` | Kafka broker addresses |
| `KAFKA_TOPIC` | `events.pending` | Kafka topic to consume |
| `KAFKA_CONSUMER_GROUP` | `dispatch-workers` | Consumer group ID |
| `INSTANCE_ID` | `worker-1` | Unique worker instance ID (use pod name in k8s) |
| `METRICS_ADDR` | `:8081` | Metrics HTTP server address |
| `DB_MAX_CONNS` | `30` | Database connection pool size |
| `RETRY_POLL_INTERVAL` | `5s` | Retry poller interval |
| `RETRY_BATCH_SIZE` | `100` | Max events per retry poll |
| `RETRY_LEASE_DURATION` | `30s` | Claim lifetime; keep above expected delivery processing time |

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
```

### Validation Pipeline

The automated validation pipeline is layered:

- `test-unit` — fast unit/component coverage with race detector
- `test-integration` — Testcontainers-backed PostgreSQL and Redis validation
- `test-e2e` — thin smoke coverage for API → Kafka → delivery/retry flow
- `load-test` — non-blocking heavier validation on `main`

`test-integration` and `test-e2e` require Docker because they start real dependencies with Testcontainers.

## Architecture

The diagram below is an operational summary. [Architecture](docs/architecture.md) is the
technical authority; [the system specification](docs/spec.md) defines observable behavior.

```mermaid
flowchart LR
    subgraph dispatch
        API[HTTP API]
        Kafka[(Kafka)]
        Workers[Kafka Workers]
        DB[(PostgreSQL)]
        Redis[(Redis)]
        Client[HTTP Client]
    end

    Producer -->|POST /events| API
    API -->|Publish| Kafka
    Kafka -->|Consumer Group| Workers
    Workers -->|Rate Limit + CB| Redis
    Workers --> Client
    Client -->|POST webhook| Endpoint
    Workers -->|Status updates| DB
```

### Event Lifecycle

```mermaid
stateDiagram-v2
    [*] --> pending: Event received
    pending --> processing: Worker picks up
    processing --> delivered: 2xx response
    processing --> retrying: Error + retries left
    processing --> throttled: Rate limited or circuit open
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
| `X-Signature` | Non-cryptographic placeholder (if secret configured); do not use for authentication |

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
| `dispatch_worker_events_throttled_total` | Counter | Throttled by rate limiter or circuit breaker |
| `dispatch_worker_delivery_duration_seconds` | Histogram | Per-attempt HTTP latency |
| `dispatch_worker_delivery_attempts_total` | Counter | Total HTTP attempts (includes retries) |
| `dispatch_worker_circuit_breaker_state` | Gauge | Per-subscription CB state (0=closed, 1=half-open, 2=open) |
| `dispatch_worker_circuit_breaker_trips_total` | Counter | Times a CB transitioned to open, per subscription |
| `dispatch_worker_rate_limiter_rejections_total` | Counter | Rate limit hits per subscription |

**Kafka exporter:**

| Metric | Type | Description |
|--------|------|-------------|
| `kafka_consumergroup_lag` | Gauge | Per-partition consumer lag for `dispatch-workers` group |

## Resilience

Rate limiting, circuit breaker, and concurrency semaphore use **Redis** for distributed state, enabling horizontal scaling with multiple worker instances.

### Rate Limiting

Per-destination rate limiting using sliding window algorithm (Redis-backed):
- Default: 100 requests/second fixed limit
- Prevents overwhelming webhook endpoints
- Shared state across all worker instances

### Circuit Breaker

Per-destination circuit breaker (Redis-backed):
- Opens when ≥50% of at least 3 requests fail within the measurement window
- Half-open after 30 seconds timeout
- Requires 3 consecutive successes in half-open state to close
- Open circuit does **not** consume event retry attempts

### Distributed Semaphore

Per-destination concurrency control (Redis-backed):
- Default: 100 concurrent requests per subscription
- Coordinates across all worker instances
- Auto-release after 30s TTL (prevents deadlocks on worker crash)
- Falls back to local semaphore if Redis unavailable

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
│   ├── resilience/     # Rate limiter, circuit breaker (Redis)
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
    ├── LIMITATIONS.md  # Known limitations and roadmap
    ├── PERFORMANCE.md  # Benchmark results
    └── adr/            # Architecture Decision Records
```

## Documentation

- [Product Definition](docs/product.md)
- [System Behavior Specification](docs/spec.md)
- [Architecture](docs/architecture.md)
- [Current Limitations and Opportunities](docs/LIMITATIONS.md)
- [V1 Roadmap and Release Gate](docs/v1-roadmap.md)
- [Verified Engineering State](PROGRESS.md)
- [Strategic Next Steps](docs/next-steps.md)

### Architecture Decision Records (ADRs)

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

## License

MIT
