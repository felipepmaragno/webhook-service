# Execution Plan: Operational Dependency Readiness

> **Status:** Queued; version and execution date are intentionally unassigned
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

- Should production deployments require `REDIS_URL`, or should that remain a documented deployment
  rule for now?
- What is the lowest-risk way to verify Kafka readiness for each role without introducing heavy
  probes or side effects?
- Should readiness expose dependency details in the response body, logs, metrics, or all three?
- Should Redis readiness be checked directly by the worker health endpoint, by the limiter, or by a
  small dependency registry owned by `internal/app`?

## Phase 1: Current Health Surface Review

- [ ] Inventory current API and worker health/readiness endpoints and their callers.
- [ ] Identify which checks are liveness checks and which checks are readiness checks.
- [ ] Verify whether API acceptance can return `202` when Kafka publishing is not actually usable.
- [ ] Verify whether worker readiness currently observes Kafka, PostgreSQL, and Redis state.
- **Verify:** document the current behavior with file references before changing implementation.

## Phase 2: Define Role-Specific Dependency Checks

- [ ] Define a small internal dependency check contract suitable for app wiring.
- [ ] Keep checks role-owned in `internal/app`; avoid leaking concrete repository, Kafka, or Redis
  clients into HTTP handlers beyond what the checks need.
- [ ] API readiness must check:
  - PostgreSQL connectivity;
  - Kafka publishing path readiness.
- [ ] Worker readiness must check:
  - PostgreSQL connectivity;
  - Kafka consumer dependency readiness;
  - Redis connectivity only when `REDIS_URL` is configured.
- [ ] Liveness must remain shallow and avoid dependency checks.
- **Verify:** unit tests cover healthy and unhealthy dependency check outcomes without requiring
  real external services.

## Phase 3: Redis Production Semantics

- [ ] Make the docs explicit that local rate limiting is dev/single-worker mode, not a trustworthy
  multi-worker production guarantee.
- [ ] Preserve the current fail-closed Redis behavior when `REDIS_URL` is configured and Redis is
  unavailable.
- [ ] Decide whether to add an explicit production guard such as `REQUIRE_DISTRIBUTED_RATE_LIMIT`.
- [ ] If a guard is added, keep it small: production can refuse startup when Redis is required but
  `REDIS_URL` is absent.
- **Verify:** config/app tests prove absent Redis local mode, configured Redis healthy mode,
  configured Redis unhealthy fail-closed mode, and any production guard behavior.

## Phase 4: HTTP Readiness Behavior

- [ ] Update readiness handlers so unhealthy dependencies return a non-2xx status.
- [ ] Keep response bodies safe: no secrets, credentials, broker internals, or raw connection URLs.
- [ ] Include enough dependency identity for operators to see which subsystem is blocking readiness.
- [ ] Ensure API and worker expose only the checks relevant to their role.
- **Verify:** handler tests assert status codes and safe response bodies for healthy and unhealthy
  dependencies.

## Phase 5: Deployment and Documentation

- [ ] Update Kubernetes readiness/liveness probe docs to match the new semantics.
- [ ] Update Docker Compose and local development docs to explain local limiter mode versus Redis
  distributed mode.
- [ ] Update `docs/spec.md`, `docs/architecture.md`, `docs/LIMITATIONS.md`, package READMEs, and
  `PROGRESS.md` with the accepted readiness contract.
- [ ] Add or update ADR notes if the production Redis requirement or readiness semantics become a
  durable decision.
- **Verify:** current-state docs do not imply that infrastructure alone can infer application
  readiness without app-exposed signals.

## Required Test Matrix

- [ ] API readiness returns ready only when PostgreSQL and Kafka publisher dependencies are ready.
- [ ] Worker readiness returns ready only when PostgreSQL, Kafka, and configured Redis dependencies
  are ready.
- [ ] Liveness remains ready during simulated dependency outages.
- [ ] Redis configured but unavailable keeps delivery fail-closed.
- [ ] Redis absent keeps explicit local/dev limiter behavior.
- [ ] Readiness responses are safe and do not expose connection strings or secrets.

## Final Verification

- [ ] `gofmt` on changed Go files.
- [ ] `go build ./...`.
- [ ] Focused health/readiness tests.
- [ ] `go test ./...` where Docker/Testcontainers is available.
- [ ] `golangci-lint run ./... --timeout=5m`.
- [ ] `git diff --check`.
- [ ] Relative Markdown link validation.
- [ ] Compose rendering and Kubernetes YAML parse if manifests change.

## Progress Log

| Date | Step | Note |
|------|------|------|
| 2026-06-29 | Plan created | Captures dependency readiness and Redis production semantics as a narrow queued increment. |
