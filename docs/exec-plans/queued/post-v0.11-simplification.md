# Execution Plan: Post-v0.11 - Implementation Simplification Pass

> **Status:** Queued
> **Target:** Reduce accidental complexity after per-subscription delivery state is active.
> **Depends on:** v0.10.0 per-subscription delivery persistence and v0.11.0 processing cutover
> **Followed by:** v1 roadmap work, unless this pass is promoted as release-risk reduction.

## Goal

Simplify implementation areas that became callback-heavy or contract-fragile while preserving the
reliability guarantees that make Dispatch useful.

This plan must not replace v0.10.0 or v0.11.0. The largest current simplification is the move from
aggregate event state to per-subscription delivery state. This pass is only valuable after that model
is in place and the remaining accidental complexity is visible.

## Current candidates

1. Delivery handler observability wiring has too many callback-shaped options.
2. Redis and in-memory resilience implementations can drift unless they share stronger contract tests.
3. Legacy aggregate-event compatibility code may need isolation after new per-delivery runtime paths are active.

## Non-goals

- Do not remove Kafka, PostgreSQL leases, Redis coordination, or atomic persistence only because they are complex.
- Do not simplify the retry poller unless a concrete post-v0.11 defect or maintenance burden is identified.
- Do not collapse the documentation harness; stale documentation is the problem to manage, not documentation volume.

## Acceptance criteria

- [ ] Delivery handler emits delivery lifecycle observations through one cohesive observer or metrics adapter.
- [ ] Resilience contract tests run against Redis and in-memory implementations where behavior is expected to match.
- [ ] Remaining aggregate-event compatibility code is isolated behind clear repository/API boundaries.
- [ ] No reliability invariant from `PROGRESS.md`, ADR 015, ADR 016, or the v1 roadmap is weakened.
- [ ] Documentation identifies which complexity was essential and which was removed.

## Phase 1: Reassess After v0.11

- [ ] Review the post-v0.11 runtime path and identify actual duplication or callback sprawl.
- [ ] Check whether per-delivery state made any aggregate event code unreachable for new events.
- [ ] Decide whether this pass should run before v0.12.0 or stay deferred.
- **Verify:** create a short design note before editing code.

## Phase 2: Simplify Observability Wiring

- [ ] Replace scattered delivery metric callbacks with a small lifecycle observer or typed metrics adapter.
- [ ] Keep tests able to assert delivery, retry, throttle, degraded, and attempt events.
- [ ] Preserve Prometheus metric names and labels unless a migration is explicitly documented.
- **Verify:** handler tests and metrics wiring tests pass.

## Phase 3: Harden Resilience Contracts

- [ ] Extract shared tests for policy defaulting, allow/deny behavior, retry delay, and degraded metadata.
- [ ] Apply shared cases to in-memory and Redis-backed implementations where semantics are intentionally common.
- [ ] Document remaining intentional differences, especially sliding-window versus token-bucket burst semantics.
- **Verify:** Redis Testcontainers and in-memory resilience tests pass.

## Phase 4: Isolate Legacy Compatibility

- [ ] Identify legacy aggregate reads or writes that exist only for pre-delivery-model compatibility.
- [ ] Move compatibility behavior behind named repository/API helpers where practical.
- [ ] Avoid deleting compatibility paths unless migrations and product docs say they are no longer supported.
- **Verify:** legacy event and attempt reads remain covered.

## Closure

- [ ] Build, lint, race-gated tests, PostgreSQL/Redis integration, and API tests pass.
- [ ] `PROGRESS.md` records what was simplified and why it did not weaken the v1 promise.
- [ ] Move this file to `done/` only if the pass is actually promoted and executed.
