# Current Limitations and Opportunities

> This document describes verified limitations and possible responses. It does not define
> the product, commit roadmap work, or authorize implementation. See
> [product.md](product.md) for the current product and [next-steps.md](next-steps.md) for
> strategic sequencing.

## Product limitations

| Limitation | Current impact | Possible direction |
|------------|----------------|--------------------|
| Single trust domain | No authentication, authorization, tenant isolation, quotas, or customer audit boundary | Keep separate deployments or investigate the [multi-tenancy spike](spikes/multi-tenancy.md) |
| Replay has no application authorization | Failed deliveries can be replayed, but any caller inside the API trust boundary can trigger it | Add authenticated operator identity only if the product moves beyond one trusted organization |
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

### Delivery is still at least once

Dispatch now retries per delivery and skips destinations that already reached a terminal or
successful state. This improves recovery precision, but it still does not provide exactly-once
HTTP delivery. A worker can call a receiver and fail before committing the outcome transaction;
after lease expiry, that same delivery can be attempted again.

### Attempt history can be incomplete

If an HTTP call occurs and its PostgreSQL transaction does not commit, that call cannot
be guaranteed to appear in attempt history. Redelivery may then create another call and
a committed attempt.

### Retry throughput remains bounded by downstream capacity

The scheduler now drains full batches immediately with explicit per-worker batch
concurrency and backlog metrics. Increasing that concurrency cannot bypass PostgreSQL pool
capacity, destination max-delivery-rate checks, HTTP latency, Kafka partitions, or worker resources.
Operators must tune from observed backlog age rather than treating a larger value as
universally faster.

## Policy limitations

### Destination protection limitations

- `max_delivery_rate` is distributed only when `REDIS_URL` is configured.
- `max_delivery_rate` is scoped to a subscription record, not a normalized destination URL, host, or
  external server.
- Duplicate subscriptions pointing to the same URL have independent limiter budgets.
- If `REDIS_URL` is absent, workers use local enforcement and the value is not globally coordinated.
- If Redis is configured but unavailable, delivery decisions fail closed as `throttled` and backlog
  can grow until Redis recovers.
- There is no separate burst-size, concurrency-limit, circuit-breaker, or distributed semaphore
  contract in v1.

Stronger algorithms such as distributed token bucket remain post-v1 spikes unless measurements show
the sliding-window limiter is insufficient.

## Security limitations

| Area | Current state |
|------|---------------|
| API access | No authentication or authorization |
| Tenant isolation | None |
| Signed-request replay | Timestamp validation reduces exposure, but there is no nonce store; receivers must also deduplicate event IDs |
| Secret storage | Subscription secrets are stored without application-level encryption |
| TLS | Deployment responsibility |
| API abuse protection | No inbound API rate limiting |
| Audit identity | Operations are not attributed to authenticated actors |

Signed webhooks use the ADR 020 HMAC-SHA256 contract. This authenticates request bytes but does
not provide exactly-once delivery, API access control, or application-level secret encryption.

## Operational limitations

- PostgreSQL is the only durable query and retry-state store; availability and backups
  depend on the operator's deployment.
- Kafka is required for initial event acceptance and processing.
- Kafka readiness checks broker/topic metadata, not a guaranteed successful next publish or fetch.
- Redis is required for distributed max-delivery-rate enforcement across multiple workers.
- Retention deletes terminal history rather than archiving it; legal hold and archival are unsupported.
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
- prefer strengthening per-destination delivery semantics over adding unrelated product breadth.

## Opportunity evaluation

When considering a limitation, record:

1. the user affected and frequency of the problem;
2. the consequence of leaving it unresolved;
3. the smallest acceptable product behavior;
4. state, security, and operational obligations introduced by the solution;
5. evidence that will validate the benefit.

Uncertain architectural responses belong in `docs/spikes/`. Accepted technical decisions
belong in ADRs. Executable work belongs in a promoted exec plan.
