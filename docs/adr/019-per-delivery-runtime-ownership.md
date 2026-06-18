# ADR 019: Per-Delivery Runtime Ownership

## Status

Accepted

## Context

ADR 018 introduced stable per-subscription delivery rows, but v0.10 kept Kafka processing and
retry ownership on aggregate event rows. That left the main product flaw in place: one destination's
outcome could still obscure or repeat another destination's work.

V1 requires every destination to own its own processing state, retry lease, attempt attribution,
and recovery path.

## Decision

For new events, `deliveries` are the authoritative runtime unit.

Kafka processing now:

1. loads active matching subscriptions;
2. initializes the event row and frozen delivery set before external HTTP calls;
3. claims processable delivery rows with owner/deadline fencing;
4. processes only the claimed delivery rows;
5. writes each delivery outcome and attributed attempt atomically;
6. commits Kafka offsets only after durable delivery outcomes exist or recovery is represented
   by a delivery lease/retry state.

The retry poller claims due, throttled, and expired `deliveries`, not aggregate `events`.
`GetRetryBacklogStats` also measures delivery backlog.

Event status remains as a compatibility and query projection derived from delivery states. Event
rows are no longer the retry ownership record for new runtime work.

Legacy aggregate event retry methods remain in the repository for compatibility and tests around
old rows, but the app bootstrap no longer wires the retry poller through them.

## Consequences

- Duplicate Kafka processing reuses the frozen delivery set and skips destinations that are already
  terminal or successful.
- Later subscription changes do not silently alter an existing event's delivery targets.
- Mixed fan-out outcomes are independently inspectable and retryable.
- A persistence failure after an HTTP call can leave the initialized delivery leased. Kafka redelivery
  should not immediately duplicate that destination while the lease is active; the retry poller recovers
  expired processing deliveries.
- Attempt history is now attributed with `delivery_id` and `subscription_id` for new runtime attempts.
- The handler has a temporary compatibility surface because legacy aggregate event methods still exist.
  A post-v0.11 simplification pass may isolate or remove unreachable compatibility paths.

## Related

- [ADR 015: Atomic Outcome Persistence and Kafka Commit Safety](015-atomic-outcome-persistence.md)
- [ADR 016: Owner-Fenced Retry Claim Leases](016-owner-fenced-retry-leases.md)
- [ADR 018: Per-Subscription Delivery Identity](018-per-subscription-delivery-identity.md)
- [v0.11.0 execution plan](../exec-plans/done/v0.11.0.md)
