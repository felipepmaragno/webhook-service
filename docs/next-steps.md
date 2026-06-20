# Next Steps - Dispatch

> Updated: 2026-06-19
> The product direction and finite release sequence are accepted in
> [product.md](product.md) and [v1-roadmap.md](v1-roadmap.md). This document now records
> only execution orientation and deferred directions.

## Current direction

Dispatch v1 is a production-conscious, self-hosted webhook delivery service for one
trusted organization. Reliability and understandable recovery are the product value;
simplicity constrains further distributed complexity.

The required sequence is:

| Increment | Outcome | State |
|-----------|---------|-------|
| v0.8.0 | Bounded retry draining and backlog observability | Completed |
| v0.9.0 | Normalize rate, burst, concurrency, throttling, and Redis degradation | Completed |
| v0.10.0 | Per-subscription delivery persistence foundation | Completed |
| v0.11.0 | Per-subscription processing and retry cutover | Completed |
| v0.12.0 | Cryptographic signatures and deployment security contract | Completed |
| v0.13.0 | Terminal-delivery replay, retention, and cleanup | Next planning decision |
| v0.14.0 | Operational readiness and measured capacity envelope | Roadmap |
| v1.0.0 | Release hardening and complete validation | Roadmap; no new features |

The active and queued exec plans are authoritative for implementation details. Later
roadmap entries intentionally do not have exec plans until dependencies clarify their
decision details.

## Immediate work

Design the v0.13.0 terminal-delivery replay, retention, and cleanup contract before
implementation. The plan must settle replay generation identity, concurrent replay
ownership, eligible terminal states, cleanup ordering, retention configuration, and
observability without rewriting delivery history.

Before replay design begins, resolve the pre-v0.11 non-terminal upgrade policy identified in the
[weak-spots review](learnings/system-weak-spots-review.md). Legacy read compatibility remains
necessary, but unused aggregate runtime methods must either gain an explicit supported caller or be
removed before replay introduces more lifecycle transitions.

The broader API plan remains intentionally unversioned. Before promotion, split it if
necessary so contract-quality work does not obscure the finite v1 security, replay,
operations, and release gates.

## Deferred directions

These ideas are not part of v1 and must not enter the active sequence:

- multi-tenancy or managed-service capabilities;
- UI, billing, quotas, transformations, strict ordering, or batch delivery;
- multi-region architecture or speculative storage redesign;
- Kafka outcome-topic architecture;
- distributed token bucket without evidence from the normalized v0.9.0 implementation.

Their analysis remains in `docs/spikes/` so the project does not lose useful reasoning.

## Feature-freeze rule

Until v1.0.0, proposed work must satisfy at least one condition:

1. close a named v1 release criterion;
2. fix a defect that threatens a v1 criterion;
3. reduce demonstrated delivery risk in the active increment.

Other work is deferred. Completing v1 ends planned feature development for this project.
