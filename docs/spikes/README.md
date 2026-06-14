# Architectural Spikes

This directory preserves architectural ideas that deserve investigation but are not yet
accepted decisions or implementation plans.

## Harness role

- A spike proposal records the hypothesis, expected benefits, risks, unknowns, and experiments.
- It is not part of the living product contract in `docs/spec.md`.
- It is not an accepted decision; only an ADR can establish one.
- It is not executable work; only a promoted exec plan authorizes implementation.
- `docs/next-steps.md` links to candidate spikes when strategic planning resumes.

## Lifecycle

1. **Proposed:** initial analysis is preserved and research questions are defined.
2. **Investigating:** a bounded spike exec plan has been explicitly selected.
3. **Concluded:** evidence and recommendation are recorded.
4. If accepted, create an ADR and implementation exec plan. If rejected or deferred, keep the
   conclusion here so the same questions do not need to be rediscovered.

Spike code should be disposable unless a later exec plan explicitly promotes it into production.

## Current proposals

- [Kafka outcome topic and asynchronous persistence](kafka-outcome-topic.md)
