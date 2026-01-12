# Dispatch

Webhook dispatcher service with reliable delivery, retry with exponential backoff, and observability.

## Features

- **Reliable delivery** — PostgreSQL-backed event storage with `FOR UPDATE SKIP LOCKED` for concurrent processing
- **Retry with backoff** — Exponential backoff with jitter, configurable max attempts
- **Idempotency** — Event deduplication via `ON CONFLICT DO NOTHING`
- **HMAC signatures** — Webhook payload signing for verification
- **Graceful shutdown** — Drains workers before stopping

## Quick Start

```bash
# Start PostgreSQL
docker-compose up -d postgres

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

## License

MIT
