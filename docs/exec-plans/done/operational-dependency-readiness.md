# Execution Plan: Operational Dependency Readiness

> **Status:** Done
> **Estimated effort:** 1-2 focused engineering sessions
> **Candidate placement:** Before v0.14.0 or as the first slice of v0.14.0 operational readiness
> **Depends on:** Redis distributed max-delivery-rate enforcement

## Goal

Expose truthful role-specific readiness for Dispatch dependencies so deployment infrastructure can
make correct routing and restart decisions without guessing application state.

The application should stay explicit about the difference between:

- liveness: the process is running and should not be restarted only because a dependency is down;
- readiness: this instance can safely perform its role right now;
- delivery safety: when distributed rate limiting is configured but Redis is unavailable, workers
  fail delivery closed instead of falling back to local limits.

## Accepted Contract

- Liveness remains shallow and process-oriented.
- Readiness is role-specific:
  - API readiness checks PostgreSQL and Kafka publishing readiness.
  - Worker readiness checks PostgreSQL, Kafka consumption readiness, and Redis when `REDIS_URL` is
    configured.
- `REDIS_URL` absent means explicit local/dev/single-worker limiter mode.
- `REDIS_URL` present but Redis unavailable means worker readiness fails and delivery decisions fail
  closed.
- The worker process should not exit just because Redis, Kafka, or PostgreSQL is temporarily
  unavailable after startup.
- Deployment infrastructure consumes these signals; it does not own application-specific dependency
  truth.

## Non-goals

- Do not redesign the rate-limiting algorithm.
- Do not replace the local limiter with local sliding-window enforcement in this increment.
- Do not add circuit breakers, semaphores, burst controls, or concurrency controls.
- Do not add new infrastructure services.
- Do not build a complete incident-response or capacity-planning runbook; that belongs to v0.14.0.
- Do not implement cloud-provider-specific health checks.

## Design Questions To Resolve During Implementation

- Production deployments continue to require `REDIS_URL` by documented deployment rule for now; no
  new startup guard is added in this increment.
- Kafka readiness uses broker/topic metadata lookup. It verifies configured Kafka reachability
  without publishing probe messages or consuming records.
- Readiness exposes safe dependency names and statuses in the response body. Raw dependency errors,
  connection strings, credentials, and broker internals stay out of responses.
- Redis readiness is owned by `internal/app` as a worker dependency check. The limiter still owns
  fail-closed delivery decisions.

## Phase 1: Current Health Surface Review

- [x] Inventory current API and worker health/readiness endpoints and their callers.
- [x] Identify which checks are liveness checks and which checks are readiness checks.
- [x] Verify whether API acceptance can return `202` when Kafka publishing is not actually usable.
- [x] Verify whether worker readiness currently observes Kafka, PostgreSQL, and Redis state.
- **Verify:** document the current behavior with file references before changing implementation.

Current behavior found before implementation:

- API exposed `/health` and `/ready`; readiness checked only application state and PostgreSQL.
- Worker Compose healthcheck targeted `/health`, but the worker metrics server only exposed
  Prometheus metrics.
- Kubernetes worker liveness used `/metrics`; no worker readiness probe existed.
- Kafka and Redis dependency state were not part of readiness.

## Phase 2: Define Role-Specific Dependency Checks

- [x] Define a small internal dependency check contract suitable for app wiring.
- [x] Keep checks role-owned in `internal/app`; avoid leaking concrete repository, Kafka, or Redis
  clients into HTTP handlers beyond what the checks need.
- [x] API readiness must check:
  - PostgreSQL connectivity;
  - Kafka publishing path readiness.
- [x] Worker readiness must check:
  - PostgreSQL connectivity;
  - Kafka consumer dependency readiness;
  - Redis connectivity only when `REDIS_URL` is configured.
- [x] Liveness must remain shallow and avoid dependency checks.
- **Verify:** unit tests cover healthy and unhealthy dependency check outcomes without requiring
  real external services.

## Phase 3: Redis Production Semantics

- [x] Make the docs explicit that local rate limiting is dev/single-worker mode, not a trustworthy
  multi-worker production guarantee.
- [x] Preserve the current fail-closed Redis behavior when `REDIS_URL` is configured and Redis is
  unavailable.
- [x] Decide whether to add an explicit production guard such as `REQUIRE_DISTRIBUTED_RATE_LIMIT`;
  decision: defer the guard and keep Redis required by deployment rule for now.
- **Verify:** config/app tests prove absent Redis local mode, configured Redis healthy mode,
  configured Redis unhealthy fail-closed mode, and any production guard behavior.

## Phase 4: HTTP Readiness Behavior

- [x] Update readiness handlers so unhealthy dependencies return a non-2xx status.
- [x] Keep response bodies safe: no secrets, credentials, broker internals, or raw connection URLs.
- [x] Include enough dependency identity for operators to see which subsystem is blocking readiness.
- [x] Ensure API and worker expose only the checks relevant to their role.
- **Verify:** handler tests assert status codes and safe response bodies for healthy and unhealthy
  dependencies.

## Phase 5: Deployment and Documentation

- [x] Update Kubernetes readiness/liveness probe docs to match the new semantics.
- [x] Update Docker Compose and local development docs to explain local limiter mode versus Redis
  distributed mode.
- [x] Update `docs/spec.md`, `docs/architecture.md`, `docs/LIMITATIONS.md`, package READMEs, and
  `PROGRESS.md` with the accepted readiness contract.
- [x] Assess ADR need; no new ADR is required because this increment implements the existing
  deployment-health boundary without changing product architecture.
- **Verify:** current-state docs do not imply that infrastructure alone can infer application
  readiness without app-exposed signals.

## Required Test Matrix

- [x] API readiness returns ready only when PostgreSQL and Kafka publisher dependencies are ready.
- [x] Worker readiness returns ready only when PostgreSQL, Kafka, and configured Redis dependencies
  are ready.
- [x] Liveness remains ready during simulated dependency outages.
- [x] Redis configured but unavailable keeps delivery fail-closed.
- [x] Redis absent keeps explicit local/dev limiter behavior.
- [x] Readiness responses are safe and do not expose connection strings or secrets.

## Final Verification

- [x] `gofmt` on changed Go files.
- [x] `go build ./...`.
- [x] Focused health/readiness tests.
- [x] `go test ./...` where Docker/Testcontainers is available.
- [x] `golangci-lint run ./... --timeout=5m`.
- [x] `git diff --check`.
- [x] Relative Markdown link validation.
- [x] Compose rendering and Kubernetes YAML parse if manifests change.

## Progress Log

| Date | Step | Note |
|------|------|------|
| 2026-06-29 | Plan created | Captures dependency readiness and Redis production semantics as a narrow queued increment. |
| 2026-06-30 | Implemented | Added named readiness checks, API/worker dependency wiring, worker health routes, and Kubernetes probe updates. |
| 2026-06-30 | Verified | Build, full tests, focused health tests, lint, Compose rendering, Kubernetes YAML parse, Markdown links, and diff check passed. |
