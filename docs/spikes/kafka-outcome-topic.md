# Spike Proposal: Kafka Outcome Topic and Asynchronous Persistence

> **Status:** Proposed
> **Recorded:** 2026-06-13
> **Earliest evaluation:** after v0.7.0 retry leases and v0.8.0 retry-poller capacity
> **Decision impact:** would supersede or amend ADR 015 if adopted

## Hypothesis

Decouple webhook delivery from PostgreSQL outcome persistence by publishing each delivery result
to a second Kafka topic. A separate projection consumer would persist event state, attempt history,
and retry scheduling data.

```text
input topic
    -> delivery worker
    -> webhook endpoint
    -> outcome topic
    -> outcome projection consumer
    -> PostgreSQL
```

The delivery worker would acknowledge an input event only after its outcome is durably published.
PostgreSQL availability would no longer be required immediately after every webhook call.

## Why investigate it

- Kafka could buffer outcomes during PostgreSQL outages or backpressure.
- Delivery and database projection could scale independently.
- Outcome writes could be batched by a dedicated consumer.
- The outcome stream could support auditing, analytics, or additional projections.
- The delivery worker's synchronous post-delivery persistence path would become smaller.

## What it does not solve

Kafka cannot atomically combine an arbitrary external HTTP call with publishing its outcome.
This failure window remains:

```text
webhook succeeds
-> worker crashes before outcome publication
-> input event is redelivered
-> webhook may be called again
```

The design therefore remains at-least-once. Receiver-side idempotency is still required.

Publishing an outcome and committing the consumed input offset is another atomicity boundary.
Without Kafka transactions, a crash after publishing but before committing can produce duplicate
outcomes and duplicate webhook calls. Kafka transactions may reduce that Kafka-only window, but
library support, failure semantics, and operational cost must be verified rather than assumed.

## Complexity introduced

- A versioned outcome-event schema and compatibility policy.
- Stable identity for event, subscription, delivery, and attempt.
- Idempotent PostgreSQL projection of duplicate outcomes.
- Ordering and stale-outcome rules for multiple attempts of the same delivery.
- Outcome-topic partition key, retention, replay, and compaction decisions.
- Poison-outcome handling and possibly a projection DLQ.
- Projection lag metrics, alerts, and operational recovery procedures.
- Eventual consistency for status APIs and retry scheduling.
- Independent deployment and scaling of another consumer pipeline.

Moving the SQL does not remove state-management complexity. It converts a local PostgreSQL
transaction into a distributed log-and-projection consistency problem.

## Central design questions

1. Is the outcome topic the authoritative source of truth or only a transport buffer?
2. Who owns retry scheduling: the delivery worker, the outcome projection consumer, or another scheduler?
3. What is the idempotency key: event ID, `(event, subscription, attempt)`, or a generated delivery-attempt ID?
4. How does the projection reject duplicate, stale, or out-of-order outcomes?
5. What partition key preserves required ordering without creating hotspots?
6. Are Kafka transactions required, and does the selected Go client support the exact consume-transform-produce contract safely?
7. What projection lag is acceptable for status APIs and retry timing?
8. How can outcomes be replayed without regressing finalized state or scheduling duplicate retries?
9. How are schema evolution and mixed producer/consumer versions handled during deployment?
10. Does the expected availability or throughput gain justify the additional service and operational surface?

## Proposed experiments

Keep the investigation bounded and disposable:

1. Define a candidate outcome envelope with version, event ID, subscription ID, attempt ID,
   attempt number, observed result, timestamps, and retry recommendation.
2. Prototype consume-input -> publish-outcome -> commit-input behavior with and without Kafka
   transactions. Inject crashes at each boundary and record duplicates or losses.
3. Build an idempotent projection consumer and test duplicate and out-of-order outcomes against PostgreSQL.
4. Simulate PostgreSQL unavailability, measure outcome lag and recovery throughput, and compare it
   with the current ADR 015 transaction design.
5. Model retry scheduling under projection lag and prove that replay cannot overwrite newer state.
6. Estimate operational cost: topic storage, consumer lag monitoring, deployment count, alerts,
   incident recovery, and schema compatibility burden.

## Evaluation criteria

Recommend adoption only if evidence shows a material benefit in at least one target dimension:

- webhook delivery availability during PostgreSQL outages
- sustainable throughput or database batching efficiency
- required reuse of the outcome stream by multiple consumers
- simpler operational recovery than the current synchronous transaction path

The recommendation must also demonstrate:

- no acknowledged outcome loss under injected failures
- deterministic duplicate and stale-outcome handling
- bounded status and retry-scheduling lag
- a supportable Kafka transaction or idempotency strategy
- observability sufficient to operate and replay the projection safely

## Current recommendation

Do not implement this architecture now. Complete v0.7.0 and v0.8.0 first so the current model has
crash recovery and measurable retry capacity. Then run this as a time-boxed architectural spike if
PostgreSQL coupling is an observed availability or throughput constraint, or if another consumer
genuinely needs the outcome stream.

The default remains ADR 015: atomically persist the outcome and attempts in PostgreSQL, then commit
the input Kafka offset.

## Related context

- [ADR 012: Kafka Event Queue](../adr/012-kafka-event-queue.md)
- [ADR 015: Atomic Outcome Persistence](../adr/015-atomic-outcome-persistence.md)
- [v0.6.0 learning: Atomic Delivery Persistence](../learnings/v0.6.0-atomic-delivery-persistence.md)
- [Kafka package context](../../internal/kafka/README.md)
- [PostgreSQL package context](../../internal/repository/postgres/README.md)
