# Dispatch System Behavior Specification

> **Authority:** This document defines externally observable behavior and system
> invariants. Product purpose and positioning belong in [product.md](product.md).
> Implementation structure belongs in [architecture.md](architecture.md), ADRs, and
> critical package READMEs.

## Scope

Dispatch accepts events, matches them to active webhook subscriptions, delivers them
asynchronously, records aggregate outcomes, and schedules retryable failures.

This specification describes current behavior. It is not a roadmap, test plan, database
schema, or deployment guide.

## HTTP API

| Method | Path | Behavior |
|--------|------|----------|
| `POST` | `/events` | Validate and publish an event to Kafka; return `202` after publish succeeds |
| `GET` | `/events/{id}` | Return the persisted aggregate event state or `404` |
| `GET` | `/events/{id}/attempts` | Return recorded HTTP attempts for the event |
| `GET` | `/events/{id}/deliveries` | Return per-subscription delivery rows for the event; legacy aggregate events may return an empty list |
| `POST` | `/subscriptions` | Create an active webhook subscription |
| `GET` | `/subscriptions` | List active subscriptions |
| `DELETE` | `/subscriptions/{id}` | Deactivate a subscription |
| `GET` | `/health` | Process health |
| `GET` | `/ready` | Dependency readiness |
| `GET` | `/metrics` | Prometheus metrics |

The API currently has no authentication, authorization, tenant identity, or API-level
rate limit.

### Event submission

Request:

```json
{
  "id": "evt_abc123",
  "type": "order.created",
  "source": "billing-service",
  "data": {
    "order_id": "12345",
    "amount": 99.90
  }
}
```

`id`, `type`, and `source` must be non-empty. The API assigns `max_attempts = 5`.

Successful response:

```json
{
  "id": "evt_abc123",
  "status": "pending",
  "created_at": "2026-06-14T12:00:00Z"
}
```

The response means Kafka accepted the message. The returned timestamp and `pending`
status are acceptance metadata; no PostgreSQL event row is created by the API. A status
query can therefore return `404` until a worker persists the first outcome.

Publishing the same ID more than once is accepted. PostgreSQL retains one event row, but
each consumed message may already have caused HTTP calls and may add attempt history.

### Subscription creation

Request:

```json
{
  "id": "sub_abc123",
  "url": "https://receiver.example/webhooks",
  "event_types": ["order.*"],
  "secret": "optional-secret",
  "rate_limit": 100,
  "burst_size": 10,
  "concurrency_limit": 100
}
```

`id`, `url`, and at least one `event_types` value are required. A missing or non-positive
`rate_limit`, `burst_size`, or `concurrency_limit` is stored with its default. `rate_limit`
means sustained requests per second, `burst_size` means the local fallback token bucket burst
capacity, and `concurrency_limit` means simultaneous HTTP calls allowed for the subscription.
Creation does not verify URL ownership, reachability, or TLS policy. Duplicate IDs fail creation.

Deletion sets the subscription inactive. Only active subscriptions are listed or used
for new delivery cycles.

## Subscription matching

For each event type, an active subscription matches when any configured filter is:

- exactly equal to the event type;
- `*`, which matches every type; or
- a suffix-wildcard prefix such as `order.*`.

Matching is case-sensitive. More general glob syntax is not supported.

The worker loads matching subscriptions when each delivery cycle starts. A retry can
therefore observe subscription changes made after the previous cycle.

## Webhook request

Dispatch sends one HTTP `POST` to each matching subscription:

```http
POST {subscription.url}
Content-Type: application/json
X-Event-ID: evt_abc123
X-Event-Type: order.created
X-Trace-ID: <trace-id when present>
X-Signature: sha256=<placeholder when a secret is configured>

{
  "id": "evt_abc123",
  "type": "order.created",
  "source": "billing-service",
  "data": {
    "order_id": "12345"
  }
}
```

The default HTTP timeout is 10 seconds. At most 1024 bytes of the response body are
retained in attempt history.

`X-Signature` is not cryptographic HMAC and MUST NOT be used to authenticate requests.

If no active subscription matches, the event is considered successfully delivered with
no HTTP attempt.

## Result classification

| Receiver result | Classification |
|-----------------|----------------|
| Any `2xx` | Success |
| `408`, `429`, `500`, `502`, `503`, `504` | Retryable while attempts remain |
| Network, DNS, TCP, TLS, or timeout error | Retryable while attempts remain |
| `400`, `401`, `403`, `404`, `405`, `406`, `410`, `411`, `413`, `414`, `415`, `422`, `426`, `431` | Terminal failure |
| Any other non-`2xx` | Terminal failure |

Rate-limiter, circuit-breaker, semaphore, and local semaphore cancellation rejection
produce a `throttled` outcome without an HTTP attempt. Throttling schedules another cycle
without incrementing delivery attempts. Subscription-load failure still produces a retryable
outcome because the worker could not evaluate the matching destinations.

## Fan-out and aggregate outcome

An event is delivered to every active matching subscription. Dispatch stores one event
state for the complete cycle rather than one state per subscription.

The aggregate rule is:

```text
terminal failure > retryable HTTP outcome > throttled outcome > success
```

Consequences:

- one terminal destination failure makes the event terminal even if another destination
  had a retryable failure;
- a retry repeats matching for the whole event and can call destinations that succeeded
  previously;
- current runtime attempt rows may not identify the subscription, so multiple calls in one
  aggregate cycle are not always uniquely attributable through the attempts API.

## Per-subscription delivery records

Dispatch has a durable per-subscription delivery model used by the v0.10 compatibility path.
Each delivery is identified by a stable event/subscription pair and snapshots the destination URL,
secret, and rate-control policy needed for deterministic future processing.

Current v0.10 behavior:

- delivery rows can be initialized and read through repository/API paths;
- `GET /events/{id}/deliveries` returns initialized delivery rows or an empty list for legacy
  aggregate-only events;
- new delivery-attributed attempts can store `delivery_id` and `subscription_id`;
- existing aggregate attempts keep null attribution because older processing did not record it;
- Kafka and retry workers still use aggregate event processing until the v0.11 cutover.

Delivery status projection is deterministic:

```text
processing > retrying > throttled > pending > failed > delivered
```

Zero deliveries project to `delivered`.

## Event lifecycle

Persisted statuses are:

| Status | Meaning |
|--------|---------|
| `delivered` | The aggregate cycle succeeded, including the case of no matching subscriptions |
| `retrying` | Another cycle is scheduled after a retryable outcome |
| `processing` | A retry worker owns a time-bounded claim |
| `failed` | The aggregate cycle reached a terminal outcome |
| `throttled` | Another cycle is scheduled after internal backpressure without consuming an attempt |
| `pending` | Accepted/initial state represented by the API and schema; normally not persisted before first processing |

The effective current flow is:

```text
Kafka message -> delivered | throttled | retrying | failed
retrying/throttled -> processing -> delivered | throttled | retrying | failed
expired processing claim -> processing by a new owner
```

An HTTP delivery attempt increments the event attempt count. Internal throttling does not.
The API currently fixes the
maximum at five cycles. Exponential backoff starts around one second, doubles by attempt,
adds 10% jitter, and is capped at one hour.

## Persistence and recovery invariants

### New Kafka events

1. The worker performs subscription matching and HTTP calls.
2. The aggregate event state and generated attempt rows are written in one PostgreSQL
   transaction.
3. Kafka offsets are committed only after that transaction succeeds.
4. If persistence fails, the batch remains uncommitted and may repeat HTTP calls.

A persistence failure for one event prevents offset commit for the fetched Kafka batch,
so other events in that batch can also be delivered again.

### Retry events

1. Due `retrying` or `throttled` rows, and expired `processing` rows, are claimed with
   row locking that skips work owned by another transaction.
2. A claim records a worker owner and exact expiration deadline.
3. The outcome transaction succeeds only if both owner and deadline still match.
4. Persisting the outcome clears claim metadata atomically with state and attempts.
5. A stale worker is rejected; an expired claim can be recovered by another worker.

Lease recovery preserves liveness but cannot undo an HTTP call, so duplicate delivery
remains possible.

### Retry scheduler capacity

The retry scheduler separates idle discovery from backlog throughput:

- it checks immediately on worker startup and after each configured poll interval;
- a full claim triggers another immediate claim while a configured batch slot is free;
- an empty or partial claim ends the current drain cycle and returns to interval waiting;
- no more than `RETRY_MAX_CONCURRENT_BATCHES` batches execute concurrently per worker;
- shutdown stops new claims and waits for tracked in-flight batches;
- `RETRY_BATCH_SIZE`, `RETRY_MAX_CONCURRENT_BATCHES`, and `RETRY_POLL_INTERVAL` are
  independent controls.

The scheduler does not bypass database pool, receiver rate, circuit-breaker, or
per-subscription concurrency controls.

## Delivery semantics

- Delivery is **at least once**, not exactly once.
- Event IDs deduplicate event rows, not external HTTP effects.
- Receivers are expected to tolerate duplicate calls and should use `X-Event-ID` as an
  idempotency key when appropriate.
- Dispatch provides no FIFO or per-key ordering guarantee.
- There is no automatic replay operation for terminal events.
- Attempt history contains only committed records. An HTTP call followed by a failed
  database transaction may be absent from history.

## Resilience controls

Rate limiting, circuit breaking, and concurrency limiting are scoped by subscription ID.
When Redis is configured, state is coordinated across workers. Without Redis, the worker
uses in-memory fallbacks that do not provide a global multi-worker guarantee.

The current rate-control policy has known inconsistencies documented in
[internal/resilience/README.md](../internal/resilience/README.md). The product must not
claim a normalized configurable traffic contract until v0.9.0 is implemented and
verified. A token-bucket migration is not required unless measurements justify it.

## Security and isolation contract

- All callers and data belong to one trust domain.
- There is no tenant isolation, RBAC, API key, or authenticated operator identity.
- TLS termination and network restriction are deployment responsibilities.
- Subscription secrets are stored in PostgreSQL without application-level encryption.
- The signature header is not a security control.

## Explicitly unsupported behavior

- multi-tenancy or customer isolation;
- exactly-once delivery;
- ordered delivery;
- payload transformation or enrichment;
- subscription verification handshake;
- batch delivery to receivers;
- dead-letter queue or replay API;
- customer-facing UI;
- a managed-service availability or support contract.

## Related authorities

- [Product definition](product.md)
- [Architecture](architecture.md)
- [Limitations and opportunities](LIMITATIONS.md)
- [Verified project state](../PROGRESS.md)
- [Architecture decisions](adr/)
