# Application Assembly and E2E Harness

> Local implementation context for engineers and coding agents. Read this file before
> changing service startup, dependency wiring, shutdown order, or end-to-end tests.

## Responsibilities

- `api.go`: assemble PostgreSQL, Kafka producer, HTTP routes, health, and API metrics.
- `worker.go`: assemble PostgreSQL, optional Redis resilience, delivery handler, Kafka consumer,
  retry poller, and worker metrics.
- `e2e_test.go`: validate thin user-visible flows with real PostgreSQL, Redis, Kafka, HTTP servers,
  migrations, API assembly, and worker delivery components.

The command packages should remain thin wrappers around this package. Reusable bootstrap logic belongs
here so it can be exercised without launching external processes.

## Assembly invariants

1. The API publishes new events to Kafka; it does not create the delivery outcome row first.
2. The worker Kafka consumer and retry poller share one `DeliveryHandler` so delivery rules cannot diverge.
3. Redis is optional. Initialization falls back to in-memory resilience when Redis is absent or unavailable.
4. Startup errors must close resources already created by that startup path.
5. Worker shutdown cancels work, stops consumer and poller, shuts down metrics, closes Redis, then closes PostgreSQL.
6. Dynamically assigned listeners must expose normalized loopback addresses to tests.

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

## Adding scenarios

Add an E2E case when a change crosses at least two subsystem boundaries and cannot be proven adequately in
a focused package test. Prefer extending the existing stack over creating another full container topology.
Use unique event and subscription IDs, reset receiver state between scenarios, and assert durable database
state in addition to observing the HTTP request.

```bash
go test ./internal/app/...
```

Docker is required. Update this file when service composition, resource lifecycle, or E2E harness strategy changes.
