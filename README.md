# Dispatch

Webhook dispatcher service with reliable delivery, retry with exponential backoff, and observability.

## Features

- **Reliable delivery** — PostgreSQL-backed event storage with `FOR UPDATE SKIP LOCKED` for concurrent processing
- **Retry with backoff** — Exponential backoff with jitter, configurable max attempts
- **Idempotency** — Event deduplication via `ON CONFLICT DO NOTHING`
- **HMAC signatures** — Webhook payload signing for verification
- **Rate limiting** — Per-destination rate limiting with token bucket algorithm
- **Circuit breaker** — Automatic failure isolation per destination
- **Observability** — Prometheus metrics, structured logging, health checks
- **Graceful shutdown** — Drains workers before stopping

## Quick Start

### With Docker Compose (recommended)

```bash
# Start all services (dispatch + postgres + prometheus + grafana)
docker compose up -d

# Run migrations
docker compose exec dispatch ./dispatch migrate up

# Access services:
# - API: http://localhost:8080
# - Prometheus: http://localhost:9090
# - Grafana: http://localhost:3000 (admin/admin)
```

### Local Development

```bash
# Start PostgreSQL only
docker compose up -d postgres

# Run migrations
export DATABASE_URL="postgres://postgres:postgres@localhost:5432/dispatch?sslmode=disable"
make migrate-up

# Run the service
make run
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

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `DATABASE_URL` | `postgres://postgres:postgres@localhost:5432/dispatch?sslmode=disable` | PostgreSQL connection string |
| `ADDR` | `:8080` | HTTP server address |

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
        DB[(PostgreSQL)]
        Workers[Worker Pool]
        Client[HTTP Client]
    end

    Producer -->|POST /events| API
    API -->|INSERT| DB
    DB -->|SELECT FOR UPDATE SKIP LOCKED| Workers
    Workers --> Client
    Client -->|POST webhook| Endpoint
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
| `dispatch_delivery_duration_seconds` | Histogram | Delivery attempt latency |
| `dispatch_circuit_breaker_state` | Gauge | CB state per subscription (0=closed, 1=half-open, 2=open) |
| `dispatch_circuit_breaker_trips_total` | Counter | Times CB tripped to open |
| `dispatch_rate_limiter_rejections_total` | Counter | Requests rejected by rate limiter |

## Resilience

### Rate Limiting

Per-destination rate limiting using token bucket algorithm:
- Default: 100 requests/second with burst of 10
- Prevents overwhelming webhook endpoints

### Circuit Breaker

Per-destination circuit breaker using [sony/gobreaker](https://github.com/sony/gobreaker):
- Opens after 50% failure rate (minimum 3 requests)
- Half-open after 30 seconds timeout
- Allows 5 requests in half-open state to test recovery

## Project Structure

```
dispatch/
├── cmd/
│   ├── dispatch/       # Main application
│   └── migrate/        # Database migrations CLI
├── internal/
│   ├── api/            # HTTP handlers and routes
│   ├── domain/         # Core business entities
│   ├── observability/  # Metrics, logging, health checks
│   ├── repository/     # Data access layer
│   ├── resilience/     # Rate limiter, circuit breaker
│   ├── retry/          # Exponential backoff policy
│   └── worker/         # Webhook delivery engine
├── migrations/         # SQL migrations
├── deploy/             # Prometheus/Grafana configs
└── docs/
    ├── spec.md         # Technical specification
    ├── architecture.md # Architecture diagrams
    └── adr/            # Architecture Decision Records
```

## Documentation

- [Technical Specification](docs/spec.md)
- [Architecture](docs/architecture.md)
- [ADR 001: Library Choices](docs/adr/001-library-choices.md)

## License

MIT
