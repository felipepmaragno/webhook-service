# ADR 014: Microservices Decomposition — API vs Worker

## Status
Accepted (Implemented)

## Context

The dispatch system originally ran as a single binary that handled both HTTP requests and Kafka consumption. As the system evolved, two distinct workloads emerged with fundamentally different scaling requirements:

| Concern | HTTP API | Kafka Worker |
|---------|----------|--------------|
| **Trigger** | External HTTP requests | Kafka messages |
| **Scaling constraint** | CPU/memory (stateless) | Kafka partition count |
| **State** | Stateless (PostgreSQL + Kafka) | Stateful (consumer group membership) |
| **Failure domain** | API outage ≠ delivery outage | Worker crash ≠ API outage |
| **Deployment frequency** | High (API changes often) | Low (delivery logic is stable) |

Running both in the same process meant:
- Scaling for API load also scaled workers (wasting Kafka partitions)
- A worker crash took down the API
- Metrics were mixed — impossible to tell if latency was from HTTP or delivery

## Decision

Split into two independent services within the same repository (monorepo):

### `dispatch-api` (`cmd/dispatch`)
- Handles all HTTP traffic: event ingestion, subscription management, event status queries
- Publishes events to Kafka
- Reads/writes PostgreSQL for subscriptions and event status
- **Scales freely** — stateless, no partition constraint
- Exposes metrics on `:8080/metrics`

### `dispatch-worker` (`cmd/worker`)
- Consumes events from Kafka (`events.pending` topic)
- Runs retry poller alongside Kafka consumer
- Delivers webhooks with rate limiting, circuit breaker, and semaphore
- **Scales up to partition count** — `maxReplicas: 12` in HPA
- Exposes metrics on `:8081/metrics` (separate port, separate Prometheus job)

## Architecture

```
                    ┌──────────────────────────────────────────┐
                    │            Client / Producer              │
                    └──────────────────┬───────────────────────┘
                                       │ HTTP
                                       ▼
                    ┌──────────────────────────────────────────┐
                    │           dispatch-api                    │
                    │         (cmd/dispatch)                    │
                    │                                          │
                    │  POST /events          → Kafka publish   │
                    │  GET  /events/:id      → PostgreSQL read │
                    │  POST /subscriptions   → PostgreSQL write│
                    │  GET  /subscriptions   → PostgreSQL read │
                    │                                          │
                    │  HPA: 2–20 replicas (CPU-based)          │
                    │  Metrics: :8080/metrics                  │
                    └──────────────────┬───────────────────────┘
                                       │ Kafka publish
                                       │ (X-Trace-ID in header)
                                       ▼
                    ┌──────────────────────────────────────────┐
                    │              Kafka                        │
                    │         events.pending (12 partitions)   │
                    └──────────────────┬───────────────────────┘
                                       │ Consumer group
                                       ▼
                    ┌──────────────────────────────────────────┐
                    │           dispatch-worker                 │
                    │           (cmd/worker)                    │
                    │                                          │
                    │  Kafka Consumer  ←──── events.pending    │
                    │  Retry Poller    ←──── PostgreSQL        │
                    │                                          │
                    │  Rate Limiter  (Redis)                   │
                    │  Circuit Breaker (Redis)                 │
                    │  Semaphore       (Redis)                 │
                    │                                          │
                    │  HPA: 2–12 replicas (≤ partitions)       │
                    │  Metrics: :8081/metrics                  │
                    └──────────────────┬───────────────────────┘
                                       │ HTTP POST + X-Trace-ID
                                       ▼
                              Webhook Destination
```

## Cross-Service Observability

### Trace Propagation

Every request to `dispatch-api` generates or accepts an `X-Trace-ID` header. This ID is:
1. Logged in `dispatch-api` with every request log line
2. Injected as a Kafka message header when publishing to `events.pending`
3. Extracted by `dispatch-worker` consumer and injected into the processing context
4. Logged in `dispatch-worker` with every delivery log line
5. Forwarded as `X-Trace-ID` header to the webhook destination

This enables full end-to-end correlation:
```
dispatch-api log: trace_id=abc123 event_id=evt_001 path=/events status=202
dispatch-worker log: trace_id=abc123 event_id=evt_001 subscription_id=sub_xyz status=delivered
```

### Separate Prometheus Jobs

```yaml
# prometheus.yml
- job_name: 'dispatch-api'
  targets: ['dispatch-api:8080']

- job_name: 'dispatch-worker'
  targets: ['dispatch-worker:8081']
```

Each service has its own Grafana dashboard:
- **dispatch-api**: HTTP throughput, latency p50/p95/p99, error rate, events published
- **dispatch-worker**: delivery rate, webhook latency, circuit breaker state, rate limiter rejections, retry rate

### Metric Namespaces

| Service | Namespace | Example |
|---------|-----------|---------|
| dispatch-api | `dispatch_` | `dispatch_http_requests_total` |
| dispatch-worker | `dispatch_worker_` | `dispatch_worker_events_delivered_total` |

## Kubernetes Scaling Constraints

### dispatch-api
```yaml
# No partition constraint — scales freely
minReplicas: 2
maxReplicas: 20
```

### dispatch-worker
```yaml
# Bounded by Kafka partition count
# Workers > partitions are idle (receive no messages)
minReplicas: 2
maxReplicas: 12  # = events.pending partition count
```

## Consequences

### Positive
- **Independent failure domains** — API outage does not stop delivery; worker crash does not affect ingestion
- **Independent scaling** — API scales for HTTP load; worker scales for delivery throughput
- **Clear observability** — separate dashboards, separate metric namespaces, trace_id correlation
- **Explicit constraints** — HPA maxReplicas documents the partition limit as code
- **Simpler reasoning** — each service has one job

### Negative
- **Two deployments** — two Docker images, two Kubernetes Deployments to manage
- **Shared database** — both services connect to the same PostgreSQL instance (acceptable at this scale)
- **Trace ID is best-effort** — not a full distributed tracing solution (no spans, no sampling)

## Alternatives Considered

### Single Binary
**Rejected:** Mixed scaling constraints, mixed failure domains, mixed metrics.

### gRPC between services
**Rejected:** Kafka already decouples them. Adding synchronous gRPC would create tight coupling.

### Full OpenTelemetry
**Rejected:** Adds significant complexity (collector, sampling, span propagation). `X-Trace-ID` provides 80% of the value at 5% of the cost.

## References

- [ADR 012: Kafka Event Queue](./012-kafka-event-queue.md)
- [ADR 013: Retry Poller and Distributed Semaphore](./013-retry-poller-distributed-semaphore.md)
- [W3C Trace Context](https://www.w3.org/TR/trace-context/)
