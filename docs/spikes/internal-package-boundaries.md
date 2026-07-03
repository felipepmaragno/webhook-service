# Internal Package Boundaries and Delivery Ownership

> **Status:** Concluded; implementation plan queued
> **Latest review:** 2026-07-03
> **Scope:** Package ownership and dependency direction; no behavior or product change is accepted here

## Question

Does the current `internal/` package structure still represent the system's real ownership model,
or should delivery execution move out of `internal/kafka` before replay becomes a third caller?

## 2026-07-03 Conclusion

The ownership problem is real but should be corrected with a narrow structural refactor, not a broad
package-layout rewrite.

Current evidence:

- `internal/kafka` still owns transport-independent delivery execution: subscription freezing,
  delivery claiming, rate-limit decisions, outbound webhook construction, HMAC signing, HTTP result
  classification, retry/failure outcome calculation, and outcome persistence.
- `internal/retry` already depends on that execution path through `DeliveryProcessor`, but the
  concrete implementation is `kafka.DeliveryHandler`. Retry processing does not involve Kafka.
- `internal/api` still publishes `kafka.EventMessage`, so the HTTP API boundary depends on a broker
  adapter type.
- Repository interfaces are much narrower than they were before v0.11, but delivery runtime
  persistence still lives in the shared `internal/repository` package rather than beside the
  delivery service that consumes it.

Recommended action:

1. Promote the queued [delivery package extraction](../exec-plans/queued/delivery-package-extraction.md)
   plan when this becomes the next active simplification increment.
2. Extract delivery execution into `internal/delivery` while preserving behavior.
3. Leave Kafka producer/consumer in `internal/kafka`; do not introduce `internal/messaging/kafka`
   in the same increment.
4. Defer the API event-envelope cleanup unless it is promoted as a separate API boundary increment.
5. Defer repository interface relocation unless the delivery extraction exposes a clear dependency
   improvement with low churn.

The expected benefit is clearer ownership and easier reasoning for future maintainers: Kafka becomes
the queue adapter, retry remains the durable scheduler, and delivery execution gets a package name
that matches its actual responsibility.

The expected risk is MR size. Moving files and package names can touch many tests without changing
behavior. The implementation plan must stop at one increment: delivery extraction only.

## v0.13 Conclusion

Replay will atomically schedule a new failed-delivery generation in PostgreSQL and let the existing
retry poller invoke delivery execution. It does not become a direct caller of
`kafka.DeliveryHandler`, so the extraction trigger is absent. V0.13 will remove dead aggregate
runtime contracts but will not move the active delivery engine. Reconsider this spike only if a
future real caller needs synchronous delivery execution outside Kafka/retry ownership.

## Current Assessment

The structure is healthy enough for the current project size. It avoids excessive nesting and has
several clear boundaries:

- `internal/app` owns dependency assembly and process lifecycle;
- `internal/api` owns HTTP routing and transport DTOs;
- `internal/domain` owns core state and projection rules;
- `internal/repository/postgres` owns concrete persistence;
- `internal/resilience` owns destination-protection mechanisms;
- `internal/testutil` keeps infrastructure harness helpers out of production APIs.

The main issue is not directory count. It is that package names and dependency direction no longer
match runtime ownership consistently.

## Main Ownership Problems

### Kafka owns behavior that is not Kafka-specific

`internal/kafka` currently contains:

- Kafka producer, consumer, and message representation;
- delivery orchestration;
- outbound webhook HTTP construction;
- HMAC signing;
- receiver result classification;
- rate limiter, circuit breaker, and semaphore coordination;
- delivery outcome computation.

The retry poller already calls `kafka.DeliveryHandler` when no Kafka operation is involved. Replay
would become a third non-transport concern depending on a Kafka-named service. This is evidence that
delivery execution is application behavior and Kafka is only one ingestion transport.

### The API publishes a Kafka representation

`api.EventPublisher` accepts `kafka.EventMessage`, coupling the HTTP boundary to the broker adapter's
wire type. An application-level accepted-event command or envelope would let Kafka own serialization
without making the API import the transport package.

### Repository interfaces do not consistently belong to consumers

The API-owned subscription administration interface follows idiomatic Go ownership. Event,
delivery, retry, and legacy contracts still live centrally in `internal/repository/interfaces.go`.
The broad `EventRepository` composition also preserves current and dead aggregate operations in one
surface.

Consumer-owned contracts would make dependencies explicit:

- API read interfaces in `internal/api`;
- delivery persistence interfaces beside the delivery service;
- retry claim/backlog interfaces in `internal/retry`;
- concrete implementations in `internal/repository/postgres`.

### Domain structs remain convenient transport representations

JSON tags on domain events, deliveries, subscriptions, and attempts make direct HTTP serialization
easy. The v0.12 secret leak showed the risk: a new internal field can become public accidentally.
Dedicated response DTOs should defend the external boundary even if domain types retain JSON tags
for internal serialization.

### Small secondary concerns

- `internal/clock` is currently unused by production packages. Adopt it deliberately or remove it.
- `internal/retry` contains timing policy and durable polling, but they remain cohesive enough at
  the current size; splitting it now would add ceremony without clear benefit.
- `internal/observability` is broad, but metrics, health, middleware, and logging context still form
  one manageable operational concern.

## Candidate Target

```text
internal/
  api/                  HTTP transport, request/response DTOs
  app/                  assembly and lifecycle
  domain/               entities, states, projections
  delivery/             transport-independent delivery execution
    handler.go           orchestration and outcome computation
    sender.go            outbound HTTP
    signature.go         HMAC contract
    classifier.go        receiver result classification
  messaging/
    kafka/               producer, consumer, envelopes, serialization
  repository/
    postgres/            concrete persistence implementation
  retry/                 retry policy and durable scheduler
  resilience/            rate, circuit, and concurrency controls
  observability/
  testutil/
```

The directory shape is secondary to the intended dependency direction:

```text
HTTP API -> application event publisher contract -> Kafka adapter
Kafka consumer -> delivery service
Retry poller -> delivery service
Replay operation -> delivery service
Delivery service -> consumer-owned persistence contracts
PostgreSQL repository -> implements those contracts
```

After the 2026-07-03 review, the candidate target should be treated as a long-term orientation, not
as the next implementation plan. The next useful shape is smaller:

```text
internal/
  kafka/                Kafka producer, consumer, message envelope, serialization
  delivery/             Delivery execution, webhook sender, signing, result classification
  retry/                Durable retry scheduler
```

Only move `internal/kafka` to `internal/messaging/kafka` if another broker adapter becomes real or
the package name becomes a repeated source of confusion after delivery has been extracted.

## Recommended Incremental Path

Do not reorganize every package at once. After the latest review, use this path:

1. Move webhook sending, signing, classification, and delivery orchestration into
   `internal/delivery` without changing behavior.
2. Make Kafka consumer and retry poller depend on the extracted service.
3. Keep Kafka producer/consumer package names stable during the extraction.
4. Replace `kafka.EventMessage` at the API boundary with an application-level envelope only if the
   extraction shows a stable shared contract.
5. Move repository interfaces toward consumers as packages are touched; do not perform a repository-wide
   interface migration solely for directory symmetry.
6. Remove `internal/clock` only if it still has no production caller after the extraction and no test
   package needs it.

Each move should preserve package tests and use compile-time interface satisfaction as the primary
dependency check. Avoid combining this extraction with replay state-model changes in one commit.

## Non-goals

- No framework, dependency-injection container, or generic service layer.
- No feature-based directory rewrite of every package.
- No change to Kafka, retry, persistence, lease, signature, or at-least-once semantics.
- No extraction justified only by aesthetic symmetry.
- No new abstraction unless it has at least two real consumers or removes an existing ownership
  contradiction.

## Promotion Criteria

Promote this spike into an ADR and execution-plan phase only if:

- replay would otherwise depend directly on `kafka.DeliveryHandler`;
- the legacy aggregate runtime policy is already decided;
- the extraction boundary can be covered by existing Kafka, retry, PostgreSQL, and E2E tests;
- the proposed application event envelope has a concrete consumer beyond renaming;
- the work can be split from replay behavior so structural and state-model regressions are isolated.

## Open Questions

1. Should `delivery` own retry classification policy, or should `retry` expose it to delivery?
2. Should the Kafka envelope be a dedicated adapter type or encode an application event type?
3. Which repository interfaces genuinely have multiple consumers and should remain shared?
4. Does replay call the same complete delivery service or a narrower claim/reset operation followed
   by normal retry scheduling?
5. Which legacy event-level methods and schema columns remain necessary after the upgrade policy is
   decided?
