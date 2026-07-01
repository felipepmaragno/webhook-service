# Dispatch v1 Roadmap

> **Status:** Accepted product roadmap
> **Product target:** Production-conscious, self-hosted webhook delivery for one trusted organization
> **Release rule:** V1 is complete when the release gate passes, not when every possible feature is implemented.

## Purpose

This roadmap converts the [product definition](product.md) into a finite engineering
sequence. Every required increment closes a named gap in the v1 promise. Work outside
this sequence must either fix a release-blocking defect or remain deferred.

## Scope rules

1. Reliability and understandable recovery are the product value.
2. Per-destination delivery state is the core domain model for v1.
3. Self-hosted deployment inside one trust boundary is accepted; application-level
   multi-tenancy is not required.
4. External infrastructure and algorithms are means, not milestones.
5. A new feature must map to a v1 completion criterion before it can become active work.
6. V1 ends the feature-building phase of this project.

## Current foundation

Already completed:

- asynchronous Kafka ingestion and worker separation;
- atomic event-outcome and attempt persistence before Kafka offset commit;
- owner-fenced retry leases with crash recovery;
- Redis-backed per-destination max-delivery-rate enforcement, with local mode for development;
- metrics, dashboards, health checks, structured logs, and trace propagation;
- layered unit, integration, and thin end-to-end CI validation.

These mechanisms provide a strong base. The v0.11 delivery cutover corrected the largest
state-model gap by making delivery rows the runtime ownership unit for new work.

## Required sequence

| Increment | Outcome | Estimated effort | V1 criterion closed |
|-----------|---------|------------------|---------------------|
| v0.8.0 | Bounded retry draining and backlog/lease observability | Completed | Predictable recovery and observable backlog |
| v0.9.0 | Normalize destination-protection terminology | Completed | Explicit and testable destination-protection contract |
| pre-v0.14 | Simplify destination protection to one max-delivery-rate knob | Completed | Keep v1 smaller before operational readiness |
| pre-v0.14b | Restore Redis distributed max-delivery-rate enforcement | Completed | Make the remaining destination-protection knob globally meaningful |
| v0.10.0 | Add per-subscription delivery identity and durable data model | Completed | Every destination and attempt has stable identity |
| v0.11.0 | Cut processing, retry, aggregation, and query behavior over to per-delivery state | Completed | Independent destination outcomes and recovery |
| v0.12.0 | Cryptographic webhook signatures and deployment security contract | Completed | Receiver authenticity and explicit API trust boundary |
| v0.13.0 | Terminal-delivery replay, retention, and cleanup | Completed | Supported recovery workflow and bounded storage lifecycle |
| v0.14.0 | Minimal operational readiness and capacity smoke | Completed | Install, validate, inspect, and roughly size the system |
| v1.0.0 | Release hardening, compatibility review, and final validation | 1-2 sessions | All release gates demonstrated and documented |

Estimated remaining effort: **5-8 focused engineering sessions** for v0.13.0 through v1.0.0.

## Increment boundaries

### v0.8.0: Retry operations

Required because self-hosted operators need to know whether retry backlog is recovering
and need bounded controls that do not turn polling frequency into accidental capacity.

Exit evidence:

- deterministic bounded-concurrency tests;
- backlog age and claim-health metrics;
- measured drain behavior and documented capacity controls;
- safe shutdown with in-flight retry work.

### v0.9.0: Destination protection contract

Completed to remove the contradiction between rate and concurrency semantics and to make
pre-HTTP backpressure persist as `throttled`. A later pre-v0.14 simplification intentionally
shrinks the v1 contract to one max-delivery-rate value.

Exit evidence:

- one API, storage, and delivery policy field: `max_delivery_rate`;
- explicit pre-HTTP throttling without consuming delivery attempts;
- consistent policy terminology across API, storage, delivery, and docs.

Redis-backed coordination is restored only for `max_delivery_rate`. Circuit breakers, distributed
semaphores, and independent burst or concurrency subscription controls are not v1 requirements.

### v0.10.0: Per-destination persistence foundation

Completed stable delivery identity without yet changing every runtime path. The durable
model must represent one event targeting zero or more subscriptions, with attempts tied
to the exact delivery.

Accepted modeling decisions:

- the destination set is frozen when an event is first initialized by a worker;
- later subscription changes do not silently add or remove deliveries for that event;
- a delivery identity is unique for the event and subscription pair;
- event-level status is a projection of delivery states, not the retry ownership record;
- every attempt is attributed to the delivery and subscription that produced it;
- event-level state is a projection and never a second retry ownership path.

Exit evidence:

- schema, domain, repository, migration, and API representation tests;
- attempt rows identify a delivery/subscription;
- aggregate projection rules are specified;
- existing runtime behavior remains stable until cutover.

### v0.11.0: Per-destination processing cutover

Initialize durable target deliveries before external calls, process and retry deliveries
independently, and derive event status from their outcomes.

Exit evidence:

- duplicate Kafka processing does not recreate the destination set or redeliver already
  terminal/successful deliveries merely because another destination needs retry;
- each retry claim owns one delivery with fencing and crash recovery;
- mixed success, retry, throttle, and terminal outcomes remain independently visible;
- event and delivery query APIs expose the projection clearly;
- no event-level retry ownership path remains.

### v0.12.0: Receiver and deployment security

Completed timestamped HMAC-SHA256 over exact request bytes, versioned headers,
constant-time receiver verification guidance, rotation behavior, and test vectors.

For v1, API access control is a deployment responsibility. The supported model is a
trusted private network or an authenticating reverse proxy/API gateway. The deployment
contract and examples now preserve that boundary explicitly.

### v0.13.0: Replay and retention

Implemented an operator API to replay failed deliveries. Replay creates a new explicit processing
generation for the same delivery identity without rewriting history or resetting successful
destinations.

Retention redacts attempt bodies and deletes terminal event history in bounded, observable batches
while preserving rows needed by active retries.

### v0.14.0: Operational readiness

Document and validate:

- installation and configuration;
- migration and rollback constraints;
- backups and restore exercise;
- alerts and incident runbooks;
- Kafka/PostgreSQL outage behavior;
- measured ingestion, delivery, and retry capacity for a recorded environment;
- supported scaling boundaries and resource settings.

### v1.0.0: Release increment

No new product feature is allowed in this increment. It exists to remove release blockers,
verify compatibility, run the complete validation matrix, and publish coherent v1 docs.

## V1 release gate

### Product behavior

- [ ] A submitted event has a stable destination set.
- [ ] Every destination has independent status and attempt history.
- [ ] Temporary, throttled, successful, and terminal outcomes are distinguishable.
- [x] Terminal deliveries can be replayed without database edits.
- [ ] Duplicate and ordering semantics are explicit and mechanically tested.

### Reliability

- [ ] Kafka, persistence, retry lease, and shutdown failure paths have automated coverage.
- [ ] No known failure path silently discards retryable work.
- [ ] Backlog age, stale claims, persistence failures, and terminal outcomes are observable.
- [ ] Recovery procedures exist for Kafka, PostgreSQL, and worker interruption.

### Security

- [x] Webhooks use verified HMAC-SHA256 signatures with published test vectors.
- [x] API trust assumptions and reverse-proxy/private-network requirements are explicit.
- [x] Secrets, logs, metrics, and example manifests do not expose known critical data.

### Operations

- [x] Fresh installation and migrations are validated.
- [ ] Upgrade from the pre-v1 schema is validated.
- [ ] Backup and restore procedure has been exercised.
- [x] Retention and cleanup are configured and tested.
- [x] Capacity guidance records environment and methodology rather than universal claims.

### Engineering quality

- [ ] Lint, build, race-gated unit/component tests, integration tests, and E2E tests pass.
- [ ] Migrations and critical failure paths have focused tests.
- [ ] Product, spec, architecture, ADRs, package READMEs, runbooks, and README agree.
- [ ] No open high-severity defect contradicts the v1 promise.

## Feature freeze and change control

From acceptance of this roadmap until v1.0.0:

- required roadmap work may become an exec plan;
- defects may enter the active increment when they threaten its acceptance criteria;
- unrelated refactors require direct evidence that they reduce v1 delivery risk;
- all other features and architectural ideas remain spikes or post-v1 notes;
- completing v1 ends planned feature development for this project.

## Explicitly deferred

- multi-tenancy and managed-service capabilities;
- UI, billing, quotas, customer portal, and customer support features;
- payload transformation, ordering, batch delivery, and multi-region operation;
- Kafka outcome-topic architecture;
- richer destination-protection algorithms unless measurements prove the simplified limiter cannot
  satisfy the accepted contract;
- speculative high-throughput storage or queue redesign.

## Roadmap maintenance

The roadmap may shrink when a simpler solution satisfies a release criterion. Expanding
it requires an explicit product decision and a corresponding change to `product.md`.
Effort estimates may change; the v1 promise and release gate must not drift silently.
