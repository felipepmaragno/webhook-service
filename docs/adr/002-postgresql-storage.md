# ADR 002: PostgreSQL as Storage

## Status
Accepted

## Context
The webhook dispatcher needs persistent storage for:
- Events awaiting delivery
- Delivery attempt history
- Subscription configurations

Requirements:
- Durability (events must not be lost)
- Concurrent access (multiple workers)
- Transactional guarantees
- Operational simplicity

## Decision
Use **PostgreSQL** as the primary data store.

## Alternatives Considered

### Redis + PostgreSQL
**Pros:**
- Redis for queue, PostgreSQL for persistence
- Very fast queue operations

**Cons:**
- Two systems to operate
- Complexity in keeping them in sync
- Redis persistence is less reliable

### Apache Kafka
**Pros:**
- Built for event streaming
- Excellent throughput
- Built-in partitioning

**Cons:**
- Operational complexity (ZooKeeper/KRaft)
- Overkill for single-tenant dispatcher
- Harder to query event status

### RabbitMQ
**Pros:**
- Purpose-built message queue
- Good delivery guarantees

**Cons:**
- Additional infrastructure
- Less flexible querying
- No built-in retry scheduling

### MongoDB
**Pros:**
- Flexible schema
- Good for document storage

**Cons:**
- Weaker transactional guarantees
- Less mature tooling
- Not ideal for queue patterns

## Rationale

### 1. Durability
PostgreSQL provides ACID guarantees:
- Events are durably stored on commit
- No data loss on crash
- Point-in-time recovery possible

### 2. FOR UPDATE SKIP LOCKED
PostgreSQL's `FOR UPDATE SKIP LOCKED` enables efficient concurrent processing:
```sql
SELECT * FROM events 
WHERE status IN ('pending', 'retrying') 
  AND (next_attempt_at IS NULL OR next_attempt_at <= NOW())
FOR UPDATE SKIP LOCKED
LIMIT 10
```
- Workers don't block each other
- No duplicate processing
- Scales horizontally (multiple dispatch instances)

### 3. Operational Simplicity
- Single system to operate
- Well-understood backup/restore
- Excellent monitoring tools
- Most teams already run PostgreSQL

### 4. Flexible Querying
- Query events by status, type, date
- Analytics on delivery attempts
- Easy debugging and troubleshooting

### 5. Horizontal Scaling Path
- Read replicas for queries
- Partitioning by date for large volumes
- Citus for sharding if needed

## Schema Design

```sql
-- Events table with status-based indexing
CREATE TABLE events (
    id VARCHAR(255) PRIMARY KEY,
    status event_status NOT NULL DEFAULT 'pending',
    next_attempt_at TIMESTAMPTZ,
    -- ...
);

-- Partial index for pending events (small, fast)
CREATE INDEX idx_events_pending 
ON events (next_attempt_at) 
WHERE status IN ('pending', 'retrying');
```

## Consequences

### Positive
- Single source of truth
- Strong consistency
- Familiar technology
- Easy to debug and query

### Negative
- Not as fast as dedicated queue for pure throughput
- Connection pooling needed at scale
- Vacuum maintenance for high-write workloads

### Risks
- Table bloat with high event volume
- Mitigated by: partitioning, archival strategy, proper vacuuming

### Performance Considerations
- Use connection pooling (pgxpool)
- Batch inserts for high volume
- Consider partitioning events by created_at for retention
