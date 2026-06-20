# PostgreSQL Repositories

> Local implementation context for engineers and coding agents. Read this file before
> changing SQL, event claiming, transaction boundaries, migrations, or repository interfaces.

## Authority

Schema and persistence behavior must agree with `migrations/`, `docs/spec.md`, relevant ADRs,
and the repository interfaces in `internal/repository/interfaces.go`. This file is a map of the
current implementation and its invariants, not an independent specification.

## Responsibilities

- `event.go`: event reads, per-delivery persistence, retry selection, replay scheduling, outcome transactions, projection updates, and attempt history.
- `retention.go`: bounded attempt-body redaction and terminal-event deletion.
- `subscription.go`: subscription CRUD and wildcard event-type lookup.
- `testhelper_test.go`: Testcontainers PostgreSQL setup and migration application for integration tests.

The concrete PostgreSQL repository may remain broad because it owns SQL transaction boundaries.
Package dependencies must remain narrow: API reads/replay, Kafka delivery runtime, retry claiming,
and retention each consume only the role they need. Do not add a broad repository interface to
satisfy a package mock; define the contract at the consumer when no shared role exists.

## Transaction boundaries

- Delivery outcome persistence updates one delivery, inserts attributed attempts, and refreshes the event projection in one transaction.
- Replay locks one failed delivery, increments its generation, resets its attempt budget, and refreshes the event projection in one transaction.
- Any queued SQL failure rolls back the entire transaction.
- Kafka-originated duplicate event IDs reuse the existing frozen delivery set.

Do not replace these operations with separate status and attempt calls in a delivery path.

## Per-delivery model

`deliveries` and mandatory attempt attribution are the only runtime model.

- `InitializeEventDeliveries` inserts the aggregate event row and a frozen event/subscription
  delivery set in one transaction.
- `GetDeliveriesByEventID` reads the frozen per-delivery model for an event.
- `ClaimEventDeliveries` claims delivery rows scoped to Kafka message event IDs.
- `ClaimDeliveries` claims due retry/throttled delivery rows and expired processing delivery rows.
- `PersistClaimedDeliveryOutcome` updates one delivery and inserts attributed attempts atomically.
- `ReplayFailedDelivery` schedules the next generation without changing the delivery ID or frozen destination data.
- Attempts are ordered and numbered within their generation; the initial generation is 1.
- The schema requires each attempt's event, delivery, and subscription identity to match one
  concrete delivery row.

The migration directory is a clean current-state baseline. There is no supported upgrade path from
the removed aggregate runtime schema.

## Retention

`RetentionRepository` uses bounded CTEs and `FOR UPDATE SKIP LOCKED`, allowing every worker to run
cleanup without a singleton leader. A cycle redacts old attempt response bodies before deleting old
terminal events. Event deletion excludes any event with a pending, processing, retrying, or throttled
delivery and relies on declared foreign-key cascades for delivery and attempt history.

## Retry selection today

`ClaimDeliveries` uses `FOR UPDATE SKIP LOCKED`, selects due `retrying`/`throttled` deliveries and
expired `processing` deliveries, then atomically stores owner and deadline. `PersistClaimedDeliveryOutcome`
requires the same owner and exact deadline, clears lease metadata with the outcome, refreshes the event
projection, and treats zero affected rows as `ErrClaimLost`. The exact deadline distinguishes successive
claims by the same instance ID.

`GetRetryBacklogStats` aggregates due retry/throttled rows, the oldest due or expired
schedule, expired processing claims, and active leases. The query is limited to the retry
status subset and validated against `idx_deliveries_retry_claimable`. It supports scheduler
gauges and must not become an event-by-event metrics query.

## SQL rules for changes

1. Parameterize all values; do not build SQL with string interpolation.
2. Preserve context propagation through pgx calls.
3. Return operation context with wrapped errors at transaction boundaries.
4. Treat affected-row counts as part of correctness when ownership or fencing is expected.
5. Add indexes only for demonstrated query access paths and verify them against representative SQL.
6. Every schema change needs ordered up/down migrations and corresponding test migration updates.
7. Keep domain state transitions in `internal/domain`; repositories persist state rather than inventing it.

## Verification

Repository behavior must be tested against real PostgreSQL with Testcontainers. Mock-only tests cannot
verify locking, constraints, rollback, affected-row counts, or SQL compatibility.

```bash
go test ./internal/repository/postgres/...
go test ./internal/app/...
```

For transaction changes, include both commit and forced-rollback scenarios. For claim changes, include
concurrent workers, eligibility boundaries, and stale-owner behavior. Update this file when transaction,
locking, schema, or repository ownership rules change.
