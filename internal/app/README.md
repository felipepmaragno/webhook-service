# Application Assembly and E2E Harness

> Local implementation context for engineers and coding agents. Read this file before
> changing service startup, dependency wiring, shutdown order, or end-to-end tests.

## Responsibilities

- `api.go`: assemble PostgreSQL, Kafka producer, HTTP routes, health, and API metrics.
- `worker.go`: assemble PostgreSQL, Redis/local destination protection, delivery handler, Kafka consumer,
  retry poller, retention cleaner, and worker metrics.
- `e2e_test.go`: validate thin user-visible flows with real PostgreSQL, Kafka, HTTP servers,
  migrations, API assembly, and worker delivery components.

The command packages should remain thin wrappers around this package. Reusable bootstrap logic belongs
here so it can be exercised without launching external processes.

## Assembly invariants

1. The API publishes new events to Kafka; it does not create the delivery outcome row first.
2. The worker Kafka consumer and retry poller share one `DeliveryHandler` so delivery rules cannot diverge.
3. Destination protection is wired through one `RateLimiter`. `REDIS_URL` enables distributed
   sliding-window enforcement; absent `REDIS_URL` uses local enforcement.
4. Startup errors must close resources already created by that startup path.
5. Worker shutdown cancels work, stops consumer, poller, and retention cleaner, shuts down metrics,
   then closes PostgreSQL. Cleanup shutdown waits for an in-flight cycle.
6. Dynamically assigned listeners must expose normalized loopback addresses to tests.

## Observability wiring

`internal/kafka` emits delivery lifecycle observations through `DeliveryObserver`; this package adapts
those observations to Prometheus metrics. Keep that boundary intact. Kafka owns delivery semantics,
while app assembly owns concrete metric names, labels, and registration.

This avoids passing many callback-shaped metric options into the delivery handler and keeps future
metrics backend changes out of the Kafka runtime path.

## E2E harness design

The E2E suite intentionally uses:

- Testcontainers for PostgreSQL, Redis, and Kafka
- a real API HTTP listener
- a deterministic local webhook receiver
- a direct Kafka partition reader instead of consumer-group coordination
- polling assertions with deadlines rather than fixed correctness sleeps
- a warmup event before measured scenarios to remove Kafka startup timing from assertions

The direct partition reader makes delivery deterministic but does **not** validate consumer-group rebalancing
or real offset commits. Commit/no-commit semantics belong in `internal/kafka` component tests; SQL atomicity
belongs in PostgreSQL integration tests. Keep the E2E suite thin and use it for cross-component contracts.

The replay E2E schedules a failed generation through the production API router and proves that the
real retry path delivers generation 2 with signing and preserved generation-1 history. Retention
batching and locking remain PostgreSQL integration concerns rather than full-stack timing tests.

## Adding scenarios

Add an E2E case when a change crosses at least two subsystem boundaries and cannot be proven adequately in
a focused package test. Prefer extending the existing stack over creating another full container topology.
Use unique event and subscription IDs, reset receiver state between scenarios, and assert durable database
state in addition to observing the HTTP request.

```bash
go test ./internal/app/...
```

Docker is required. Update this file when service composition, resource lifecycle, or E2E harness strategy changes.
