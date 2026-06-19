# System Weak-Spots Review After v0.12.0

## Purpose

This review records the weakest or most questionable parts of Dispatch after the per-delivery
runtime cutover, simplification pass, and v0.12.0 security increment. It also corrects two points
that were initially described too broadly: persistence-failure handling is substantially solved,
while legacy aggregate runtime compatibility is less complete than its retained code suggests.

The ranking uses three criteria:

1. potential to contradict the v1 product promise;
2. uncertainty in behavior or ownership;
3. maintenance and operational cost.

## Corrected Ranking

### 1. Replay, retention, and cleanup are absent

Terminal deliveries cannot be replayed through a supported operation, and events, deliveries,
attempts, response excerpts, and frozen secrets have no bounded lifecycle. This is the largest
remaining product gap and the purpose of v0.13.0.

### 2. API and outbound-network trust depend heavily on deployment controls

Dispatch has no application authentication or authorization. Subscription URLs also control where
workers make outbound HTTP requests, while v1 does not provide complete SSRF isolation or destination
ownership verification. The accepted single-trust-domain model makes this supportable, but a
misconfigured deployment has a large blast radius.

### 3. Legacy aggregate runtime compatibility is ambiguous

New Kafka and retry work uses delivery rows. The application no longer wires aggregate event retry
processing, but the repository still contains:

- `LegacyEventRepository`;
- `ClaimRetryEvents`;
- `PersistNewOutcomes`;
- `PersistClaimedOutcomes`;
- standalone event status and attempt write methods;
- event-level processing lease columns.

These methods are exercised mainly by repository tests, not by the current application runtime.
Retaining them does not by itself recover pre-v0.11 non-terminal event rows because migration 005
does not backfill those rows into delivery state and no legacy retry worker is assembled.

The simplification pass correctly preserved legacy **readability**: old events and unattributed
attempts must remain queryable. It was too conservative about legacy **runtime execution**. The
project must choose one honest policy before v0.13 expands lifecycle behavior:

1. migrate recoverable pre-v0.11 work into delivery rows without inventing false attribution;
2. provide an explicit temporary legacy drainer; or
3. declare old non-terminal aggregate work unsupported after upgrade and provide an operator
   resolution procedure.

After that decision, remove runtime interfaces, methods, tests, and eventually schema columns that
have no supported caller. Do not confuse historical read compatibility with maintaining two runtime
state machines.

### 4. Operational recovery is not yet demonstrated as a product procedure

Failure-path tests are strong, but installation, upgrade, backup/restore, alert response, and Kafka,
PostgreSQL, Redis, and worker interruption runbooks have not been exercised as one supported
operational contract. V0.14.0 owns this gap.

### 5. The API wire contract is still immature

V0.12 made subscription secrets write-only, but the broader API still lacks strict bounded JSON
decoding, deliberate URL policy, stable machine-readable errors, bounded pagination, machine-readable
schemas, and compatibility checks. Event and delivery responses also remain coupled to domain types.
The queued API hardening plan preserves this work without automatically adding it to v1.

### 6. Secrets remain plaintext in PostgreSQL and backups

API responses no longer expose subscription secrets, but active and frozen secrets remain available
to database readers and backups. This is an accepted v1 deployment responsibility, not an implemented
application security control.

### 7. Consumer-group behavior is outside the deterministic E2E harness

The E2E suite uses a direct partition reader to make cross-component behavior deterministic. Unit and
component tests cover commit decisions, but real consumer-group rebalance, partition reassignment,
and broker failover are not exercised end to end.

### 8. Observability and process bootstrap have thinner coverage

Observability remains the lowest-covered important package, production router behavior has limited
focused coverage, and command wrappers have no direct tests. These are smaller surfaces than delivery
state but can silently impair diagnostics and lifecycle behavior.

### 9. Redis and local rate limiting implement different algorithms

Both implementations satisfy shared normalized contract tests, but Redis sliding-window behavior and
local token-bucket burst behavior are not identical. This remains evidence-gated technical debt; a
distributed token bucket should not be introduced without measured need.

### 10. Capacity evidence is not yet a supported operating envelope

Existing benchmarks demonstrate useful ingestion and retry performance in one environment. They do
not yet establish sustained delivery capacity under representative receiver latency, consumer-group
coordination, infrastructure contention, and failure rates. V0.14.0 must convert measurements into
bounded environment-specific guidance rather than a universal throughput claim.

## Persistence Failure Handling: What Is Actually Solved

The v0.6 through v0.11 increments resolved the dangerous correctness failures:

- delivery initialization is durable before an HTTP call;
- each destination owns a fenced lease;
- one delivery outcome and its attempt history commit atomically;
- persistence errors prevent Kafka offset commit;
- Kafka redelivery reuses the frozen delivery set;
- delivered and terminal destinations are skipped;
- an active lease prevents immediate duplicate HTTP calls after outcome persistence fails;
- expired leases make abandoned work recoverable.

Therefore, saying that a persistence failure simply “redelivers the entire Kafka batch” is
misleading. Kafka messages are fetched again, but current leases and terminal delivery states prevent
blind repetition of every HTTP side effect.

The residual limitation is fundamental to arbitrary HTTP delivery:

```text
receiver accepts HTTP
-> PostgreSQL outcome transaction fails
-> attempt is not durable and delivery remains leased
-> Kafka offset remains uncommitted
-> active lease suppresses immediate duplicate work
-> recovery after lease expiry may call the receiver again
```

Dispatch cannot atomically commit an external receiver response, PostgreSQL transaction, and Kafka
offset. It deliberately favors recovery over silent loss. Receiver event-ID deduplication remains
required when duplicates matter. This is a managed at-least-once limitation, not an unresolved core
implementation defect.

## Planning Consequence

Before implementing v0.13.0, decide the pre-v0.11 non-terminal upgrade policy and remove or support
the aggregate runtime path accordingly. Replay and retention must be designed against one explicit
runtime ownership model. Carrying dead aggregate methods into replay would multiply state transitions,
tests, cleanup rules, and operator ambiguity without providing real compatibility.
