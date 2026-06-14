# Execution Plan: Product definition and documentation boundaries

> **Status:** Active
> **Started:** 2026-06-14
> **Target:** Make the current Dispatch product understandable before choosing further complexity.
> **Supersedes temporarily:** v0.8.0, returned to the queued sequence unchanged

## Goal

Establish a product-level source of truth that explains who Dispatch serves, which
problem it solves, how users experience it, what it guarantees today, and where its
boundaries are. Separate those concerns from system contracts, architecture,
implementation guidance, execution history, and possible future work.

## Acceptance criteria

- [x] A reader can understand the current product without reading Go code, storage schemas, or deployment manifests.
- [x] Current behavior, known limitations, hypotheses, and future choices are visibly distinct.
- [x] The documentation authority model identifies one durable home for product, behavior, architecture, decisions, execution state, and proposals.
- [x] The current best-fit use case is stated with evidence and without pretending unresolved positioning decisions are settled.
- [x] The product document defines questions and decision criteria for self-hosted versus managed operation, intended users, product value, boundaries, and a credible v1.
- [x] Existing technical information remains discoverable after duplicated or misplaced content is removed.

## Phase 1: Reconstruct the current product

### Step 1: Inventory documentation responsibilities

- [x] Classify the current spec sections as product, behavior contract, architecture, implementation, operations, historical state, or future proposal.
- [x] Compare documented claims with API routes, domain state, worker behavior, configuration, and tests.
- [x] Record contradictions and uncertain claims instead of silently resolving them.
- **Verify:** every major section of the existing spec has an intended durable home.

### Step 2: Define the current product model

- [x] Identify current actors, jobs, workflows, capabilities, guarantees, and limitations.
- [x] State the deployment and trust model supported by the implementation today.
- [x] Separate evidence-backed current positioning from possible future positioning.
- **Verify:** product claims link to behavioral or technical detail where precision is needed.

## Phase 2: Establish durable documentation boundaries

### Step 3: Create the product document

- [x] Add `docs/product.md` as the product-level source of truth.
- [x] Explain the problem, users, concepts, workflow, capabilities, guarantees, boundaries, and current maturity.
- [x] Add a decision framework for unresolved product direction without presenting proposals as committed work.
- **Verify:** the document contains no package maps, SQL, Redis keys, pseudocode, or implementation checklists.

### Step 4: Refocus the system specification

- [x] Rewrite `docs/spec.md` as the precise externally observable behavior contract.
- [x] Remove architecture, implementation, test examples, CI configuration, project structure, and historical roadmap duplication.
- [x] Preserve important API, delivery, state, retry, rate-control, and consistency semantics.
- **Verify:** product intent lives in `product.md`; technical mechanisms live in architecture, ADRs, package READMEs, or operations documentation.

### Step 5: Update navigation and authority rules

- [x] Update README documentation links and reduce duplicated product explanation where practical.
- [x] Update `AGENTS.md` and `PROGRESS.md` with the new authority model.
- [x] Update limitations and next-steps references where their scope overlaps product direction.
- **Verify:** links resolve and no document claims to be the source of truth for another document's concern.

## Phase 3: Review and closure

### Step 6: Validate the documentation as a product model

- [x] Check current claims against routes, configuration, migrations, tests, and critical package documentation.
- [x] Search for stale version claims and conflicting descriptions of scope, guarantees, and roadmap.
- [x] Run formatting/link-oriented checks available in the repository plus the normal build baseline.
- [x] Add a learning note explaining product documents versus specifications, ADRs, and exec plans.
- **Verify:** a new reader can answer what Dispatch is, who it fits today, what it guarantees, what it does not do, and which decisions remain open.

## Closure

- [x] `PROGRESS.md` records the documentation model and the next active engineering increment.
- [x] Move this plan to `docs/exec-plans/done/`.
- [x] Promote v0.8.0 from `queued/` back to `active/` without changing its implementation scope.

## Progress log

| Date | Step | Note |
|------|------|------|
| 2026-06-14 | Plan created | v0.8.0 returned to queued while the product model is clarified. |
| 2026-06-14 | Product reconstructed | Verified product claims against API, domain, delivery, retry, persistence, configuration, migrations, tests, and package context. |
| 2026-06-14 | Documentation split | Added product authority, refocused the behavior spec, and reduced stale limitations and roadmap duplication. |
| 2026-06-14 | Verified | Markdown links, `git diff --check`, build, and race-gated unit/component packages pass. |
