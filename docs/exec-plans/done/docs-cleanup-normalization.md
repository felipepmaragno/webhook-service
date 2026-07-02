# Execution Plan: Docs Cleanup And Normalization

> **Status:** Completed
> **Estimated effort:** Small documentation increment
> **Depends on:** v1.0.0 release hardening and v1 showcase polish
> **Release criterion:** Make `docs/` look intentional and current without adding product scope or
> another navigation document.

## Goal

Clean up the documentation directory by reducing naming inconsistency, separating current documents
from historical/supporting material, and updating the harness so future work does not treat old
archaeology as mandatory current context.

## Accepted Contract

- Documentation-only change.
- Do not add a new `docs/README.md` in this increment.
- Keep core current docs easy to find at `docs/` root.
- Prefer lowercase kebab-case filenames.
- Preserve historical evidence when still useful, but do not present it as current authority.
- Update all links and harness references in the same commit.

## Target Layout

- Current root docs:
  - `product.md`
  - `spec.md`
  - `architecture.md`
  - `operations.md`
  - `limitations.md`
  - `next-steps.md`
  - `v1-roadmap.md`
  - `v1-summary.md`
- Supporting guides and reports:
  - `docs/guides/deployment-security.md`
  - `docs/guides/performance-validation.md`
  - `docs/guides/performance.md`
- Historical context:
  - `docs/archive/audit.md`
- Existing grouped history:
  - `docs/adr/`
  - `docs/exec-plans/`
  - `docs/learnings/`
  - `docs/spikes/`

## Phase 1: File Moves And Naming

- [x] Rename `docs/LIMITATIONS.md` to `docs/limitations.md`.
- [x] Move `docs/PERFORMANCE.md` to `docs/guides/performance.md`.
- [x] Move `docs/performance-validation-guide.md` to `docs/guides/performance-validation.md`.
- [x] Move `docs/deployment-security.md` to `docs/guides/deployment-security.md`.
- [x] Move `docs/audit.md` to `docs/archive/audit.md`.
- **Verify:** `find docs -maxdepth 2 -type f` shows a cleaner top-level layout.

## Phase 2: Link And Harness Updates

- [x] Update README links and project structure.
- [x] Update `AGENTS.md` so `archive/audit.md` is historical, not mandatory pre-implementation
  reading.
- [x] Update `PROGRESS.md` documentation responsibilities.
- [x] Update all links in docs, ADRs, spikes, learnings, and completed exec plans.
- **Verify:** relative Markdown link validation passes.

## Phase 3: Closure

- [x] Run `git diff --check`.
- [x] Run `GOCACHE=/tmp/dispatch-gocache go build ./...`.
- [x] Move this exec plan to `done/`.
- [x] Commit and push the branch for review.

## Required Validation

- [x] Relative Markdown link validation
- [x] `git diff --check`
- [x] `GOCACHE=/tmp/dispatch-gocache go build ./...`

## Progress Log

| Date | Step | Note |
|------|------|------|
| 2026-07-02 | Plan created | Scope limited to docs layout, naming, links, and harness references. |
| 2026-07-02 | Completed | Current docs stay at root, supporting docs moved under `guides/`, and historical audit moved under `archive/`. |
