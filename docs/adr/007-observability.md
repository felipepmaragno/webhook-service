# ADR 007: Observability

## Status
Accepted

## Context
Production systems need observability for:
- Monitoring health and performance
- Debugging issues
- Capacity planning
- Alerting on problems

Three pillars of observability:
1. **Metrics**: Numeric measurements over time
2. **Logs**: Discrete events with context
3. **Traces**: Request flow across services

## Decision

### Metrics: Prometheus
Use `github.com/prometheus/client_golang` for metrics.

### Logging: slog (stdlib)
Use `log/slog` for structured logging.

### Tracing: Best-effort Trace ID propagation
`X-Trace-ID` is propagated end-to-end: HTTP request → Kafka message header → worker context → webhook delivery header. This is not full distributed tracing (no spans, no sampling), but enables log correlation across services. Consider OpenTelemetry for full tracing in future.

## Alternatives Considered

### Metrics

#### StatsD/Graphite
**Cons:**
- Push model (requires agent)
- Less ecosystem support
- Older technology

#### OpenTelemetry Metrics
**Pros:**
- Unified observability
- Vendor-neutral

**Cons:**
- More complex setup
- Overkill for single service

### Logging

#### uber-go/zap
**Pros:**
- Very fast
- Feature-rich

**Cons:**
- External dependency
- slog is now standard

#### rs/zerolog
**Pros:**
- Zero allocation
- Fast

**Cons:**
- External dependency
- Similar to zap

## Rationale

### 1. Prometheus Metrics

Industry standard for Go services:
```go
metrics := &Metrics{
    EventsReceived: promauto.NewCounter(prometheus.CounterOpts{
        Namespace: "dispatch",
        Name:      "events_received_total",
        Help:      "Total events received via API",
    }),
    // ...
}
```

Benefits:
- Pull model (Prometheus scrapes `/metrics`)
- Rich query language (PromQL)
- Grafana integration
- Alertmanager for alerts

### 2. Key Metrics

All worker metrics are prefixed `dispatch_worker_`. API metrics are prefixed `dispatch_`.

| Metric (unprefixed) | Type | Namespace | Labels | Purpose |
|---|---|---|---|---|
| `events_received_total` | Counter | `dispatch` | — | Inbound rate at API |
| `events_delivered_total` | Counter | `dispatch_worker` | — | Successful deliveries |
| `events_failed_total` | Counter | `dispatch_worker` | — | Permanent failures (alert) |
| `events_retrying_total` | Counter | `dispatch_worker` | — | Events scheduled for retry |
| `events_throttled_total` | Counter | `dispatch_worker` | — | Events throttled by CB or rate limiter |
| `delivery_duration_seconds` | Histogram | `dispatch_worker` | — | Per-attempt HTTP latency |
| `delivery_attempts_total` | Counter | `dispatch_worker` | — | Total HTTP attempts (incl. retries) |
| `circuit_breaker_state` | Gauge | `dispatch_worker` | `subscription_id` | 0=closed, 1=half-open, 2=open |
| `circuit_breaker_trips_total` | Counter | `dispatch_worker` | `subscription_id` | Transitions to open state |
| `rate_limiter_rejections_total` | Counter | `dispatch_worker` | `subscription_id` | Rate limit rejections |
| `retry_events_claimed_total` | Counter | `dispatch_worker` | — | Retry events claimed |
| `retry_events_reclaimed_total` | Counter | `dispatch_worker` | — | Expired leases recovered |
| `retry_empty_polls_total` | Counter | `dispatch_worker` | — | Drain cycles that found no work |
| `retry_claim_failures_total` | Counter | `dispatch_worker` | — | Database claim failures |
| `retry_persistence_failures_total` | Counter | `dispatch_worker` | — | Retry outcome persistence failures |
| `retry_stale_owner_rejections_total` | Counter | `dispatch_worker` | — | Fenced stale outcome writes |
| `retry_active_batches` | Gauge | `dispatch_worker` | — | Current processing batches on this worker |
| `retry_due_events` | Gauge | `dispatch_worker` | — | Due retry/throttled rows |
| `retry_expired_claims` | Gauge | `dispatch_worker` | — | Expired processing leases |
| `retry_leased_events` | Gauge | `dispatch_worker` | — | Active processing leases |
| `retry_oldest_due_age_seconds` | Gauge | `dispatch_worker` | — | Oldest due/expired work age |
| `retry_scheduling_lag_seconds` | Histogram | `dispatch_worker` | — | Eligibility-to-claim delay |
| `http_requests_total` | Counter | `dispatch` | `method, path, status` | API HTTP throughput |
| `http_request_duration_seconds` | Histogram | `dispatch` | `method, path` | API HTTP latency |

### 3. Circuit Breaker Observability — StateChangeNotifier

Circuit breaker state transitions are exposed to metrics via a `StateChangeNotifier` interface (`internal/resilience/interfaces.go`). Both `RedisCircuitBreaker` and `SimpleCircuitBreaker` implement it. The `DeliveryHandler` wires state-change callbacks via `WithCircuitBreakerMetrics` option, which type-asserts the circuit breaker to `StateChangeNotifier` at construction time — keeping the core `CircuitBreaker` interface clean.

```go
// StateChangeNotifier is optional — type-asserted, not required by CircuitBreaker.
type StateChangeNotifier interface {
    OnStateChange(fn func(subscriptionID string, from, to CircuitState))
}
```

`RedisCircuitBreaker` snapshots state before and after each Lua script execution in `RecordSuccess`/`RecordFailure`, then fires the callback only when a transition is detected. The callback is never called in the hot path when no metrics are configured.

### 4. slog Structured Logging

Go 1.21+ standard library:
```go
logger.Info("event delivered",
    "event_id", event.ID,
    "subscription_id", sub.ID,
    "duration_ms", duration.Milliseconds(),
    "status_code", resp.StatusCode,
)
```

Output (JSON handler):
```json
{
  "time": "2024-01-15T10:30:00Z",
  "level": "INFO",
  "msg": "event delivered",
  "event_id": "evt_123",
  "subscription_id": "sub_456",
  "duration_ms": 45,
  "status_code": 200
}
```

Benefits:
- No external dependency
- Structured by default
- Context propagation
- Multiple handlers (JSON, text)

### 5. Health Endpoints

```
GET /health  → Always 200 (liveness)
GET /ready   → 200 if DB connected (readiness)
GET /metrics → Prometheus metrics
```

Kubernetes integration:
```yaml
livenessProbe:
  httpGet:
    path: /health
    port: 8080
readinessProbe:
  httpGet:
    path: /ready
    port: 8080
```

## Grafana Dashboard

Pre-configured dashboard includes:
- Event rate (received/delivered/failed)
- Delivery latency percentiles (p50, p95, p99)
- Circuit breaker state per subscription
- Rate limiter rejections
- Retry backlog, leases, active batches, scheduling age, and scheduler failures

Retry scheduler metrics intentionally have no event or subscription labels. They describe
worker and global backlog health without creating unbounded cardinality. Prometheus rules
alert on sustained oldest-due age, expired claims, repeated claim failures, and outcome
persistence failures.

## Consequences

### Positive
- Industry-standard tooling
- Easy Kubernetes integration
- Rich querying and alerting
- No external logging dependency

### Negative
- Prometheus requires infrastructure
- slog is relatively new

### Future Considerations
- OpenTelemetry tracing (full spans + sampling)
- Log aggregation (Loki, Elasticsearch)
- Custom Grafana alerts
