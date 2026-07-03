# Next Steps - Dispatch

> Updated: 2026-07-01
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
| pre-v0.14b | Restore Redis distributed max-delivery-rate enforcement | Completed |
| v0.10.0 | Per-subscription delivery persistence foundation | Completed |
| v0.11.0 | Per-subscription processing and retry cutover | Completed |
| v0.12.0 | Cryptographic signatures and deployment security contract | Completed |
| v0.13.0 | Terminal-delivery replay, retention, and cleanup | Completed |
| v0.14.0 | Minimal operational readiness and capacity smoke | Completed |
| v1.0.0 | Release hardening and complete validation | Completed |

There is no active required exec plan after v1. Queued plans are optional future work and become
authoritative only after an explicit decision promotes them.

## Immediate work

Review the v1.0.0 release branch and, if accepted, tag the v1 release. There is no required next
feature increment after v1. Optional future work should start from a new product decision or a
focused defect report, not from roadmap momentum.

V0.13.0 is complete and preserved at
[`docs/exec-plans/done/v0.13.0.md`](exec-plans/done/v0.13.0.md). Because no deployment requires
backward compatibility, its migration baseline models only per-delivery execution and mandatory
attempt attribution. Historical reasoning remains in ADRs and learnings, not runtime code.

The [internal package-boundaries spike](spikes/internal-package-boundaries.md) was reviewed after
v1. It concluded that delivery execution should eventually move out of `internal/kafka`, but only as
one narrow structural increment. The queued
[delivery package extraction](exec-plans/queued/delivery-package-extraction.md) plan captures that
candidate work.

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

After v1.0.0, proposed work should satisfy at least one condition:

1. fix a defect that contradicts the accepted v1 contract;
2. clarify or simplify the completed v1 system without changing product scope;
3. implement a newly accepted product direction through a fresh exec plan.

Other work is deferred. V1 completion ends planned feature development for this project.
