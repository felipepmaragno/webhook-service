# Next Steps — Dispatch

> Updated: 2026-06-12
> This document contains strategic options. Dependency-ready implementation work belongs
> in `docs/exec-plans/active/`; future decision-complete work belongs in `queued/`.

## Current reliability sequence

The immediate direction is already decided and split into bounded increments:

| Increment | Outcome | Effort | Benefit |
|-----------|---------|--------|---------|
| v0.6.0 | Atomic event outcome + attempt persistence before Kafka commit | Completed | Prevents acknowledged events from losing retry state during database failures |
| v0.7.0 | Expiring, owner-fenced retry claim leases | 2-3 focused sessions | Recovers events left `processing` after worker crashes and prevents stale writes |
| v0.8.0 | Bounded retry-poller concurrency and backlog observability | 2-3 focused sessions | Improves retry throughput without weakening persistence or lease invariants |

The active and queued exec plans are the authority for implementation details and closure
checks. This file should not duplicate their step-by-step tasks.

## Feature options after reliability work

| Direction | Estimated effort | Benefit | Important dependency or tradeoff |
|-----------|------------------|---------|----------------------------------|
| Dead-letter replay API | 1-2 sessions | Makes exhausted events operationally recoverable | Define authorization and replay idempotency before exposing the endpoint |
| Per-subscription delivery identity | 2-3 sessions | Makes fan-out attempts auditable and supports stronger deduplication contracts | Requires a schema migration and explicit attempt identity semantics |
| Multi-tenancy | 4-6 sessions | Enables isolated customers, quotas, and tenant-aware operations | Cross-cuts authentication, every repository query, metrics, and migrations |
| Webhook verification | 1-2 sessions | Detects invalid destinations before normal traffic | Adds a subscription lifecycle and receiver handshake contract |
| Ordered delivery per key | 3-5 sessions | Helps consumers that require sequencing | Reduces parallelism and needs a partitioning/ordering key contract |

## Recommendation

Finish v0.7.0 and v0.8.0 before adding product features. Atomic persistence prevents one
data-loss window, but retry claims can still remain stuck after a crash and the poller still
lacks explicit backlog capacity. Once those invariants are mechanically verified, dead-letter
replay is the best bounded feature candidate because it adds direct operational value without
the broad cross-cutting scope of multi-tenancy.
