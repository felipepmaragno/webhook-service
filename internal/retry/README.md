# Retry Scheduler

> Local implementation context for engineers and coding agents. Read this file before
> changing polling, retry claims, scheduling, concurrency, or retry shutdown behavior.

## Authority

The product retry contract lives in `docs/spec.md`; ADRs explain durable scheduling choices;
the active exec plan defines work not implemented yet. This file describes current package behavior.

## Current behavior

- `policy.go` calculates exponential-backoff delays and maximum-attempt behavior.
- `poller.go` starts a drain cycle immediately and on each configured interval.
- Full claims continue immediately while a bounded batch slot is available.
- Empty or partial claims stop draining until the next interval.
- `ClaimDeliveries` selects due retry/throttled deliveries and expired processing leases, then stores owner and deadline.
- `DeliveryHandler.ProcessDeliveries` reuses the delivery execution path and atomically persists attributed outcomes.
- Processor persistence errors are logged as failed retry batches and are not reported as successful outcomes.
- The poller depends on the retry delivery repository role only: delivery claiming plus backlog stats.
  It should not depend on event reads, event writes, subscription lookup, or legacy aggregate persistence.

## Critical invariants

1. PostgreSQL is the only durable schedule for delayed retries.
2. The poller must not duplicate delivery rules; it delegates delivery to `DeliveryProcessor`.
3. Retry outcome persistence must return errors to the poller.
4. Shutdown must stop new polling and wait for in-flight goroutines tracked by the poller.
5. Time-dependent behavior should be deterministic in tests; avoid arbitrary sleeps when a clock or synchronization hook can express the condition.

## Lease and fencing model

`FOR UPDATE SKIP LOCKED` prevents concurrent selection while the claim statement runs. Durable
ownership is stored on delivery rows in `processing_owner` and `processing_deadline`. Expired processing rows are
eligible for reclaim. Outcome persistence must match both owner and exact deadline, which fences
stale work even when the same instance ID acquires a later lease generation.

`MaxConcurrentBatches` is enforced by the single scheduler coordinator. No ticker-triggered
claim loop can overlap it. Shutdown closes the scheduler to new claims and waits for every
tracked batch. Poll interval controls idle discovery latency, not backlog throughput.

Scheduler metrics report claimed and reclaimed deliveries, active batches, empty polls, claim
and persistence failures, stale-owner rejection, scheduling lag, and PostgreSQL backlog
age/counts. Keep these metrics free of event and subscription labels.

## Change protocol

1. Read the active exec plan and the PostgreSQL package README before changing claim semantics.
2. Write poller contract tests before implementation, including claim identity, shutdown, and processor-error paths.
3. Any durable claim change requires PostgreSQL integration tests, not only mock tests.
4. Lease work must prove claim exclusivity, expiration recovery, and stale-owner rejection independently.
5. Run:

```bash
go test -race ./internal/retry/...
go test ./internal/repository/postgres/...
go test ./internal/app/...
```

Update this file when scheduler ownership, claim semantics, concurrency, or shutdown behavior changes.
