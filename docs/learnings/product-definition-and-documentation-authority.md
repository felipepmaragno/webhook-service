# Product Definition and Documentation Authority

## Why this increment was necessary

The original `docs/spec.md` tried to answer too many kinds of questions at once. It mixed
product purpose, user scope, API behavior, architecture diagrams, SQL and Redis schemas,
code examples, testing guidance, CI configuration, historical milestones, and future
plans.

That created two engineering problems:

1. A reader could not understand the product without also interpreting implementation
   detail and stale history.
2. Future work could appear justified because it was present in a roadmap, even when the
   target user and product boundary had not been selected.

Documentation structure is part of the engineering harness. If documents do not have
clear ownership, an engineer or LLM can follow a locally plausible statement while
violating the actual product or system contract.

## The authority model

| Question | Durable authority |
|----------|-------------------|
| Why does the product exist, for whom, and with which boundaries? | `docs/product.md` |
| What behavior can callers and receivers observe? | `docs/spec.md` |
| How is that behavior implemented? | `docs/architecture.md` and critical package READMEs |
| Why was a technical choice accepted? | ADR |
| What is mechanically verified now? | `PROGRESS.md` |
| How will one bounded increment be executed? | Active exec plan |
| Which direction might be valuable later? | `docs/next-steps.md` |
| Which uncertain architecture idea deserves investigation? | Spike document |

The important rule is not merely having several files. It is avoiding two files that
both claim authority over the same question.

## Product document versus specification

A product document is not a smaller technical specification.

The product document defines:

- the problem and value;
- intended users and actors;
- user-visible workflow;
- capabilities and promises;
- boundaries, maturity, and unresolved direction.

The system specification defines:

- request and delivery contracts;
- result classification;
- state semantics;
- consistency and recovery invariants;
- explicitly unsupported behavior.

The specification can change without changing product purpose, such as correcting HTTP
status classification. The product can also change without immediately prescribing an
implementation, such as choosing to serve multiple independent customers.

## Exec plans are not small product specifications

An exec plan is temporary execution authority for an accepted increment. It describes
steps, checks, dependencies, and closure. It may reference a product requirement or
behavioral contract, but it must not become their only durable home.

After completion:

- enduring product meaning remains in `product.md`;
- enduring behavior remains in `spec.md`;
- technical rationale remains in an ADR;
- the completed plan remains historical evidence of how the change was delivered.

This keeps implementation history useful without forcing future engineers to reconstruct
the current contract from old checklists.

## What code review revealed about the current product

The code supports a self-hosted dispatcher inside one trust domain more strongly than it
supports an external webhook platform:

- there is no caller identity, authentication, authorization, or tenant key;
- subscriptions and event IDs occupy one global namespace;
- operations are exposed through engineering APIs, logs, metrics, and dashboards;
- users must operate Kafka, PostgreSQL, and optionally Redis;
- security, replay, and per-destination audit features are incomplete.

That does not permanently decide the product direction. It establishes an honest current
position from which alternatives can be evaluated.

## Labels can hide weaker semantics

Several broad labels had to be replaced with precise behavior:

- “idempotency” means one persisted event row per ID, not exactly-once delivery;
- “throttled” exists in the model, but the current delivery path persists control
  rejection as a generic retry;
- “fan-out” sends to many destinations, but stores one aggregate event state;
- “accepted” means published to Kafka, not immediately queryable in PostgreSQL;
- “distributed resilience” depends on Redis and degrades to independent local controls.

Product documentation should explain the consequence a user experiences. Technical
documentation should then explain the mechanism.

## Complexity lesson

Architecture is not product value by itself. Kafka, Redis, leases, semaphores, and
microservice boundaries are justified only when they make a chosen user promise stronger
at an expected workload.

Before adding complexity, require a traceable chain:

```text
user problem -> product behavior -> system contract -> technical decision -> verification
```

If the chain starts with a technology or an imagined scale target, the idea belongs in a
spike until evidence connects it to the product.

## Practical maintenance rules

1. Write current behavior separately from planned behavior.
2. Do not copy implementation detail into the product document.
3. Do not use an ADR as a statement of current behavior; decisions can be superseded or
   incompletely implemented.
4. Do not use a completed exec plan as the current roadmap.
5. Treat contradictions as defects and verify them against code and tests.
6. Add links instead of duplicating authoritative content.
7. Revisit product boundaries before implementing a plan whose value depends on scale,
   tenancy, or deployment model.
