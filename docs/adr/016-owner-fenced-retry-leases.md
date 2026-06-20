# ADR 016: Owner-Fenced Retry Claim Leases

## Status

Accepted (Implemented)

## Context

The retry poller previously used `FOR UPDATE SKIP LOCKED` to select due rows and changed them
to `processing`. The row lock ended with the claim transaction, while `processing` remained.
If a worker crashed after claiming, no later query considered that row eligible again.

Simply adding a timeout is insufficient. A slow first worker can finish after its lease expires
and after a second worker has reclaimed and processed the same delivery. Without fencing, the stale
first worker could overwrite the newer outcome.

## Decision

PostgreSQL remains the durable retry scheduler. Each retry claim atomically sets:

- `status = processing`
- `processing_owner = INSTANCE_ID`
- `processing_deadline = NOW() + RETRY_LEASE_DURATION`

Due `retrying`/`throttled` rows and expired `processing` rows are eligible for claims. Selection
uses `FOR UPDATE SKIP LOCKED` so concurrent workers cannot claim the same row in one lease generation.

Outcome persistence requires the delivery ID, `processing` status, owner, and exact deadline returned
by the claim. Owner plus deadline acts as the fencing identity. Comparing the deadline is necessary
because the same `INSTANCE_ID` may reclaim an expired event; owner alone would accept stale work.

Successful outcome persistence clears both lease columns in the same transaction that updates state
and inserts attempts. Zero affected rows means the claim was lost and returns `ErrClaimLost`; the
whole outcome transaction rolls back.

The default lease is 30 seconds, longer than the normal 10-second HTTP delivery timeout. Operators
must keep it longer than expected processing time to avoid unnecessary concurrent redelivery.

## Schema behavior

Lease columns exist only on delivery rows in the fresh-installation baseline. Event rows are query
projections and carry no processing ownership.

## Consequences

### Positive

- Worker crashes no longer strand retry deliveries permanently.
- Concurrent workers cannot hold the same current claim.
- Stale workers cannot overwrite outcomes from a newer lease generation.
- Lease acquisition and expiration use PostgreSQL time and one atomic statement.

### Negative

- A delivery that exceeds the lease can run concurrently with a reclaim and reach the receiver twice.
- Correct operation depends on unique worker instance IDs and a sensible lease duration.
- The stale worker learns it lost ownership only when persistence is rejected.
- This is still at-least-once delivery; leases recover work but do not make HTTP exactly-once.

## Alternatives Rejected

- **Permanent `processing` state:** loses retries after crashes.
- **Reset all processing rows during shutdown:** unsafe because shutdown may not know whether HTTP completed.
- **Timeout without fencing:** allows stale workers to overwrite newer outcomes.
- **Owner-only fencing:** fails when one instance ID acquires multiple lease generations.
- **Redis locks:** would split durable scheduling and ownership across two systems.
- **Kafka retry topics:** changes the scheduler architecture and is unnecessary for this increment.

## References

- [ADR 006: Polling vs LISTEN/NOTIFY](006-polling-vs-listen-notify.md)
- [ADR 013: Retry Poller and Distributed Semaphore](013-retry-poller-distributed-semaphore.md)
- [ADR 015: Atomic Outcome Persistence](015-atomic-outcome-persistence.md)
