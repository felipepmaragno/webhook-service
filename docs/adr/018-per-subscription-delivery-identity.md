# ADR 018: Per-Subscription Delivery Identity

## Status

Accepted

## Context

Dispatch currently stores one aggregate event state for fan-out delivery. That makes the system
simple, but it cannot represent independent destination outcomes:

- one destination can make the whole event terminal;
- retrying one destination can repeat calls to destinations that already succeeded;
- delivery attempts do not identify the subscription that received the HTTP call;
- subscription changes between attempts can change the target set.

V1 requires a stable event-destination model before processing and retry ownership can move away
from aggregate event rows.

## Decision

Add a durable `deliveries` model. A delivery is uniquely identified by the event ID and subscription
ID pair:

```text
delivery_id = event_id + ":" + subscription_id
```

The delimiter is not parsed for meaning; it only creates a deterministic, readable key. The database
also enforces `UNIQUE (event_id, subscription_id)`.

When an event is initialized for the new model, Dispatch freezes the active matching subscriptions
into delivery rows. Later subscription activation, deactivation, URL changes, or policy changes do
not silently change that event's delivery set.

Each delivery snapshots the fields required for deterministic future processing:

- subscription ID;
- destination URL;
- secret;
- `rate_limit`;
- `burst_size`;
- `concurrency_limit`;
- event type, source, and payload.

Delivery state is independent from aggregate event state. Delivery statuses use their own
`delivery_status` enum with the same vocabulary as event state: `pending`, `processing`,
`delivered`, `retrying`, `throttled`, and `failed`.

Event status becomes a projection of delivery states:

1. zero deliveries -> `delivered`;
2. any `processing` -> `processing`;
3. otherwise any `retrying` -> `retrying`;
4. otherwise any `throttled` -> `throttled`;
5. otherwise any `pending` -> `pending`;
6. otherwise any `failed` -> `failed`;
7. otherwise all delivered -> `delivered`.

Delivery attempts gain nullable `delivery_id` and `subscription_id` columns. Existing attempts keep
null attribution because the historical aggregate model did not record which subscription produced
each HTTP call. New per-delivery attempts can reference the exact delivery.

## Increment Boundary

v0.10.0 only adds the durable model, repository methods, and read representation. Current Kafka and
retry processing continue to use aggregate event rows until v0.11.0.

This keeps the migration reversible and validates the model before external HTTP processing depends
on it.

## Consequences

- Future retries can target only the delivery that still needs work.
- Future attempt history can be audited per destination.
- Existing aggregate event reads and legacy attempts remain compatible.
- Storage and API shape become more complex before runtime behavior changes.
- Subscription snapshots may diverge from current subscription settings for old events; this is
  intentional because the event target set is frozen.

## Related

- [ADR 015: Atomic Outcome Persistence and Kafka Commit Safety](015-atomic-outcome-persistence.md)
- [ADR 016: Owner-Fenced Retry Claim Leases](016-owner-fenced-retry-leases.md)
- [ADR 017: Rate-Control Contract Normalization](017-rate-control-contract.md)
- [v0.10.0 execution plan](../exec-plans/done/v0.10.0.md)
