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
| v0.9.0 | Normalize destination-protection terminology | Completed |
| pre-v0.14 | Simplify destination protection to one max-delivery-rate knob | Completed |
| v0.10.0 | Per-subscription delivery persistence foundation | Completed |
| v0.11.0 | Per-subscription processing and retry cutover | Completed |
| v0.12.0 | Cryptographic signatures and deployment security contract | Completed |
| v0.13.0 | Terminal-delivery replay, retention, and cleanup | Completed |
| v0.14.0 | Operational readiness and measured capacity envelope | Roadmap |
| v1.0.0 | Release hardening and complete validation | Roadmap; no new features |

The active and queued exec plans are authoritative for implementation details. Later
roadmap entries intentionally do not have exec plans until dependencies clarify their
decision details.

## Immediate work

Write and review the v0.14.0 operational-readiness exec plan. It should turn the roadmap outcomes
into reproducible installation, migration, backup/restore, incident, alert, and measured capacity
procedures. Do not add product features to that increment.

V0.13.0 is complete and preserved at
[`docs/exec-plans/done/v0.13.0.md`](exec-plans/done/v0.13.0.md). Because no deployment requires
backward compatibility, its migration baseline models only per-delivery execution and mandatory
attempt attribution. Historical reasoning remains in ADRs and learnings, not runtime code.

The [internal package-boundaries spike](spikes/internal-package-boundaries.md) is not promoted for
v0.13 because replay schedules work through PostgreSQL and does not call `kafka.DeliveryHandler`.

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
