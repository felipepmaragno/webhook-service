# Execution Plan: Delivery Package Extraction

> **Status:** Queued; promote only after explicit approval
> **Estimated effort:** Medium structural refactor, one focused MR
> **Depends on:** Concluded [internal package-boundaries spike](../../spikes/internal-package-boundaries.md)
> **Release criterion:** Make delivery execution ownership match the code's runtime model without
> changing product behavior.

## Goal

Extract transport-independent delivery execution from `internal/kafka` into `internal/delivery`.
Kafka should own queue I/O and serialization. Delivery should own subscription freezing,
claim-backed execution, webhook sending, signing, HTTP classification, rate-limit decisions,
outcome calculation, and outcome persistence.

## Why This Is Worth Doing

The current system works, but the package name is misleading:

- Kafka-originated work calls `kafka.DeliveryHandler`.
- Retry-originated work also calls `kafka.DeliveryHandler`, even though no Kafka operation is
  involved.
- The `internal/kafka` package now mixes broker concerns with core delivery behavior.

This does not break runtime behavior, but it raises the cognitive cost for future changes. A
maintainer must learn that the Kafka package is also the delivery engine. Extracting a delivery
package makes dependency direction easier to explain and reduces the chance that future retry,
replay, or webhook changes accidentally touch broker code.

## Accepted Contract

- No product behavior changes.
- No database schema changes.
- No changes to Kafka offset commit semantics.
- No changes to retry, replay, retention, rate-limit, signature, or at-least-once guarantees.
- No `internal/messaging/kafka` rename in this increment.
- No API contract hardening in this increment; `api.EventPublisher` may still use the existing Kafka
  event envelope until a separate API boundary plan is promoted.
- No repository-wide interface migration for symmetry.
- One increment, one MR. If the extraction starts pulling in API DTOs, repository relocation, or
  broader package renames, stop and split the work.

## Phase 0: Baseline And Move Map

- [ ] Run the focused baseline tests before moving code.
- [ ] Identify the exact files/functions to move:
  - delivery handler construction and options;
  - `ProcessBatch` and `ProcessDeliveries`;
  - webhook sender;
  - signature helpers;
  - delivery result classification.
- [ ] Decide whether the accepted-event envelope should temporarily remain in `internal/kafka` or
  move to `internal/delivery` as a delivery command.
- **Verify:** the move map does not require observable behavior, schema, or queue protocol changes.

## Phase 1: Extract `internal/delivery`

- [ ] Create `internal/delivery` with the current handler behavior.
- [ ] Rename exported types only where the new package name makes the old names redundant.
- [ ] Keep functional options and tests close to the extracted package.
- [ ] Preserve `DeliveryObserver` semantics and metric call sites.
- **Verify:** delivery package tests cover success, retry, failure, throttling, signing, and
  persistence-error behavior previously covered in Kafka tests.

## Phase 2: Rewire Kafka And Retry

- [ ] Make the Kafka consumer depend on the extracted delivery processor.
- [ ] Keep Kafka producer, consumer, and event serialization in `internal/kafka`.
- [ ] Make the retry poller depend on `delivery` instead of a Kafka-named concrete type.
- [ ] Update `internal/app` assembly with the new package ownership.
- **Verify:** Kafka offset commit tests and retry processor-error tests still prove their existing
  guarantees.

## Phase 3: Documentation And Harness Updates

- [ ] Update `internal/kafka/README.md` so it owns broker behavior only.
- [ ] Add `internal/delivery/README.md` because delivery execution is a critical package.
- [ ] Update `AGENTS.md` package map and critical package guidance.
- [ ] Update architecture documentation only if package ownership diagrams mention Kafka as the
  delivery owner.
- [ ] Update `PROGRESS.md` with validation evidence.
- **Verify:** docs describe current code after the move and do not introduce planned behavior as
  implemented behavior.

## Required Validation

- [ ] `GOCACHE=/tmp/dispatch-gocache go test -race ./internal/kafka/... ./internal/delivery/... ./internal/retry/...`
- [ ] `GOCACHE=/tmp/dispatch-gocache go test ./internal/repository/postgres/... ./internal/app/...`
- [ ] `GOCACHE=/tmp/dispatch-gocache go build ./...`
- [ ] `git diff --check`
- [ ] Relative Markdown link validation
- [ ] `make validate-basic` if the code move touches Docker/app assembly behavior

## Open Decisions Before Promotion

1. Does the event envelope move now, or stay in `internal/kafka` until API boundary work?
2. Should `DeliveryObserver` remain in `delivery`, or should `observability` own a narrower adapter
   interface?
3. Which existing Kafka tests become delivery tests, and which remain broker commit/serialization
   tests?

## Progress Log

| Date | Step | Note |
|------|------|------|
| 2026-07-03 | Queued | Created from the concluded internal package-boundaries spike. |
