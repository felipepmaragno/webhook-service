# ADR 022: Bounded Delivery Data Retention

## Status

Accepted

## Context

Dispatch currently retains events, frozen delivery secrets and payloads, and receiver response
excerpts indefinitely. Unbounded storage contradicts the v1 operational promise and makes secret
rotation overlap impossible to close predictably.

Cleanup must not race active retries, require a singleton worker, or create large deletion
transactions that disrupt delivery persistence.

## Decision

Worker processes run bounded cleanup cycles using PostgreSQL as the coordination authority.

Each cycle:

1. redacts old non-null delivery-attempt response bodies in a limited `FOR UPDATE SKIP LOCKED` batch;
2. deletes old terminal events in a limited `FOR UPDATE SKIP LOCKED` batch;
3. reports counts, duration, failure, and last-success time.

Attempt body redaction preserves attempt identity, event/delivery/subscription attribution,
generation, status, error, duration, and timestamp. Event deletion relies on existing foreign-key
cascades to remove deliveries and attempts.

An event is deletable only when it is older than the event cutoff, its projected status is delivered
or failed, and no delivery is pending, processing, retrying, or throttled. Zero-delivery completed
events are eligible. Active work is ineligible regardless of age.

Multiple workers may clean concurrently. Row locking and batch limits divide work without a leader.
Cleanup failures are logged and counted but do not stop Kafka or retry processing. Shutdown waits for
an in-flight cycle.

Configuration defaults:

| Setting | Default |
|---------|---------|
| `ATTEMPT_BODY_RETENTION` | `168h` (7 days) |
| `EVENT_RETENTION` | `720h` (30 days) |
| `RETENTION_CLEANUP_INTERVAL` | `1h` |
| `RETENTION_BATCH_SIZE` | `1000` |

All values are positive and event retention cannot be shorter than body retention.

## Consequences

- Storage and frozen secret lifetime become bounded by an explicit operator policy.
- Attempt diagnostics become less detailed after body redaction while metadata remains useful.
- Cleanup load is bounded and horizontally safe, but exact deletion time is eventual rather than
  equal to the configured cutoff.
- Event deletion permanently removes query and replay history after retention.
- Backup retention remains an independent operator responsibility.

## Related

- [ADR 002: PostgreSQL Storage](002-postgresql-storage.md)
- [Deployment security contract](../guides/deployment-security.md)
- [v0.13.0 execution plan](../exec-plans/done/v0.13.0.md)
