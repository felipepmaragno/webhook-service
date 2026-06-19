# Execution Plan: API Contract and Boundary Hardening

> **Status:** Queued; version and execution date are intentionally unassigned
> **Estimated effort:** 3-5 focused engineering sessions; split before promotion if it competes with a roadmap increment
> **Candidate placement:** Secret redaction is assigned to v0.12.0; broader contract work may run before v1.0.0 or remain post-v1
> **Depends on:** An explicit scheduling decision against the finite v1 roadmap

> **Allocation note (2026-06-19):** Subscription-secret redaction and directly related
> response-boundary tests were completed in v0.12.0. The remaining API contract,
> validation, pagination, and compatibility work stays queued and unversioned.

## Goal

Raise the HTTP API from a functional, idiomatic Go interface to a deliberately bounded,
secure, testable, and evolvable public contract.

The current API has good foundations: small `net/http` handlers, Chi routing, context
propagation, injected dependencies, asynchronous `202 Accepted` semantics, structured
observability, server timeouts, and focused handler tests. The main weakness is not handler
readability. It is that the wire boundary is still coupled to domain and persistence types and
does not yet defend inputs, outputs, errors, or future compatibility rigorously.

## Why this matters

Clean handler code is only one part of API quality. A production-quality API must also make
the following properties explicit and mechanically testable:

- which input is accepted and how resource use is bounded;
- which data may leave the process, especially secrets and internal ownership state;
- which status and error semantics clients can depend on;
- how collections remain bounded as history grows;
- how contract changes are reviewed without silently breaking clients;
- how transport concerns remain separate from domain and application lifecycle concerns.

Before v0.12.0, `POST /subscriptions` and `GET /subscriptions` serialized
`domain.Subscription`, whose `secret` field was JSON-visible. V0.12.0 introduced a
secret-free response DTO and negative exposure tests. The broader boundary work in this
plan remains relevant but no longer owns that corrected defect.

## Documentation authority

- `docs/product.md` remains authoritative for users, promises, and product boundaries.
- `docs/spec.md` remains authoritative for observable behavior and invariants.
- An OpenAPI document, if added, is authoritative for machine-readable HTTP paths, payload
  schemas, status codes, and examples; it must not introduce behavior that contradicts the spec.
- ADRs record durable choices such as error format, pagination model, URL policy, and API
  compatibility strategy when those choices have meaningful alternatives.
- This exec plan is temporary execution authority only after it is promoted to `active/`.

## Non-goals

- Do not turn Dispatch into a public multi-tenant API or add application-level authentication
  without a separate product decision.
- Do not add a UI, SDK generation pipeline, GraphQL, or a new HTTP framework.
- Do not expose every domain or database field merely because it is available internally.
- Do not add speculative search, filtering, or versioning mechanisms without a current client or
  operational requirement.
- Do not change Kafka acceptance, delivery, retry, or persistence guarantees as an incidental API
  refactor.

## Acceptance criteria

- [x] Subscription secrets are write-only and never appear in API responses, logs, metrics, or
  contract examples.
- [ ] API request and response DTOs define the wire contract independently from domain and
  persistence structs.
- [ ] JSON request bodies have explicit content type, size, syntax, trailing-data, unknown-field,
  and validation behavior.
- [ ] Subscription URLs, identifiers, filters, payloads, and policy controls are validated against
  documented bounds and security rules.
- [ ] Errors have stable machine-readable codes, safe messages, request correlation, and tested
  status mappings.
- [ ] Growing collection endpoints have deterministic ordering and bounded pagination, or a
  documented evidence-based reason they can remain bounded without pagination.
- [ ] The production router and middleware chain have contract tests; tests do not rely on a
  behaviorally different duplicate router.
- [ ] API dependencies contain only operations the handlers use, and application lifecycle remains
  owned by `internal/app`.
- [ ] A machine-readable API contract is added and checked in CI, unless promotion records a
  deliberate decision to defer it with an alternative compatibility guard.
- [ ] Existing asynchronous acceptance and delivery-state semantics remain unchanged unless the
  product and behavior authorities explicitly approve a change.

## Phase 0: Schedule and Split the Work

- [ ] Reassess this plan against the active v1 increment and feature-freeze rule.
- [x] Pull secret redaction and directly related security tests into v0.12.0 because this full
  plan was not promoted before the security increment.
- [ ] Decide whether to execute as one increment or split into:
  1. data safety and strict input handling;
  2. errors, pagination, and production-router tests;
  3. machine-readable contract and compatibility checks.
- [ ] Record version assignment and dependencies before moving any plan to `active/`.
- **Verify:** every promoted piece closes a v1 criterion, fixes a release-threatening defect, or is
  explicitly deferred until after v1.

## Phase 1: Protect the Response Boundary

- [ ] Add API-specific response DTOs for events, deliveries, and attempts; subscription responses
  were separated in v0.12.0.
- [x] Make subscription secrets write-only and test both creation and listing for redaction.
- [ ] Decide intentionally whether destination URLs, response excerpts, last errors, payload data,
  processing owners, and lease deadlines belong in operator responses.
- [ ] Prevent future domain fields from becoming public automatically through JSON serialization.
- [ ] Keep timestamps and empty collection representation consistent across endpoints.
- **Verify:** golden or schema-oriented response tests prove exactly which fields may leave each
  endpoint, including negative assertions for sensitive/internal fields.

## Phase 2: Harden Request Decoding and Validation

- [ ] Introduce one reusable strict JSON decoder with a documented maximum body size.
- [ ] Require an accepted JSON media type for endpoints with request bodies.
- [ ] Reject malformed JSON, unknown fields, trailing JSON values, and oversized bodies with
  stable client errors.
- [ ] Define and enforce bounds for IDs, event type, source, payload, event filter count/value,
  secret size, and rate-control values.
- [ ] Parse subscription URLs and define allowed schemes, host rules, credential handling,
  fragments, redirects, and private-network behavior.
- [ ] Treat URL validation as part of the outbound SSRF trust boundary, not only syntax checking.
- **Verify:** table-driven tests cover valid boundaries, each rejection class, and repository or
  Kafka non-invocation after invalid input.

## Phase 3: Define Error and Status Semantics

- [ ] Define a stable error envelope with a machine code, safe message, optional field details,
  and request or trace identifier.
- [ ] Map known domain and infrastructure failures deliberately, including duplicate IDs,
  malformed input, unsupported media types, not found, temporary Kafka unavailability, and
  unexpected failures.
- [ ] Ensure internal database, Kafka, network, and secret values never leak through responses.
- [ ] Decide whether empty attempts or deliveries for a missing event are distinguishable from a
  known event with no history.
- **Verify:** contract tests assert status, error code, response media type, and correlation data for
  every supported error class.

## Phase 4: Bound Read APIs

- [ ] Measure or reason from retention rules about the maximum size of subscriptions, deliveries,
  and attempt history.
- [ ] Add deterministic ordering to every collection response.
- [ ] Add cursor or limit-based pagination where collections can grow beyond a safe response size.
- [ ] Define default and maximum page sizes and reject invalid parameters consistently.
- [ ] Add repository queries and indexes only where the accepted pagination contract requires them.
- **Verify:** repository and handler tests cover first, middle, final, empty, and invalid pages with
  stable ordering and no duplicate or skipped records.

## Phase 5: Clarify HTTP and Application Boundaries

- [ ] Narrow the handler's event publisher dependency to publishing only; keep `Close` in
  application lifecycle ownership.
- [ ] Narrow subscription persistence dependencies to the methods used by API handlers.
- [ ] Remove the duplicate handler health endpoint if production routing remains owned by
  `observability.HealthHandler`.
- [ ] Make tests construct the production router or a shared production-equivalent assembly.
- [ ] Add and test `ReadHeaderTimeout`, explicit header limits where justified, panic recovery,
  request/trace propagation, metrics route labels, readiness behavior, and graceful shutdown.
- **Verify:** API, observability, and app tests exercise the real middleware order and server
  assembly without changing current health and metrics contracts accidentally.

## Phase 6: Make Compatibility Mechanical

- [ ] Add an OpenAPI contract for supported business and operational endpoints.
- [ ] Reconcile OpenAPI schemas and examples with `docs/spec.md` and project README examples.
- [ ] Validate the contract and representative responses in CI.
- [ ] Add a breaking-change check or an explicit reviewed compatibility baseline.
- [ ] Document the compatibility policy before introducing URL version prefixes or alternate media
  types; do not version preemptively without a concrete migration need.
- **Verify:** CI fails for an invalid contract, undocumented endpoint/schema drift, or an
  unapproved breaking change.

## Required Test Matrix

- [ ] Handler unit tests cover strict decoding, validation, DTO mapping, redaction, and error maps.
- [ ] Router/component tests cover the real middleware chain, routes, methods, headers, recovery,
  metrics, and correlation IDs.
- [ ] PostgreSQL integration tests cover pagination/order queries and duplicate classification.
- [ ] E2E tests preserve event acceptance, delayed visibility, subscription management, and
  delivery-state queries through real infrastructure.
- [ ] Security-focused tests prove secrets are absent from responses and unsafe URL cases follow
  the accepted policy.
- [ ] Race-gated API and observability tests pass.

## Closure and Final Verification

- [ ] Run formatting, lint, build, unit/component, race, PostgreSQL/Redis integration, and thin E2E
  validation required by the project harness.
- [ ] Update `docs/spec.md` for every changed observable behavior.
- [ ] Update `docs/product.md` only if the users, promise, or trust boundary changes.
- [ ] Update `docs/LIMITATIONS.md` to remove corrected limitations and preserve accepted remaining
  boundaries.
- [ ] Update `docs/v1-roadmap.md` and `docs/next-steps.md` with the actual scheduling and release-gate
  effect.
- [ ] Add `internal/api/README.md` describing current API ownership, invariants, hazards, contract
  verification, and links to durable authorities; add it to `AGENTS.md` package guidance.
- [ ] Update the project `README.md` examples and API references from the accepted contract.
- [ ] Update `PROGRESS.md` with implementation evidence, validation results, remaining risks, and
  the next active plan.
- [ ] Add a learning note explaining why clean handlers are insufficient without a defended wire
  boundary.
- [ ] Move this plan, or each promoted split plan, to `done/` only after its acceptance criteria and
  closure checks pass.

## Promotion Questions

1. Is the complete plan required for the v1 release gate, or only secret redaction and selected
   boundary hardening?
2. Which API data is truly needed by a trusted platform operator, especially payloads, destination
   URLs, receiver response excerpts, errors, and lease ownership?
3. What outbound URL policy matches the accepted single-trust-domain deployment without claiming
   stronger SSRF isolation than the product provides?
4. Which collections must be paginated immediately based on retention and measured usage?
5. Is OpenAPI the accepted wire-schema authority, and what compatibility changes should CI reject?
