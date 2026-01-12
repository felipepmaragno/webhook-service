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

### Tracing: Deferred
Not implemented in v1.0. Consider OpenTelemetry for future.

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

| Metric | Type | Purpose |
|--------|------|---------|
| `events_received_total` | Counter | Inbound rate |
| `events_delivered_total` | Counter | Success rate |
| `events_failed_total` | Counter | Failure rate (alert) |
| `events_retrying_total` | Counter | Retry rate |
| `delivery_duration_seconds` | Histogram | Latency distribution |
| `circuit_breaker_state` | Gauge | Destination health |
| `circuit_breaker_trips_total` | Counter | CB activations |
| `rate_limiter_rejections_total` | Counter | Rate limit hits |

### 3. slog Structured Logging

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

### 4. Health Endpoints

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
- OpenTelemetry tracing
- Distributed tracing across services
- Log aggregation (Loki, Elasticsearch)
- Custom Grafana alerts
