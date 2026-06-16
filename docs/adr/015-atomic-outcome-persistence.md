# ADR 015: Atomic Outcome Persistence and Kafka Commit Safety

## Status

Accepted (Implemented)

## Context

Kafka workers perform an external HTTP call and then store event state and delivery history
in PostgreSQL. Previously, event state and attempts were separate database operations, and
persistence errors were logged without being returned to the consumer. The consumer could
therefore commit Kafka offsets even when the retry schedule or final outcome was not durable.

The HTTP call, PostgreSQL transaction, and Kafka offset commit cannot participate in one
distributed transaction. The system must choose an explicit failure guarantee.

## Decision

Use at-least-once processing with this ordering:

1. Fetch a Kafka batch without committing offsets.
2. Perform webhook deliveries and compute outcomes.
3. Persist each event outcome and its generated attempts in one PostgreSQL transaction.
4. Commit Kafka offsets only after the PostgreSQL transaction succeeds.
5. On persistence failure, return the error and leave the Kafka batch uncommitted.

Kafka-originated outcomes create event rows with `ON CONFLICT DO NOTHING`. Retry-originated
outcomes update existing event rows only while their owner-fenced claim remains current, as
defined by ADR 016. These semantics remain separate repository operations, but both use the
same atomic outcome boundary.

Malformed Kafka messages remain a poison-message exception: they are logged and committed
without delivery because repeated decoding cannot succeed.

## Consequences

### Positive

- A persistence outage cannot silently acknowledge and lose a retry schedule.
- Event state and attempt history cannot partially commit.
- Kafka redelivery provides recovery after transient database failure.
- The commit contract is mechanically testable with fake readers and real PostgreSQL.

### Negative

- A webhook may be called more than once if delivery succeeds before persistence fails.
- Attempt history cannot record an HTTP call when the database transaction never commits.
- Redelivered batches may repeat successful calls for other events in the same batch.

## Invariants

- Kafka offsets are never committed after outcome persistence returns an error.
- Event state and generated attempts commit or roll back together.
- Duplicate event IDs produce one event row, while durably persisted repeated attempts remain visible.
- Exactly-once webhook delivery is not claimed; receivers should deduplicate by event ID.

## Alternatives Rejected

- **Log and continue:** can permanently lose retry state after committing Kafka offsets.
- **Commit before persistence:** creates a larger event-loss window.
- **Distributed transaction across Kafka, PostgreSQL, and HTTP:** unavailable for arbitrary webhook receivers.
- **Suppress repeated attempts:** would hide HTTP calls that actually occurred and requires a missing per-subscription delivery identity.

## References

- [ADR 012: Kafka Event Queue](012-kafka-event-queue.md)
- [ADR 013: Retry Poller and Distributed Semaphore](013-retry-poller-distributed-semaphore.md)
- [ADR 016: Owner-Fenced Retry Claim Leases](016-owner-fenced-retry-leases.md)
- [ADR 018: Per-Subscription Delivery Identity](018-per-subscription-delivery-identity.md)
