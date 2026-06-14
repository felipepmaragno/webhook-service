# Spike Proposal: End-to-End Multi-Tenancy

> **Status:** Proposed
> **Recorded:** 2026-06-14
> **Earliest evaluation:** after v1, only if Dispatch is intentionally continued beyond the accepted single-trust-domain product
> **Estimated implementation:** 5-8 focused sessions for a credible baseline

## Why Dispatch is currently single-tenant

Dispatch has no trusted tenant identity or ownership boundary. All callers and workers operate
inside one global namespace:

- Business API endpoints have no authentication or authorization.
- Events and subscriptions do not contain `tenant_id`.
- Event and subscription IDs are globally unique primary keys.
- Event lookup and subscription listing are not ownership-scoped.
- Workers match every event type against globally visible subscriptions.
- Kafka messages and partition keys carry no tenant identity.
- Redis rate-limit, circuit-breaker, and semaphore keys use only subscription ID.
- Retry claims, delivery attempts, logs, and operational limits have no tenant boundary.

The event `source` field is caller-controlled metadata. It is not authenticated and must not be
treated as tenant identity.

Deploying one Dispatch stack per customer is the current isolation workaround. Sharing one stack
between mutually untrusted customers would risk cross-customer reads, deletes, deliveries, and
resilience-state interference.

## Hypothesis

A shared-schema, tenant-scoped architecture can let one Dispatch deployment serve multiple customers
or internal teams safely while retaining shared Kafka, PostgreSQL, Redis, and worker infrastructure.

Multi-tenancy must be an end-to-end isolation contract. Adding a nullable `tenant_id` column without
authentication, scoped repositories, Kafka propagation, and Redis key isolation would create the
appearance of safety without providing it.

## Expected benefits

- Safely share infrastructure and operational tooling across customers or teams.
- Reduce duplicated deployments and idle capacity.
- Support tenant-specific ingestion quotas, subscription policies, and feature plans.
- Isolate suspension, retention, replay, and administrative actions by tenant.
- Enable usage accounting and future billing.
- Provide tenant-owned audit trails and operational investigations.
- Create a path toward premium or dedicated capacity for selected tenants.

## Required system changes

### Trusted identity and authorization

- Authenticate business API requests through an API key, JWT, mTLS identity, or another explicit mechanism.
- Resolve the authenticated principal to a tenant in middleware and propagate it through context.
- Never accept authoritative tenant identity from request JSON or arbitrary headers alone.
- Define tenant-admin and system-admin capabilities separately from normal tenant operations.
- Keep health/readiness and metrics authorization as an operational concern distinct from tenant APIs.

### Domain and API contracts

- Add required tenant ownership to events and subscriptions.
- Make event status, attempts, subscription listing, creation, and deletion tenant-scoped.
- Decide whether public IDs remain caller-selected and tenant-local or become globally generated IDs.
- Define behavior for cross-tenant ID collisions and unauthorized lookups without leaking resource existence.

### PostgreSQL isolation

- Add non-null `tenant_id` to events and subscriptions, with a migration assigning existing data to a default tenant.
- Prefer composite ownership constraints such as `UNIQUE (tenant_id, id)`.
- Make every repository operation require tenant identity explicitly.
- Add tenant-leading indexes for lookup, retry scheduling, and subscription matching access paths.
- Decide whether delivery attempts inherit tenant ownership only through events or also store `tenant_id` for indexing and defense in depth.
- Evaluate PostgreSQL Row-Level Security as an additional boundary, not a substitute for explicit application scoping.

One missing tenant predicate can become a data-security incident, so negative isolation tests are as
important as normal happy-path tests.

### Kafka propagation and partitioning

- Include immutable tenant identity in every Kafka event schema and compatibility policy.
- Include tenant identity in message keys, for example `tenant_id:event_id`, unless a later ordering contract requires another key.
- Ensure retries and future outcome topics preserve the same tenant identity.
- Prevent workers from matching an event against subscriptions from another tenant.
- Define safe behavior for malformed or legacy messages that lack tenant identity during rollout.

### Redis and resilience controls

- Namespace every distributed key by tenant and subscription:

```text
ratelimit:{tenant_id}:{subscription_id}
circuit:{tenant_id}:{subscription_id}
semaphore:{tenant_id}:{subscription_id}
```

- Preserve per-subscription controls while introducing optional tenant-wide quotas separately.
- Ensure the v0.9.0 rate policy model does not assume subscription scope is the only future control level.
- Define tenant-level ingestion, delivery-rate, concurrency, and backlog protections to limit noisy neighbors.

### Retry, leases, and outcomes

- Carry tenant identity through claims, retries, fenced outcomes, and administrative replay operations.
- Ensure claim queries and outcome updates cannot cross tenant ownership.
- Define how tenant suspension affects already queued Kafka events and leased retries.
- Scope dead-letter and replay behavior before exposing tenant administration.

### Observability and usage

- Add tenant identity to structured logs and traces where access controls permit it.
- Avoid unrestricted `tenant_id` Prometheus labels because tenant count can create high cardinality.
- Use bounded labels for plan/tier, logs and traces for detailed diagnosis, and database or event-stream aggregation for usage accounting.
- Define tenant-aware audit records for security-sensitive mutations.

## Isolation models considered

### Shared database and shared schema

Every owned row includes `tenant_id`.

**Benefits:** simplest provisioning, efficient resource sharing, easiest migration from the current model.

**Risks:** a missed predicate can leak data; requires disciplined interfaces, tests, and possibly RLS defense in depth.

This is the recommended starting model for Dispatch.

### Schema per tenant

**Benefits:** stronger database namespace separation.

**Costs:** migrations, connection routing, pooling, and tenant provisioning become substantially harder.

### Database or deployment per tenant

**Benefits:** strongest operational and data isolation; independent backup and scaling.

**Costs:** expensive infrastructure, duplicated upgrades, fragmented observability, and poor utilization for many small tenants.

This remains useful for customers requiring dedicated isolation, but should not be the default shared-service model.

## Proposed implementation sequence

If selected, split implementation into bounded exec plans:

1. Define tenant identity, authentication, authorization, ID semantics, and migration policy in an ADR.
2. Add tenant domain fields, default-tenant migration, composite constraints, and repository isolation tests.
3. Make API handlers and repository interfaces tenant-explicit; add cross-tenant negative tests.
4. Propagate tenant identity through Kafka, workers, retries, leases, attempts, and Redis keys.
5. Add tenant-level quotas, noisy-neighbor protection, observability, and administrative controls.
6. Perform security review and E2E isolation validation before claiming multi-tenant readiness.

Do not ship a partially tenant-aware system to mutually untrusted users. During migration, either all
business paths enforce tenant isolation or the deployment remains explicitly single-tenant.

## Research questions

1. Which authentication mechanism fits the intended users: API keys, JWT/OIDC, mTLS, or gateway-provided identity?
2. Are tenants external customers, internal teams, environments, or projects, and can principals belong to multiple tenants?
3. Should IDs be globally unique or only unique within a tenant?
4. Is application-enforced scoping sufficient, or is PostgreSQL RLS required for defense in depth?
5. What tenant-wide quotas are required at ingestion, delivery, retry backlog, storage, and retention layers?
6. How should queued work behave when a tenant is suspended or deleted?
7. What usage data is needed for billing, and what consistency/retention guarantees does it require?
8. Which metrics can safely include tenant dimensions without excessive cardinality or information exposure?
9. Are any customers expected to require dedicated databases, Kafka topics, encryption keys, or deployments?
10. How will existing single-tenant installations migrate and manage their default tenant credentials?

## Evaluation criteria

Recommend implementation only after the proposal demonstrates:

- trusted tenant derivation for every business request
- no unscoped repository or worker path
- deterministic cross-tenant isolation tests for reads, writes, deletes, delivery, retry, and Redis state
- backward-safe migration for existing data and Kafka messages
- bounded noisy-neighbor behavior
- an observability design that supports operations without unsafe cardinality
- a clear operational model for tenant provisioning, suspension, deletion, and incident response

## Current recommendation

Keep Dispatch explicitly single-tenant through v1. The rate-control normalization should
use policy abstractions that can later support tenant-wide quotas, but it must not introduce partial
tenant semantics.

After reliability and rate-control work, investigate shared-schema logical isolation as a dedicated
multi-increment program. Multi-tenancy has meaningful product and cost benefits, but its primary
requirement is security isolation rather than adding a field to existing tables.

## Related context

- [Next steps](../next-steps.md)
- [Rate-control normalization plan](../exec-plans/active/v0.9.0.md)
- [Distributed token bucket spike](distributed-token-bucket.md)
- [Kafka package context](../../internal/kafka/README.md)
- [PostgreSQL package context](../../internal/repository/postgres/README.md)
- [Resilience package context](../../internal/resilience/README.md)
