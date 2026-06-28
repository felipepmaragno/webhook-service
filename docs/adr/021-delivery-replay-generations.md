# ADR 021: Delivery Replay Generations and Clean Runtime Cutover

## Status

Accepted

## Context

V1 requires operators to replay terminal delivery failures without database edits. The current
delivery row owns stable destination identity, retry attempts, leases, and terminal state. Resetting
that row without an explicit generation would make old and replayed attempts indistinguishable and
would erase the meaning of the exhausted attempt budget.

Earlier increments retained aggregate event retry and nullable-attribution compatibility. This
project has no deployed database or supported old-schema consumer, so that compatibility adds a
second conceptual model without protecting real users.

## Decision

Add a positive `generation` to deliveries and delivery attempts. Initial rows are generation 1.
Only a failed delivery can be replayed. Replay atomically:

1. locks the failed delivery;
2. increments its generation;
3. resets attempts to zero;
4. clears next-attempt, error, delivered, and lease fields;
5. sets status to `retrying` with an immediate schedule;
6. refreshes the aggregate event projection.

The stable delivery ID and frozen URL, secret, payload, and resilience policy do not change. Attempt
numbers restart within each generation, while historical attempts remain immutable and queryable.
Concurrent requests rely on the failed-state update predicate: one transition succeeds and later
requests receive a conflict.

The API exposes `POST /deliveries/{id}/replay` and returns `202 Accepted` after durable scheduling.
It does not call the receiver directly. The retry poller claims the new generation through the
existing lease and outcome path.

Use one fresh-installation migration baseline. Remove aggregate runtime interfaces, event-level
lease columns, nullable attempt attribution, compatibility aliases, and production-unused event
batching/direct-write paths. Every attempt must identify one matching event/delivery/subscription.
Historical reasoning remains in ADRs, completed plans, and learnings rather than executable code.

## Consequences

- Replay history is explicit and auditable.
- Successful destinations cannot be replayed accidentally through this endpoint.
- Replay inherits normal signing, rate control, retry, lease, and persistence behavior.
- Every attempt consumer receives explicit generation, delivery, and subscription identity.
- Fresh installation and full removal are reversible through the single baseline down migration.
- There is intentionally no supported upgrade path from the abandoned aggregate schema.

## Related

- [ADR 018: Per-Subscription Delivery Identity](018-per-subscription-delivery-identity.md)
- [ADR 019: Per-Delivery Runtime Ownership](019-per-delivery-runtime-ownership.md)
- [v0.13.0 execution plan](../exec-plans/done/v0.13.0.md)
