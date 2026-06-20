# HTTP API Boundary

> Local implementation context for engineers and coding agents. Read this file before changing
> handlers, routes, request/response shapes, or subscription administration.

## Authority

`docs/spec.md` owns observable behavior. `docs/product.md` owns users and trust boundaries. ADR 020
owns webhook signatures and secret rotation. This file explains the current package implementation.

## Responsibilities

- `handler.go` decodes business requests, validates current required fields, invokes Kafka or
  repository dependencies, and maps results to HTTP responses.
- `routes.go` assembles Chi routes and the production middleware chain.
- Event ingestion returns `202` only after Kafka publication succeeds; PostgreSQL visibility can lag.
- Subscription administration creates, lists, rotates secrets, and soft-deletes subscriptions.

## Security Invariants

1. Subscription secrets are input-only and must never appear in response DTOs.
2. `SubscriptionResponse` is the public representation; do not serialize `domain.Subscription`.
3. Rotation updates only the active subscription secret. Frozen delivery rows retain the secret
   captured at initialization so retry behavior remains deterministic.
4. The API has no application-level authentication or authorization. Deployment controls own that
   boundary; handlers must not imply otherwise.
5. Repository, Kafka, and secret details must not be returned in error messages.

## Known Boundaries

- Strict bounded JSON decoding, stable machine-readable errors, pagination, URL/SSRF policy, and
  OpenAPI compatibility checks remain in the queued API hardening plan.
- The API-specific subscription repository interface is consumer-owned. Do not expand it with
  worker-only reads.
- Production health/readiness routing belongs to `observability.HealthHandler`; the legacy handler
  health method remains a cleanup candidate in the queued API plan.

## Verification

```bash
go test -race ./internal/api/...
go test ./internal/repository/postgres/...
go test ./internal/app/...
```

Update this file when route ownership, response exposure, secret administration, or API invariants
change.
