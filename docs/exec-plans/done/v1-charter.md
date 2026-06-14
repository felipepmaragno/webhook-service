# Execution Plan: v1 product charter and finite roadmap

> **Status:** Active
> **Started:** 2026-06-14
> **Target:** Convert the accepted product direction into a binding v1 finish line.
> **Temporarily supersedes:** v0.8.0, returned to queued unchanged

## Goal

Define Dispatch v1 as a finite self-hosted product, identify the minimum increments
required to satisfy its promise, and prevent optional features or speculative scale work
from extending the project indefinitely.

## Acceptance criteria

- [x] Product objective, target user, deployment model, differentiator, and non-goals are accepted rather than open questions.
- [x] V1 has an observable completion definition and release gate.
- [x] Every remaining v1 increment closes a named product gap.
- [x] Optional algorithm work is separated from required v1 work.
- [x] Per-subscription delivery state is split into bounded executable increments.
- [x] The harness defines how new ideas are handled during the v1 feature freeze.

## Phase 1: Establish the charter

### Step 1: Record accepted product decisions

- [x] Update `docs/product.md` with the accepted objective and target user.
- [x] Commit to self-hosted operation within one trusted organization for v1.
- [x] Define reliability and understandable recovery as the differentiator, with simplicity as a constraint.
- [x] Convert managed service, multi-tenancy, UI, transformations, ordering, and speculative scale into explicit non-goals.
- **Verify:** no accepted v1 decision remains phrased as an open product question.

### Step 2: Define the v1 release promise

- [x] Define the operator and receiver workflows that v1 must support.
- [x] Define completion criteria for per-destination state, recovery, replay, signatures, observability, security assumptions, retention, installation, and upgrades.
- [x] Define severity and validation gates for release.
- **Verify:** every completion criterion can be demonstrated by documentation, tests, or an operational procedure.

## Phase 2: Build the finite roadmap

### Step 3: Map current gaps to increments

- [x] Create `docs/v1-roadmap.md` with required increments, dependencies, effort, and exit criteria.
- [x] Retain v0.8.0 and v0.9.0 only where they support the accepted operational promise.
- [x] Split per-subscription delivery identity into storage/model foundation and processing/retry cutover.
- [x] Add bounded security, replay/retention, and release-readiness increments.
- **Verify:** the roadmap ends at v1.0.0 and contains no unbounded feature bucket.

### Step 4: Reclassify optional work

- [x] Remove token bucket from the automatic execution sequence.
- [x] Preserve its rationale as a spike that requires measured evidence and an explicit promotion decision.
- [x] Update v0.9.0 closure so it advances to the next required v1 increment.
- **Verify:** queued exec plans contain only required, decision-complete v1 work.

### Step 5: Define delivery-model increments

- [x] Add a queued plan for per-subscription delivery identity and persistence.
- [x] Add a queued plan for processing, retry, and query cutover to per-subscription state.
- [x] Preserve at-least-once semantics and define stable destination-set behavior.
- **Verify:** neither increment leaves an ambiguous source of truth when complete.

## Phase 3: Integrate and close

### Step 6: Update harness navigation

- [x] Link the v1 roadmap from README, product, next steps, AGENTS, and PROGRESS.
- [x] Add a learning note about defining a product finish line and controlling scope.
- [x] Record the feature-freeze rule: new work must close a v1 criterion or remain a spike/post-v1 note.
- **Verify:** current state, execution order, and post-v1 ideas are not conflated.

## Closure

- [x] Markdown links, diff checks, build, and race-gated unit/component validation pass.
- [x] Move this plan to `done/` and restore v0.8.0 as the single active plan.
- [x] Commit the complete product-definition and v1-charter documentation increment.

## Progress log

| Date | Step | Note |
|------|------|------|
| 2026-06-14 | Plan created | Product direction was accepted; v0.8.0 was temporarily returned to queued. |
| 2026-06-14 | Charter accepted | Self-hosted single-trust-domain v1, reliability focus, explicit non-goals, and release gate recorded. |
| 2026-06-14 | Roadmap bounded | v0.8.0 through v1.0.0 mapped to product gaps; token bucket moved to an evidence-gated spike. |
| 2026-06-14 | Delivery model planned | Persistence foundation and runtime cutover split into v0.10.0 and v0.11.0. |
| 2026-06-14 | Verified | Markdown links, diff checks, build, and race-gated unit/component packages pass. |
