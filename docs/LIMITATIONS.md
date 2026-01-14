# Limitations and Future Opportunities

This document outlines the current limitations of the dispatch webhook service and potential evolution paths. Understanding these constraints helps in making informed decisions about when to use this service and when to consider alternatives.

## Current Limitations

### 1. ~~Single-Instance State~~ (Resolved in v0.4.0)

**Status:** ✅ Resolved

Rate limiting and circuit breaker now use Redis for distributed state, enabling horizontal scaling. Graceful fallback to in-memory when Redis is unavailable.

---

### 2. No Multi-Tenancy

**Limitation:** Single-tenant design, no isolation between different API consumers.

**Impact:**
- Cannot serve multiple independent customers
- No per-tenant quotas or billing
- Shared resource pool

**Workaround:**
- Deploy separate instances per tenant
- Use external API gateway for tenant routing

**Evolution Path:**
- Add `tenant_id` to events and subscriptions
- Per-tenant rate limits and quotas
- Tenant-aware metrics
- Estimated effort: 1-2 weeks

---

### 3. Polling-Based Processing

**Limitation:** Workers poll database every 100ms instead of push-based notification.

**Impact:**
- 50ms average latency (acceptable for webhooks)
- Continuous database queries even when idle
- Not suitable for sub-millisecond requirements

**Workaround:**
- Reduce poll interval for lower latency (increases DB load)
- Accept 50-100ms latency (usually fine for webhooks)

**Evolution Path:**
- PostgreSQL LISTEN/NOTIFY for instant notification
- Hybrid approach: NOTIFY triggers immediate poll
- Estimated effort: 2-3 days

---

### 4. No Payload Transformation

**Limitation:** Events are delivered as-is, no transformation or filtering.

**Impact:**
- Cannot adapt payload format per destination
- No field mapping or enrichment
- No conditional delivery based on payload content

**Workaround:**
- Transform at producer side
- Use intermediate service for transformation

**Evolution Path:**
- JSONPath-based field selection
- Template-based transformation (Go templates)
- Lua/JavaScript scripting for complex transforms
- Estimated effort: 1-2 weeks

---

### 5. No Dead Letter Queue

**Limitation:** Failed events (after max retries) are marked as `failed` but not moved to a separate queue.

**Impact:**
- No automatic reprocessing mechanism
- Manual intervention required for failed events
- Failed events stay in main table

**Workaround:**
- Query failed events: `SELECT * FROM events WHERE status = 'failed'`
- Manual retry via API or direct DB update

**Evolution Path:**
- Separate `dead_letter_events` table
- API endpoint to replay failed events
- Automatic archival after N days
- Estimated effort: 2-3 days

---

### 6. No Event Ordering Guarantees

**Limitation:** Events may be delivered out of order, especially under retry scenarios.

**Impact:**
- Event B may arrive before Event A if A fails and retries
- No FIFO guarantee per subscription

**Workaround:**
- Include sequence number in payload for consumer ordering
- Design consumers to handle out-of-order delivery

**Evolution Path:**
- Per-subscription ordered delivery (single worker per subscription)
- Sequence numbers with consumer-side reordering
- Estimated effort: 1 week

---

### 7. No Webhook Verification/Handshake

**Limitation:** No verification that destination endpoint is valid before delivery.

**Impact:**
- Events may be sent to non-existent endpoints
- No subscription confirmation flow

**Workaround:**
- Validate URLs at subscription creation (HTTP HEAD)
- Monitor failed deliveries

**Evolution Path:**
- Subscription verification endpoint (destination must respond with challenge)
- Periodic health checks for subscriptions
- Auto-disable failing subscriptions
- Estimated effort: 3-5 days

---

### 8. Limited Query Capabilities

**Limitation:** No advanced querying or filtering of events.

**Impact:**
- Cannot search events by payload content
- Limited filtering options in API

**Workaround:**
- Direct database queries for complex searches
- Export to analytics system

**Evolution Path:**
- Full-text search on event data (PostgreSQL tsvector)
- Elasticsearch integration for complex queries
- GraphQL API for flexible querying
- Estimated effort: 1-2 weeks

---

### 9. No Batch Delivery

**Limitation:** Each event is delivered individually, no batching.

**Impact:**
- High overhead for high-volume destinations
- More HTTP connections

**Workaround:**
- Batch at producer side
- Accept individual delivery overhead

**Evolution Path:**
- Configurable batch size per subscription
- Time-based or count-based batching
- Estimated effort: 1 week

---

### 10. PostgreSQL as Single Point of Failure

**Limitation:** All data in single PostgreSQL instance.

**Impact:**
- Database failure = service failure
- Limited by single-node PostgreSQL throughput

**Workaround:**
- PostgreSQL replication for HA
- Regular backups

**Evolution Path:**
- Read replicas for query scaling
- Citus for horizontal sharding
- Multi-region deployment
- Estimated effort: Varies (days to weeks)

---

## Scalability Boundaries

### Measured Performance (Benchmark Results)

Benchmarks run on Intel i5-1335U (12 cores), PostgreSQL 16 in Docker container:

| Benchmark | Throughput | Latency | Notes |
|-----------|------------|---------|-------|
| Sequential ingestion | **3,970 events/s** | 252µs/op | Single goroutine |
| Parallel ingestion | **9,480 events/s** | 105µs/op | 12 goroutines |
| Sustained load (10s) | **11,643 events/s** | 0.86ms | 10 workers |

Run benchmarks yourself:
```bash
# Go benchmarks
go test -bench=. ./internal/benchmark/...

# Throughput report
go test -v -run TestThroughputReport ./internal/benchmark/...

# HTTP load test (requires hey: go install github.com/rakyll/hey@latest)
./scripts/loadtest.sh
```

### Capacity Estimates

| Metric | Measured/Estimated | Bottleneck |
|--------|-------------------|------------|
| Events/second (ingest) | **~10,000** (measured) | PostgreSQL INSERT |
| Events/second (delivery) | ~1,000-2,000 | HTTP client, worker count |
| Concurrent subscriptions | ~10,000 | Memory (rate limiters) |
| Event retention | ~10M events | PostgreSQL storage |

### Scaling Strategies

**Vertical Scaling (up to ~15k events/s):**
- More workers (increase `Workers` config)
- Larger PostgreSQL instance
- More CPU/memory for dispatch

**Horizontal Scaling (up to ~50k events/s):**
- Multiple dispatch instances (stateless with Redis-backed resilience)
- PostgreSQL read replicas
- Connection pooling (PgBouncer)

**Database Optimizations (up to ~100k events/s):**

1. **Batch Writes** — Most impactful, 10-50x throughput gain
   ```sql
   -- Instead of 1 INSERT per event:
   INSERT INTO delivery_attempts (event_id, status, ...)
   VALUES (...), (...), (...), ...  -- 100-1000 rows per batch
   ```

2. **Table Partitioning** — Parallel writes, fast deletes
   ```sql
   CREATE TABLE delivery_attempts (...) PARTITION BY RANGE (created_at);
   -- Drop old partitions instead of DELETE
   ```

3. **Async Commit** — Trade durability for speed
   ```sql
   SET synchronous_commit = off;  -- 2-3x write throughput
   ```

4. **Selective Persistence** — Not everything needs to be stored
   - Success: Increment Prometheus counter, skip database write
   - Failure: Persist for retry
   - History: Write only if explicitly requested

**Architectural Changes (100k+ events/s):**

| Component | Current | Scaled |
|-----------|---------|--------|
| Ingestion | HTTP API → PostgreSQL | Kafka topics |
| Storage | PostgreSQL | TimescaleDB / ClickHouse |
| Hot data | Same table | Last 24h in PostgreSQL |
| Cold data | Same table | Historical in columnar DB |

**When to use Kafka:**
- Multi-datacenter replication required
- Event replay is a hard requirement
- Burst absorption (10x spikes)
- Multiple consumers for same events

**Reality check:** PostgreSQL with batch writes + partitioning handles most real-world scenarios. Kafka + specialized databases are for big tech scale or specific architectural requirements (replay, multi-DC).

---

## Architectural Considerations

### Why Not Kafka?

Kafka is often suggested for high-throughput event systems, but it has trade-offs that don't always fit webhook delivery:

**Kafka limitations for this use case:**

1. **No native delayed delivery** — Kafka is a log, not a delay queue. Retry after 1 hour requires workarounds:
   - Polling with timestamp checks (blocks partition)
   - Multiple topics per delay interval (inflexible)
   - External delay service (adds complexity)

2. **Loss of queryability** — PostgreSQL allows:
   ```sql
   SELECT * FROM events WHERE subscription_id = ? AND status = 'failed'
   ```
   Kafka requires consuming entire partitions or maintaining secondary indexes.

3. **Retry scheduling** — PostgreSQL's `next_attempt_at` with `FOR UPDATE SKIP LOCKED` is elegant. Kafka requires external coordination.

**When Kafka makes sense:**
- Multi-datacenter replication is required
- Event replay is a hard requirement (regulatory, debugging)
- Burst absorption for 10x traffic spikes
- Multiple independent consumers for same events

**Hybrid architecture (if needed):**
```
Client → API → Kafka (buffer) → Worker → PostgreSQL (retry + history)
```
Kafka absorbs ingestion spikes; PostgreSQL remains source of truth for retry scheduling and historical queries.

### Why Not Separate API and Worker?

Currently, dispatch runs as a single binary with embedded API and workers. Separation makes sense at scale:

**Benefits of separation:**
- Independent scaling (API: request-bound, Worker: backlog-bound)
- Fault isolation (worker crash doesn't affect event ingestion)
- Different resource profiles (API: many connections, Worker: CPU/memory for HTTP clients)
- Independent deployments (fix worker bug without touching API)

**Current approach:**
- Single binary simplifies deployment and operations
- Adequate for <10k events/second
- Separation adds CI/CD complexity

**Evolution path:**
```
Phase 1: Single binary (current)
Phase 2: Same repo, separate binaries (cmd/api, cmd/worker)
Phase 3: Separate repos/services (only if team/org requires)
```

**Recommendation:** Keep together until scaling pain is real. Premature separation adds operational overhead without benefit.

---

## Security Considerations

### Current Security Model

| Aspect | Status | Notes |
|--------|--------|-------|
| HMAC signatures | ✅ | Payload signing for verification |
| TLS | ⚠️ | Depends on deployment (reverse proxy) |
| Authentication | ❌ | No API authentication |
| Authorization | ❌ | No RBAC |
| Secret management | ⚠️ | Secrets in database (plaintext) |
| Input validation | ✅ | Basic validation |
| Rate limiting (API) | ❌ | Only for outbound webhooks |

### Security Evolution Path

1. **API Authentication** (Priority: High)
   - API keys with hashing
   - JWT tokens
   - Estimated effort: 2-3 days

2. **Secret Encryption** (Priority: Medium)
   - Encrypt subscription secrets at rest
   - Use external secret manager (Vault)
   - Estimated effort: 1-2 days

3. **API Rate Limiting** (Priority: Medium)
   - Protect against abuse
   - Per-client limits
   - Estimated effort: 1 day

4. **Audit Logging** (Priority: Low)
   - Log all API operations
   - Compliance requirements
   - Estimated effort: 2-3 days

---

## Feature Comparison

### vs. Full-Featured Solutions (hook0, Svix, Convoy)

| Feature | dispatch | hook0/Svix/Convoy |
|---------|----------|-------------------|
| Core delivery | ✅ | ✅ |
| Retry with backoff | ✅ | ✅ |
| Rate limiting | ✅ | ✅ |
| Circuit breaker | ✅ | ✅ |
| Multi-tenancy | ❌ | ✅ |
| UI/Dashboard | ❌ | ✅ |
| Payload transformation | ❌ | ✅ |
| Event replay | ❌ | ✅ |
| Webhook verification | ❌ | ✅ |
| Managed service option | ❌ | ✅ |

### When to Use dispatch

✅ **Good fit:**
- Single-tenant internal service
- Simple webhook delivery needs
- Team comfortable with Go
- Want full control over infrastructure
- Learning/portfolio project

❌ **Consider alternatives:**
- Multi-tenant SaaS product
- Need UI for non-technical users
- Complex transformation requirements
- Want managed service
- Need enterprise support

---

## Recommended Evolution Roadmap

### Phase 1: Production Hardening (1-2 weeks)
- [ ] API authentication (API keys)
- [ ] Secret encryption at rest
- [ ] Integration tests with testcontainers
- [ ] Load testing and benchmarks

### Phase 2: Operational Excellence (2-3 weeks)
- [ ] Dead letter queue with replay API
- [ ] Subscription health checks
- [ ] Enhanced metrics and alerting
- [x] Distributed rate limiting (Redis) ✅ v0.4.0
- [x] Distributed circuit breaker (Redis) ✅ v0.4.0

### Phase 3: Feature Expansion (4-6 weeks)
- [ ] Multi-tenancy support
- [ ] Basic payload transformation
- [ ] Event ordering guarantees
- [ ] Batch delivery option

### Phase 4: Scale (as needed)
- [ ] Batch writes for higher throughput
- [ ] PostgreSQL partitioning
- [ ] Separate API/Worker binaries
- [ ] Kafka integration (only if replay/multi-DC required)
- [ ] Multi-region support

---

## Contributing

When addressing limitations:

1. **Start with ADR** - Document the decision before implementing
2. **Maintain simplicity** - Don't over-engineer
3. **Add tests first** - TDD approach
4. **Update documentation** - Keep this file current
5. **Consider backwards compatibility** - Don't break existing deployments
