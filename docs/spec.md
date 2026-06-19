# Dispatch System Behavior Specification

> **Authority:** This document defines externally observable behavior and system
> invariants. Product purpose and positioning belong in [product.md](product.md).
> Implementation structure belongs in [architecture.md](architecture.md), ADRs, and
> critical package READMEs.

## Scope

Dispatch accepts events, freezes matching webhook subscriptions into delivery rows, delivers them
asynchronously, records per-destination outcomes, and schedules retryable deliveries.

This specification describes current behavior. It is not a roadmap, test plan, database
schema, or deployment guide.

## HTTP API

| Method | Path | Behavior |
|--------|------|----------|
| `POST` | `/events` | Validate and publish an event to Kafka; return `202` after publish succeeds |
| `GET` | `/events/{id}` | Return the persisted event projection or `404` |
| `GET` | `/events/{id}/attempts` | Return recorded HTTP attempts for the event |
| `GET` | `/events/{id}/deliveries` | Return per-subscription delivery rows for the event; legacy aggregate events may return an empty list |
| `POST` | `/subscriptions` | Create an active webhook subscription |
| `GET` | `/subscriptions` | List active subscriptions |
| `PUT` | `/subscriptions/{id}/secret` | Replace the active secret used by future delivery initialization |
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
The request secret is write-only and never appears in subscription responses.

Secret values must contain between 1 and 4096 bytes when provided. Rotate the active secret with:

```http
PUT /subscriptions/sub_abc123/secret
Content-Type: application/json

{"secret":"replacement-secret"}
```

A successful rotation returns metadata only:

```json
{"id":"sub_abc123","secret_rotated":true}
```

An empty or oversized secret returns `400`; a missing or inactive subscription returns `404`.

Rotating an active subscription secret changes the secret copied into deliveries initialized after
the rotation. Existing delivery rows retain the old secret for deterministic retries. Operators
must accept both secrets at the receiver until old-secret deliveries are terminal or outside the
retention window.

Deletion sets the subscription inactive. Only active subscriptions are listed or used
for new delivery cycles.

## Subscription matching

For each event type, an active subscription matches when any configured filter is:

- exactly equal to the event type;
- `*`, which matches every type; or
- a suffix-wildcard prefix such as `order.*`.

Matching is case-sensitive. More general glob syntax is not supported.

The worker loads matching subscriptions when the event is first initialized. That matching result
is frozen into delivery rows. Retries use the frozen delivery rows and do not observe later
subscription changes for the same event.

## Webhook request

Dispatch sends one HTTP `POST` to each matching subscription:

```http
POST {subscription.url}
Content-Type: application/json
X-Event-ID: evt_abc123
X-Event-Type: order.created
X-Trace-ID: <trace-id when present>
X-Dispatch-Timestamp: <Unix seconds when a secret is configured>
X-Dispatch-Signature: v1=<HMAC-SHA256 when a secret is configured>

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

For subscriptions with a non-empty secret, Dispatch signs the exact transmitted request body.
The HMAC input is the ASCII timestamp, one `.` byte, and the raw body bytes. The signature is
HMAC-SHA256 encoded as lowercase hexadecimal and prefixed with `v1=`. Unsigned subscriptions omit
both signature headers.

Receivers verify the raw body before JSON parsing, compare signatures in constant time, and should
reject timestamps outside a bounded tolerance. A five-minute tolerance is the documented starting
point. Signature validity does not prevent Dispatch from repeating the same logical event;
receivers still deduplicate by `X-Event-ID` when duplicates matter. ADR 020 contains the canonical
test vector and rotation contract.

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

## Fan-out and delivery projection

An event is delivered to every active matching subscription captured in its frozen delivery set.
Dispatch stores one delivery state per event/subscription pair and projects event state from those
delivery rows.

The event projection rule is:

```text
processing > retrying > throttled > pending > failed > delivered
```

Consequences:

- one destination can retry without repeating destinations that already delivered;
- one destination can fail terminally while another remains retryable;
- duplicate Kafka processing reuses the frozen delivery set and skips already terminal or
  successful deliveries;
- new runtime attempt rows identify the exact delivery and subscription.

## Per-subscription delivery records

Dispatch has a durable per-subscription delivery model used by the current runtime path.
Each delivery is identified by a stable event/subscription pair and snapshots the destination URL,
secret, and rate-control policy needed for deterministic future processing.

Current behavior:

- delivery rows are initialized before external HTTP calls;
- `GET /events/{id}/deliveries` returns initialized delivery rows or an empty list for legacy
  aggregate-only events;
- new delivery-attributed attempts store `delivery_id` and `subscription_id`;
- existing aggregate attempts keep null attribution because older processing did not record it;
- Kafka and retry workers process delivery rows for new runtime work.

Delivery status projection is deterministic:

```text
processing > retrying > throttled > pending > failed > delivered
```

Zero deliveries project to `delivered`.

## Event lifecycle

Persisted statuses are:

| Status | Meaning |
|--------|---------|
| `delivered` | All deliveries succeeded, or the event had no matching subscriptions |
| `retrying` | Another cycle is scheduled after a retryable outcome |
| `processing` | A retry worker owns a time-bounded claim |
| `failed` | At least one delivery failed terminally and no delivery remains active |
| `throttled` | Another cycle is scheduled after internal backpressure without consuming an attempt |
| `pending` | Accepted/initial state represented by the API and schema; normally not persisted before first processing |

The effective current flow is:

```text
Kafka message -> delivery initialization -> processing delivery claims
delivery: pending/retrying/throttled -> processing -> delivered | throttled | retrying | failed
expired delivery processing claim -> processing by a new owner
```

An HTTP delivery attempt increments the event attempt count. Internal throttling does not.
The API currently fixes the
maximum at five cycles. Exponential backoff starts around one second, doubles by attempt,
adds 10% jitter, and is capped at one hour.

## Persistence and recovery invariants

### New Kafka events

1. The worker performs subscription matching and initializes the event's frozen delivery set.
2. The worker claims processable deliveries with owner/deadline fencing.
3. Each delivery outcome and generated attempt row are written in one PostgreSQL transaction.
4. Kafka offsets are committed only after delivery work has a durable outcome or recoverable
   delivery lease/retry state.
5. If persistence fails before that boundary, the batch remains uncommitted. Duplicate Kafka
   processing reuses the frozen delivery set and should not call already terminal or successful
   destinations.

A persistence failure for one event prevents offset commit for the fetched Kafka batch,
so other events in that batch can also be delivered again.

### Retry events

1. Due `retrying` or `throttled` delivery rows, and expired `processing` delivery rows, are claimed with
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
- Signed webhooks follow ADR 020; valid signatures do not prevent at-least-once duplicates.

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
