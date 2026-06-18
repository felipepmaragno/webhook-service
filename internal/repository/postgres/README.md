# PostgreSQL Repositories

> Local implementation context for engineers and coding agents. Read this file before
> changing SQL, event claiming, transaction boundaries, migrations, or repository interfaces.

## Authority

Schema and persistence behavior must agree with `migrations/`, `docs/spec.md`, relevant ADRs,
and the repository interfaces in `internal/repository/interfaces.go`. This file is a map of the
current implementation and its invariants, not an independent specification.

## Responsibilities

- `event.go`: event CRUD, inactive per-delivery persistence, retry selection, outcome transactions, and attempt history.
- `subscription.go`: subscription CRUD and wildcard event-type lookup.
- `batcher.go`: generic event batching support retained for repository operations.
- `testhelper_test.go`: Testcontainers PostgreSQL setup and migration application for integration tests.

## Transaction boundaries

`EventOutcome` groups one event state transition with all attempts produced while calculating it.

- `PersistNewOutcomes` inserts Kafka-originated event rows and attempts in one transaction.
- `PersistClaimedOutcomes` updates retry-originated rows only for the current lease and inserts attempts atomically.
- Any queued SQL failure rolls back the entire transaction.
- Kafka-originated duplicate event IDs use `ON CONFLICT (id) DO NOTHING`; attempt rows from a
  successfully committed repeated delivery are still inserted.

Do not replace these operations with separate status and attempt calls in a delivery path. The older
single/batch methods remain available, but they do not provide the delivery outcome atomicity contract.

## Per-delivery model

v0.10 adds `deliveries` and nullable attempt attribution. This model is intentionally available
before the worker runtime uses it.

- `InitializeEventDeliveries` inserts the aggregate event row and a frozen event/subscription
  delivery set in one transaction.
- `GetDeliveriesByEventID` and `GetDeliveryByID` read the inactive per-delivery model.
- `PersistDeliveryOutcome` updates one delivery and inserts attributed attempts atomically.
- Legacy aggregate attempts keep `delivery_id` and `subscription_id` null because the old runtime
  did not record destination identity.

Do not move retry ownership from `events` to `deliveries` in this package until v0.11 changes the
worker and poller paths together.

## Retry selection today

`ClaimRetryEvents` uses `FOR UPDATE SKIP LOCKED`, selects due `retrying`/`throttled` rows and expired
`processing` rows, then atomically stores owner and deadline. `PersistClaimedOutcomes` requires the
same owner and exact deadline, clears lease metadata with the outcome, and treats zero affected rows
as `ErrClaimLost`. The exact deadline distinguishes successive claims by the same instance ID.

`GetRetryBacklogStats` aggregates due retry/throttled rows, the oldest due or expired
schedule, expired processing claims, and active leases. The query is limited to the retry
status subset and validated against `idx_events_retry_claimable`. It supports scheduler
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
