# Internal Package Boundaries and Delivery Ownership

> **Status:** Concluded for v0.13; extraction deferred
> **Earliest decision point:** v0.13.0 replay design, after the legacy aggregate runtime policy is resolved
> **Scope:** Package ownership and dependency direction; no behavior or product change is accepted here

## Question

Does the current `internal/` package structure still represent the system's real ownership model,
or should delivery execution move out of `internal/kafka` before replay becomes a third caller?

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

## Recommended Incremental Path

Do not reorganize every package at once. If v0.13 confirms replay as a third delivery caller:

1. Resolve and remove or explicitly support the legacy aggregate runtime path.
2. Move webhook sending, signing, classification, and delivery orchestration into
   `internal/delivery` without changing behavior.
3. Make Kafka consumer and retry poller depend on the extracted service.
4. Introduce replay against the same delivery service rather than `kafka.DeliveryHandler`.
5. Replace `kafka.EventMessage` at the API boundary with an application-level envelope only if the
   extraction shows a stable shared contract.
6. Move repository interfaces toward consumers as packages are touched; do not perform a repository-wide
   interface migration solely for directory symmetry.
7. Remove `internal/clock` if it still has no production caller after the extraction.

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
