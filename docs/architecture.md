# Architecture — Dispatch

Detailed technical documentation of the Webhook Dispatcher architecture.

## Overview

```mermaid
flowchart TB
    subgraph External["External Systems"]
        Producer["Producer Service"]
        Consumer["Consumer Endpoint"]
    end

    subgraph Dispatch["dispatch"]
        direction TB
        API["HTTP API<br/>(chi router)"]
        
        subgraph Storage["Persistence"]
            DB[(PostgreSQL)]
        end
        
        subgraph Processing["Processing"]
            Workers["Worker Pool<br/>(N goroutines)"]
            CB["Circuit Breaker<br/>(sony/gobreaker)"]
            RL["Rate Limiter<br/>(x/time/rate)"]
        end
        
        Delivery["HTTP Client<br/>(net/http)"]
    end

    subgraph Observability["Observability"]
        Metrics["Prometheus"]
        Logs["slog (JSON)"]
    end

    Producer -->|"POST /events"| API
    API -->|"INSERT<br/>ON CONFLICT DO NOTHING"| DB
    DB -->|"SELECT FOR UPDATE<br/>SKIP LOCKED"| Workers
    Workers --> CB
    CB --> RL
    RL --> Delivery
    Delivery -->|"POST + HMAC"| Consumer
    
    API -.->|metrics| Metrics
    Workers -.->|metrics| Metrics
    Delivery -.->|structured logs| Logs
```

## Components

### HTTP API

Responsible for receiving events and managing subscriptions.

```mermaid
flowchart LR
    subgraph API["HTTP API"]
        Router["chi.Router"]
        Middleware["Middleware<br/>(RequestID, Recovery)"]
        Handlers["Handlers"]
    end

    Request --> Router
    Router --> Middleware
    Middleware --> Handlers
    Handlers --> Response
```

**Endpoints:**

| Method | Path | Handler |
|--------|------|---------|
| POST | /events | CreateEvent |
| GET | /events/{id} | GetEvent |
| GET | /events/{id}/attempts | GetEventAttempts |
| POST | /subscriptions | CreateSubscription |
| GET | /subscriptions | GetSubscriptions |
| DELETE | /subscriptions/{id} | DeleteSubscription |
| GET | /health | Health |

### PostgreSQL Storage

Stores events, delivery attempts, and subscriptions.

```mermaid
erDiagram
    events ||--o{ delivery_attempts : has
    subscriptions ||--o{ events : receives

    events {
        text id PK
        text type
        text source
        jsonb data
        event_status status
        int attempts
        int max_attempts
        timestamptz next_attempt_at
        text last_error
        timestamptz created_at
        timestamptz updated_at
        timestamptz delivered_at
    }

    delivery_attempts {
        serial id PK
        text event_id FK
        int attempt_number
        int status_code
        text response_body
        text error
        int duration_ms
        timestamptz created_at
    }

    subscriptions {
        text id PK
        text url
        text[] event_types
        text secret
        int rate_limit
        timestamptz created_at
        boolean active
    }
```

### Worker Pool

Pool of goroutines that poll the database and process events.

```mermaid
sequenceDiagram
    participant Pool as Worker Pool
    participant DB as PostgreSQL
    participant CB as Circuit Breaker
    participant RL as Rate Limiter
    participant HTTP as HTTP Client
    participant Endpoint

    loop Every 100ms
        Pool->>DB: SELECT FOR UPDATE SKIP LOCKED
        DB-->>Pool: Events (status → processing)
        
        loop For each event
            Pool->>DB: Get matching subscriptions
            
            loop For each subscription
                Pool->>CB: Allow request?
                
                alt Circuit CLOSED
                    CB-->>Pool: Yes
                    Pool->>RL: Wait for rate limit
                    RL-->>Pool: OK
                    Pool->>HTTP: Build request + HMAC
                    HTTP->>Endpoint: POST webhook
                    
                    alt 2xx Response
                        Endpoint-->>HTTP: Success
                        HTTP-->>Pool: OK
                        Pool->>DB: status = delivered
                    else Error
                        Endpoint-->>HTTP: Error
                        HTTP-->>Pool: Fail
                        Pool->>CB: Record failure
                        Pool->>DB: status = retrying, schedule retry
                    end
                    
                else Circuit OPEN
                    CB-->>Pool: No (fail fast)
                    Pool->>DB: status = retrying (no attempt++)
                end
            end
        end
    end
```

### Retry Policy

Exponential backoff strategy with jitter.

```mermaid
flowchart TD
    Start["Delivery Failed"] --> CanRetry{attempts < max?}
    
    CanRetry -->|Yes| Calculate["Calculate delay:<br/>delay = initial × 2^attempt"]
    Calculate --> Cap["Cap at max_interval"]
    Cap --> Jitter["Add jitter: ±10%"]
    Jitter --> Schedule["Schedule: next_attempt_at = now + delay"]
    Schedule --> Status["status = retrying"]
    
    CanRetry -->|No| Failed["status = failed<br/>(dead letter)"]
```

**Default configuration:**

| Parameter | Value |
|-----------|-------|
| InitialInterval | 1s |
| MaxInterval | 1h |
| Multiplier | 2.0 |
| Jitter | 10% |
| MaxAttempts | 5 |

**Example delays:**

| Attempt | Base Delay | With Jitter (±10%) |
|---------|------------|-------------------|
| 1 | 1s | 0.9s - 1.1s |
| 2 | 2s | 1.8s - 2.2s |
| 3 | 4s | 3.6s - 4.4s |
| 4 | 8s | 7.2s - 8.8s |
| 5 | 16s | 14.4s - 17.6s |

### Circuit Breaker

Protects problematic endpoints using the circuit breaker pattern.

```mermaid
stateDiagram-v2
    [*] --> Closed: Initial state
    
    Closed --> Open: 5 consecutive failures
    Open --> HalfOpen: After 30s timeout
    HalfOpen --> Closed: Success
    HalfOpen --> Open: Failure
    
    note right of Closed
        Normal operation
        Counting consecutive failures
        All requests allowed
    end note
    
    note right of Open
        Fail fast mode
        No requests sent
        Waiting for timeout
    end note
    
    note right of HalfOpen
        Testing recovery
        Limited requests (3)
        Deciding next state
    end note
```

**Behavior by state:**

| State | Requests | Failures | Timeout |
|-------|----------|----------|---------|
| Closed | All allowed | Counting | - |
| Open | Rejected (fail fast) | - | 30s |
| HalfOpen | 3 allowed | Any → Open | - |

**Important decision:** When the circuit is open, the event **does not consume an attempt**. This is fair because the problem is with the destination, not the event.

## Data Flow

### Event Creation

```mermaid
flowchart TD
    A["POST /events"] --> B["Validate request"]
    B --> C["Create Event struct"]
    C --> D["INSERT INTO events<br/>ON CONFLICT DO NOTHING"]
    D --> E["Return 202 Accepted"]
    
    style D fill:#326ce5,color:#fff
```

### Webhook Delivery

```mermaid
flowchart TD
    A["Worker polls DB"] --> B["Get pending events<br/>FOR UPDATE SKIP LOCKED"]
    B --> C["Get matching subscriptions"]
    C --> D{"Has subscriptions?"}
    
    D -->|No| E["Mark as delivered"]
    D -->|Yes| F["For each subscription"]
    
    F --> G{"Circuit breaker?"}
    G -->|Open| H["Reschedule<br/>(no attempt++)"]
    G -->|Closed| I["Check rate limit"]
    
    I --> J["Build request + HMAC"]
    J --> K["POST to endpoint"]
    
    K --> L{"Response?"}
    L -->|2xx| M["Mark as delivered"]
    L -->|Error| N{"Can retry?"}
    
    N -->|Yes| O["Schedule retry"]
    N -->|No| P["Mark as failed"]
    
    style B fill:#326ce5,color:#fff
    style K fill:#2e7d32,color:#fff
```

## Concurrency

### Safe Polling

Multiple workers can run in parallel without processing the same event:

```sql
UPDATE events
SET status = 'processing', updated_at = NOW()
WHERE id IN (
    SELECT id FROM events
    WHERE status IN ('pending', 'retrying')
    AND (next_attempt_at IS NULL OR next_attempt_at <= NOW())
    ORDER BY next_attempt_at NULLS FIRST, created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 10
)
RETURNING *
```

**`FOR UPDATE SKIP LOCKED`** ensures that:
- Events already being processed are skipped
- No deadlocks between workers
- Scales horizontally (multiple instances)

### Graceful Shutdown

```mermaid
sequenceDiagram
    participant Signal as OS Signal
    participant Main as main()
    participant Server as HTTP Server
    participant Pool as Worker Pool
    participant Workers as Workers

    Signal->>Main: SIGINT/SIGTERM
    Main->>Pool: Stop()
    Pool->>Workers: Cancel context
    Workers-->>Pool: Finish current work
    Pool-->>Main: All workers done
    Main->>Server: Shutdown(ctx)
    Server-->>Main: Connections drained
    Main->>Main: Exit 0
```

## Future Evolution

### v0.2.0 — Observability

```mermaid
flowchart LR
    subgraph Metrics["Prometheus Metrics"]
        events_received["events_received_total"]
        events_delivered["events_delivered_total"]
        events_failed["events_failed_total"]
        delivery_duration["delivery_duration_seconds"]
        circuit_state["circuit_breaker_state"]
    end
    
    subgraph Logs["Structured Logs"]
        event_created["event.created"]
        delivery_success["delivery.success"]
        delivery_failure["delivery.failure"]
        circuit_change["circuit.state_change"]
    end
```

### v0.3.0 — Resilience

```mermaid
flowchart TB
    subgraph RateLimiting["Rate Limiting"]
        TokenBucket["Token Bucket<br/>per subscription"]
    end
    
    subgraph CircuitBreaker["Circuit Breaker"]
        GoBreaker["sony/gobreaker<br/>per subscription"]
    end
    
    Worker --> RateLimiting
    RateLimiting --> CircuitBreaker
    CircuitBreaker --> Delivery
```
