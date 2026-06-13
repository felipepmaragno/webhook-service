# Retry Scheduler

> Local implementation context for engineers and coding agents. Read this file before
> changing polling, retry claims, scheduling, concurrency, or retry shutdown behavior.

## Authority

The product retry contract lives in `docs/spec.md`; ADRs explain durable scheduling choices;
the active exec plan defines work not implemented yet. This file describes current package behavior.

## Current behavior

- `policy.go` calculates exponential-backoff delays and maximum-attempt behavior.
- `poller.go` polls PostgreSQL immediately on startup and then once per configured interval.
- `GetPendingEvents` selects due `retrying` and `throttled` events and changes them to `processing`.
- `DeliveryHandler.ProcessEvents` reuses the Kafka delivery path and atomically persists updated outcomes.
- Processor persistence errors are logged as failed retry batches and are not reported as successful outcomes.

## Critical invariants

1. PostgreSQL is the only durable schedule for delayed retries.
2. The poller must not duplicate delivery rules; it delegates delivery to `EventProcessor`.
3. Retry outcome persistence must return errors to the poller.
4. Shutdown must stop new polling and wait for in-flight goroutines tracked by the poller.
5. Time-dependent behavior should be deterministic in tests; avoid arbitrary sleeps when a clock or synchronization hook can express the condition.

## Current limitation: claims are not leases

`FOR UPDATE SKIP LOCKED` prevents concurrent selection only while the transaction is running.
After selection, rows remain `processing` without an owner or expiration deadline. A worker crash can
therefore strand them permanently. Do not describe the current implementation as crash-recoverable.

The active v0.7.0 plan introduces owner-fenced, expiring leases. Until that plan is completed:

- there is no `processing_owner`
- there is no `processing_deadline`
- expired claims cannot be reclaimed
- stale workers are not fenced from later outcome writes

`MaxConcurrentBatches` is also configured but not currently enforced. That is intentionally deferred
to v0.8.0 after lease correctness is established.

## Change protocol

1. Read the active exec plan and the PostgreSQL package README before changing claim semantics.
2. Write poller contract tests before implementation, including shutdown and processor-error paths.
3. Any durable claim change requires PostgreSQL integration tests, not only mock tests.
4. Lease work must prove claim exclusivity, expiration recovery, and stale-owner rejection independently.
5. Run:

```bash
go test -race ./internal/retry/...
go test ./internal/repository/postgres/...
go test ./internal/app/...
```

Update this file when scheduler ownership, claim semantics, concurrency, or shutdown behavior changes.
