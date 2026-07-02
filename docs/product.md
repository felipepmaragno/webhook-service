# Dispatch Product Definition

> **Authority:** This document is the source of truth for what Dispatch is, who it
> serves, which problem it solves, and what it promises today. Precise observable
> behavior belongs in [spec.md](spec.md). Technical mechanisms belong in
> [architecture.md](architecture.md) and the ADRs.
>
> **Current-state rule:** Unimplemented ideas are not product capabilities. Open
> decisions are identified explicitly instead of being mixed into the current product.

## Product summary

Dispatch is a self-hosted service that accepts events and asynchronously sends them as
HTTP webhooks to registered destinations. It centralizes delivery retries, failure
handling, destination protection, and operational visibility so producer services do
not each need to implement those concerns.

The product's current core promise is:

> Accept an event, route it to matching webhook destinations, keep retryable failures
> recoverable, and expose the resulting delivery state.

Dispatch currently fits best as infrastructure operated by one engineering or platform
team inside a single trust boundary. It is not currently a public webhook SaaS or a
multi-tenant customer platform.

## Problem

A service that sends webhooks directly must handle receiver outages, timeouts, retry
timing, duplicate delivery, traffic spikes, destination overload, auditing, and
observability. Reimplementing those concerns in every producer creates inconsistent
behavior and makes failures difficult to operate.

Dispatch provides one delivery path with shared rules and visibility. Producer services
submit an event and remain decoupled from the availability and latency of webhook
receivers.

## Current users and actors

The implementation supports three practical actors:

| Actor | Job |
|-------|-----|
| Producer service | Submit an event without waiting for receiver delivery |
| Platform operator | Register destinations, operate Dispatch, and investigate failures |
| Webhook receiver | Accept an HTTP event and deduplicate repeated deliveries when necessary |

There is no authenticated end-user or tenant identity. All API callers share the same
subscriptions, event namespace, storage, quotas, and operational controls.

## Best-fit use case today

Dispatch is currently a reasonable fit when:

- one organization or trusted environment owns producers, subscriptions, and operations;
- asynchronous at-least-once webhook delivery is acceptable;
- receivers can use the event ID to deduplicate repeated calls;
- the team is willing to operate PostgreSQL, Kafka, and Redis for distributed destination
  rate limiting;
- an API and metrics are sufficient, without a UI, replay workflow, or customer portal.

It is not currently a good fit when:

- unrelated customers require security and data isolation;
- exactly-once delivery or strict ordering is required;
- non-technical users need self-service subscription management;
- managed key custody or application-level secret encryption is mandatory;
- the team wants a managed service with contractual availability and support.

## Core concepts

### Event

An event has a caller-provided ID, type, source, and JSON data. The ID is the stable
identity exposed to receivers and status queries. Event type selects matching
subscriptions.

### Subscription

A subscription is an active destination URL plus one or more event-type filters. Filters
support exact values, `*`, and prefix patterns such as `order.*`.

Deleting a subscription deactivates it. Dispatch does not currently verify destination
ownership or reachability before activation.

### Delivery cycle

A delivery cycle initializes one event against all currently matching subscriptions and
freezes that destination set into delivery rows. It may make zero, one, or several HTTP calls.
The event stores a projected summary, while delivery rows own per-destination runtime state.

### Delivery

A delivery is the durable event/subscription unit introduced for v1. It records a frozen
destination target and owns retry, lease, status, and attempt attribution for current runtime
processing.

### Delivery attempt

An attempt records an HTTP call, including status, response excerpt, error, and duration.
Every attempt identifies the event, delivery, subscription, and replay generation that produced it.

## User journey

1. An operator registers one or more webhook subscriptions.
2. A producer submits an event to the HTTP API.
3. Dispatch returns `202 Accepted` after Kafka accepts the event.
4. A worker finds every active subscription matching the event type.
5. Dispatch applies destination resilience controls and sends HTTP POST requests.
6. Dispatch persists per-delivery outcomes and generated attempt history.
7. Retryable deliveries are scheduled in PostgreSQL and later reclaimed by a retry worker.
8. An operator can query the event and its recorded attempts.

The event may not be queryable immediately after step 3 because its PostgreSQL record is
created after worker processing, not during API acceptance.

## Current capabilities

- HTTP event ingestion with asynchronous acceptance
- Subscription creation, listing, and deactivation
- Exact, global, and prefix-wildcard event-type matching
- Fan-out to all matching active subscriptions
- Retry of selected transient HTTP and network failures with exponential backoff and jitter
- Crash-recoverable retry claims with owner and expiration fencing
- Per-subscription destination max-delivery-rate guardrail
- Atomic persistence of an event outcome with its recorded attempts
- Kafka offset commit only after outcome persistence succeeds
- Event status, delivery-attempt, and per-subscription delivery queries
- Health, readiness, Prometheus metrics, structured logs, and trace propagation
- Independent API and worker processes
- Automated unit, integration, and thin end-to-end validation

## Product guarantees

### Asynchronous acceptance

`202 Accepted` means the event was published to Kafka. It does not mean a webhook was
delivered or that a PostgreSQL status row already exists.

### At-least-once processing

Dispatch favors recovery over suppression of duplicate HTTP calls. A webhook can be
called more than once after worker failure, retry-lease expiration, Kafka redelivery, or
an HTTP success followed by a database failure.

Exactly-once delivery to an arbitrary HTTP receiver is not guaranteed. Receivers should
deduplicate using `X-Event-ID` when duplicates matter.

### Durable outcome boundary

For a processed Kafka batch, event outcomes and their attempt rows are committed before
Kafka offsets. A persistence failure leaves the batch uncommitted so it can be processed
again.

For retries, only the worker that still owns the exact claim may persist the result.
Expired claims can be recovered by another worker.

### No ordering guarantee

Concurrent workers and retries can deliver events out of order. Dispatch exposes no
ordering key or per-subscription FIFO contract.

### Receiver success contract

Any HTTP `2xx` response is success. Network errors, timeouts, and selected temporary
statuses are retryable while attempts remain. Other non-`2xx` responses are terminal.
The precise classification is defined in [spec.md](spec.md).

## Important current boundaries

### Single trust domain

The API has no authentication or authorization. There is no tenant identity, tenant data
isolation, per-tenant quota, or tenant-aware audit trail. Separate deployments are the
current isolation mechanism.

### One per-delivery runtime model

Delivery rows own processing, retries, leases, replay generations, and attempt attribution. Event
status is a query projection for operator convenience, not a second executable state machine.

### Event identity is not exactly-once delivery

The event ID prevents duplicate event rows. Repeated submissions or redelivery can still
perform duplicate HTTP calls and append attempt rows. Producers must not interpret event
identity as an exactly-once guarantee.

### Destination protection is intentionally narrow

Subscriptions expose one optional `max_delivery_rate` value. When Redis is configured, that value
is enforced across worker instances with a Redis sliding-window limiter. Pre-HTTP rate
backpressure becomes `throttled` instead of consuming delivery attempts. V1 intentionally does not
expose separate burst capacity, concurrent in-flight limits, circuit-breaker state, or distributed
semaphore behavior because those controls add more product and operational complexity than the
study goal needs.

### Security remains deployment-scoped

Signed subscriptions use timestamped HMAC-SHA256 over the exact webhook body. Subscription
secrets are write-only through the API but remain stored without application-level encryption.
API authentication, authorization, TLS, network access control, datastore transport security,
and backup protection depend on the deployment environment.

### Operability is engineering-facing

Dispatch exposes APIs, logs, metrics, dashboards, failed-delivery replay, and bounded retention,
but has no UI, dead-letter workflow, subscription verification, or end-user support model.

## Product maturity

Dispatch is a functional engineering system with strong automated coverage around its
most important recovery paths. V1 is intentionally production-conscious rather than a full managed
production platform: operators still own deployment security, datastore administration, backups,
upgrades, and incident response.

The architecture already carries meaningful operational cost: Kafka, PostgreSQL, and Redis. Further
scaling mechanisms should be added only for a measured bottleneck or a chosen product requirement.

## Accepted v1 direction

Dispatch v1 is a **production-conscious, self-hosted webhook delivery service for one
trusted organization**. It demonstrates reliable asynchronous processing and disciplined
software engineering without expanding into a commercial webhook platform.

The target user is an engineering or platform team that wants to centralize webhook
delivery inside infrastructure it operates. The team accepts at-least-once delivery and
owns Kafka, PostgreSQL, Redis, deployment security, backups, upgrades, and incident response.

### Product differentiator

The primary value is reliable and understandable recovery:

- accepted work is not silently lost after recoverable failures;
- each destination has an inspectable delivery outcome;
- temporary failures can be retried and terminal failures can be replayed deliberately;
- duplicate and ordering limitations are explicit;
- operators can observe backlog, failure, and recovery behavior.

Scale, Kafka, rate limiting, and service decomposition are
supporting mechanisms. They are not independent product goals. Simplicity constrains the
reliability design: new distributed machinery requires a chosen user need or measured
bottleneck.

### V1 user promise

A team can operate Dispatch to:

1. register webhook destinations;
2. submit events asynchronously;
3. deliver each event to every destination selected when the event is initialized;
4. retry temporary failures without silently losing recoverable work;
5. protect destinations with a distributed maximum delivery-rate limit when Redis is configured;
6. inspect the outcome and attempt history for each event-destination delivery;
7. replay terminal deliveries safely and deliberately;
8. let receivers verify webhook authenticity cryptographically;
9. observe backlog, failures, throttling, and recovery;
10. install, validate, inspect, and operate the system using documented procedures and explicit
    operational boundaries.

### V1 completion definition

V1 is complete only when:

- every initialized event-destination delivery reaches an inspectable `delivered`,
  retryable, throttled, processing, or terminal state;
- retryable work has no known silent-loss path;
- duplicate-delivery and ordering semantics are documented and covered by tests;
- delivery attempts identify their destination;
- operators can inspect and replay terminal deliveries without editing the database;
- webhook signatures use a documented cryptographic contract with test vectors;
- max-delivery-rate throttling behavior is explicit and tested;
- backlog age, terminal failures, stale claims, persistence failures, and rate-limit rejections
  are observable;
- retention, recovery, installation, and validation procedures are documented;
- unsupported pre-v1 upgrades, full backup/restore drills, and rollback automation are recorded as
  accepted limitations rather than hidden requirements;
- the complete CI pipeline passes and no unresolved high-severity defect contradicts the
  product promise.

The finite implementation sequence and evidence required for these conditions are in
[v1-roadmap.md](v1-roadmap.md).

### Explicit v1 non-goals

- multi-tenancy or isolation between unrelated customers;
- managed-service operation, billing, quotas, compliance, or customer support;
- customer-facing UI or portal;
- payload transformation or enrichment;
- strict ordering or exactly-once delivery;
- receiver batch delivery;
- multi-region operation;
- another outcome topic or storage system;
- richer destination-protection algorithms unless measurements show the current simplified
  implementation cannot satisfy the v1 contract;
- speculative throughput work beyond the measured v1 operating envelope.

These ideas may be preserved as spikes or post-v1 notes, but they do not delay v1.

## Decision guardrails

Before adding a major feature or infrastructure mechanism, answer:

1. Which current or committed user problem does it solve?
2. Is that problem part of the core delivery promise or a new product direction?
3. What simpler option exists for the expected workload?
4. Which new operational and state-consistency obligations does it create?
5. How will we know from tests or production signals that the change was valuable?

Ideas that cannot yet answer these questions belong in `docs/spikes/`, not in the product
contract or an implementation plan.

During the v1 feature freeze, new work is accepted only when it closes a named v1
completion criterion or fixes a defect that threatens one. Everything else is deferred.

## Related documents

- [System behavior specification](spec.md)
- [Architecture](architecture.md)
- [Known limitations and opportunities](limitations.md)
- [Strategic next steps](next-steps.md)
- [V1 roadmap and release gate](v1-roadmap.md)
- [Verified engineering state](../PROGRESS.md)
- [Architectural decisions](adr/)
- [Architectural spikes](spikes/)
