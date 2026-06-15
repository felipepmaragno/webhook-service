# Current Limitations and Opportunities

> This document describes verified limitations and possible responses. It does not define
> the product, commit roadmap work, or authorize implementation. See
> [product.md](product.md) for the current product and [next-steps.md](next-steps.md) for
> strategic sequencing.

## Product limitations

| Limitation | Current impact | Possible direction |
|------------|----------------|--------------------|
| Single trust domain | No authentication, authorization, tenant isolation, quotas, or customer audit boundary | Keep separate deployments or investigate the [multi-tenancy spike](spikes/multi-tenancy.md) |
| Aggregate fan-out state | One event state represents every destination; retries can repeat successful calls and attempts do not identify subscriptions | Introduce per-subscription delivery identity and state |
| No replay workflow | Terminal events remain queryable but cannot be safely replayed through a supported API | Define replay authorization, identity, and duplicate semantics before adding an endpoint |
| No ordering guarantee | Concurrent processing and retries can reorder events | Add an explicit ordering-key contract only for users that require it |
| No payload transformation | Every destination receives the same envelope | Keep producers responsible or add a constrained transformation model |
| No destination verification | A subscription is active immediately without proof of ownership or reachability | Add a challenge/verification lifecycle |
| No receiver batching | Every event is an individual HTTP request | Add opt-in batching only if receiver overhead is measured as a constraint |
| Limited query API | Status is queried by event ID; no filtering, pagination, or payload search | Add operational query use cases before choosing an indexing/search design |
| Engineering-only operation | No UI, customer portal, support model, or managed-service contract | Remain an infrastructure component or explicitly choose a broader product direction |

## Reliability limitations

### At-least-once delivery produces duplicates

Dispatch cannot atomically combine an external HTTP call with PostgreSQL persistence and
Kafka offset commit. A receiver can be called again after persistence failure, worker
failure, lease expiration, or Kafka redelivery.

Current protections:

- event outcome and attempt history commit in one PostgreSQL transaction;
- Kafka offsets commit only after that transaction succeeds;
- retry writes require the exact current owner and lease deadline;
- expired retry claims are recoverable;
- one event row is retained for a repeated event ID.

These protections prevent silent loss of retry state but do not provide exactly-once
HTTP delivery. Receivers should deduplicate by `X-Event-ID`.

### Fan-out recovery is coarse

The aggregate result uses `terminal failure > retryable outcome > success`. A retry
re-evaluates every active matching subscription, not only destinations that failed.
Attempt rows also lack `subscription_id`. This makes per-destination audit and recovery
the most important modeling limitation in the current delivery design.

### Attempt history can be incomplete

If an HTTP call occurs and its PostgreSQL transaction does not commit, that call cannot
be guaranteed to appear in attempt history. Redelivery may then create another call and
a committed attempt.

### Retry throughput remains bounded by downstream capacity

The scheduler now drains full batches immediately with explicit per-worker batch
concurrency and backlog metrics. Increasing that concurrency cannot bypass PostgreSQL
pool capacity, destination concurrency/rate limits, HTTP latency, or worker resources.
Operators must tune from observed backlog age rather than treating a larger value as
universally faster.

## Policy limitations

### Rate algorithm limitations

- Redis uses an exact sliding-window log and applies each subscription's `rate_limit`.
- `burst_size` is part of the policy contract, but the current Redis sliding-window path
  does not provide independent token-bucket-style burst semantics.
- The in-memory fallback uses a local token bucket with `rate_limit` and `burst_size`.
- Redis failure degrades global rate control into independent per-worker controls; the
  effective system-wide rate can temporarily exceed the configured limit by roughly the
  number of active workers.

Token bucket remains an optional spike, not required v1 behavior unless measurements show
the normalized sliding-window implementation is insufficient.

## Security limitations

| Area | Current state |
|------|---------------|
| API access | No authentication or authorization |
| Tenant isolation | None |
| Webhook signature | `X-Signature` is a non-cryptographic placeholder |
| Secret storage | Subscription secrets are stored without application-level encryption |
| TLS | Deployment responsibility |
| API abuse protection | No inbound API rate limiting |
| Audit identity | Operations are not attributed to authenticated actors |

The signature placeholder must not be used as receiver authentication. A production
signature contract requires `crypto/hmac`, SHA-256 test vectors, rotation semantics, and
a compatibility decision.

## Operational limitations

- PostgreSQL is the only durable query and retry-state store; availability and backups
  depend on the operator's deployment.
- Kafka is required for initial event acceptance and processing.
- Redis is optional, but its absence weakens multi-worker coordination guarantees.
- There is no supported archival or retention policy for events and attempts.
- Capacity numbers from the pre-Kafka architecture are not current product guarantees.
- Consumer-group rebalancing is not covered by the thin end-to-end test harness.
- The compose demo flow is manually, not continuously, validated.

See [PERFORMANCE.md](PERFORMANCE.md) for historical measurements. Treat them as evidence
about a particular environment, not as an SLO.

## Complexity boundary

The current system already requires several distributed components. Before adding a
database, queue, cache, coordination mechanism, or service boundary, require evidence
that it solves a chosen user problem or measured operational constraint.

In particular:

- do not add multi-tenancy as a collection of schema fields; it is a security program;
- do not add another Kafka topic unless outcome streaming or database decoupling has a
  demonstrated consumer and failure model;
- do not optimize for speculative throughput before measuring the current bottleneck;
- prefer strengthening per-destination delivery semantics over adding breadth that
  depends on the current aggregate model.

## Opportunity evaluation

When considering a limitation, record:

1. the user affected and frequency of the problem;
2. the consequence of leaving it unresolved;
3. the smallest acceptable product behavior;
4. state, security, and operational obligations introduced by the solution;
5. evidence that will validate the benefit.

Uncertain architectural responses belong in `docs/spikes/`. Accepted technical decisions
belong in ADRs. Executable work belongs in a promoted exec plan.
