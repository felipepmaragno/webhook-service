# Dispatch

Webhook dispatcher service with reliable delivery, retry with exponential backoff, and observability.

## Features

- **Kafka-based event queue** — High-throughput event ingestion with consumer groups for horizontal scaling
- **Reliable delivery** — PostgreSQL-backed event storage for retry scheduling and delivery history
- **Retry with backoff** — Exponential backoff with jitter, configurable max attempts
- **Idempotency** — Event deduplication via `ON CONFLICT DO NOTHING`
- **HMAC signatures** — Webhook payload signing for verification
- **Rate limiting** — Redis-backed per-destination rate limiting (100 req/s)
- **Circuit breaker** — Redis-backed automatic failure isolation per destination
- **Observability** — Prometheus metrics, structured logging, health checks
- **Horizontal scaling** — Stateless workers with Redis-backed resilience
- **Graceful shutdown** — Drains workers before stopping

## Quick Start

### With Docker Compose (recommended)

```bash
# Start all services (api + worker + kafka + postgres + redis + prometheus + grafana)
docker compose up -d

# Run migrations
docker compose exec api ./dispatch migrate up

# Access services:
# - API: http://localhost:8080
# - Prometheus: http://localhost:9090
# - Grafana: http://localhost:3000 (admin/admin)
```

### Local Development

```bash
# Start infrastructure only
docker compose up -d postgres redis kafka

# Run migrations
export DATABASE_URL="postgres://postgres:postgres@localhost:5432/dispatch?sslmode=disable"
make migrate-up

# Run API server
make run-api

# Run worker (in another terminal)
make run-worker
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

### API Server

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `DATABASE_URL` | `postgres://...` | PostgreSQL connection string |
| `REDIS_URL` | `redis://localhost:6379/0` | Redis connection string |
| `KAFKA_BROKERS` | `localhost:9092` | Kafka broker addresses (comma-separated) |
| `KAFKA_TOPIC` | `events.pending` | Kafka topic for events |
| `ADDR` | `:8080` | HTTP server address |

### Worker

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `DATABASE_URL` | `postgres://...` | PostgreSQL connection string |
| `REDIS_URL` | `redis://localhost:6379/0` | Redis connection string |
| `KAFKA_BROKERS` | `localhost:9092` | Kafka broker addresses |
| `KAFKA_TOPIC` | `events.pending` | Kafka topic to consume |
| `KAFKA_CONSUMER_GROUP` | `dispatch-workers` | Consumer group ID |
| `INSTANCE_ID` | `worker-1` | Unique worker instance ID |
| `DB_MAX_CONNS` | `30` | Database connection pool size |
| `RETRY_POLL_INTERVAL` | `5s` | Retry poller interval |
| `RETRY_BATCH_SIZE` | `100` | Max events per retry poll |

## Development

```bash
# Run tests
make test

# Run tests with race detector
make test-race

# Run tests with coverage
make test-cover

# Lint
make lint
```

## Architecture

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
    processing --> failed: Error + no retries
    retrying --> processing: Retry scheduled
    delivered --> [*]
    failed --> [*]
```

## Delivery Contract

- **Success:** HTTP status `2xx` (200-299)
- **Failure:** HTTP status `4xx`, `5xx`, timeout, connection error
- **Timeout:** 30 seconds (configurable)

### Headers sent to endpoints

| Header | Description |
|--------|-------------|
| `X-Dispatch-Event-ID` | Event ID |
| `X-Dispatch-Event-Type` | Event type |
| `X-Dispatch-Timestamp` | Unix timestamp |
| `X-Dispatch-Signature` | HMAC-SHA256 signature (if secret configured) |

## Metrics

Prometheus metrics available at `/metrics`:

| Metric | Type | Description |
|--------|------|-------------|
| `dispatch_events_received_total` | Counter | Events received via API |
| `dispatch_events_delivered_total` | Counter | Successfully delivered events |
| `dispatch_events_failed_total` | Counter | Permanently failed events |
| `dispatch_events_retrying_total` | Counter | Events scheduled for retry |
| `dispatch_events_throttled_total` | Counter | Events throttled by rate limiting or circuit breaker |
| `dispatch_delivery_duration_seconds` | Histogram | Delivery attempt latency |
| `dispatch_circuit_breaker_state` | Gauge | CB state per subscription (0=closed, 1=half-open, 2=open) |
| `dispatch_circuit_breaker_trips_total` | Counter | Times CB tripped to open |
| `dispatch_rate_limiter_rejections_total` | Counter | Requests rejected by rate limiter |

## Resilience

Rate limiting, circuit breaker, and concurrency semaphore use **Redis** for distributed state, enabling horizontal scaling with multiple worker instances.

### Rate Limiting

Per-destination rate limiting using sliding window algorithm (Redis-backed):
- Default: 100 requests/second fixed limit
- Prevents overwhelming webhook endpoints
- Shared state across all worker instances

### Circuit Breaker

Per-destination circuit breaker (Redis-backed):
- Opens after 5 consecutive failures
- Half-open after 30 seconds timeout
- Allows 3 requests in half-open state to test recovery
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
│   └── producer/       # Test event producer
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
├── scripts/            # Benchmark and load test scripts
└── docs/
    ├── spec.md         # Technical specification
    ├── architecture.md # Architecture diagrams
    ├── LIMITATIONS.md  # Known limitations and roadmap
    ├── PERFORMANCE.md  # Benchmark results
    └── adr/            # Architecture Decision Records
```

## Documentation

- [Technical Specification](docs/spec.md)
- [Architecture](docs/architecture.md)
- [Limitations & Future Opportunities](docs/LIMITATIONS.md)

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

## License

MIT
