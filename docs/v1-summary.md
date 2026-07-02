# Dispatch V1 Summary

Dispatch v1 is a production-conscious, self-hosted webhook delivery service for one trusted
environment. It accepts events through an HTTP API, publishes them to Kafka, freezes matching
subscriptions into durable delivery rows, and workers deliver those rows as HTTP webhooks.

The v1 promise is reliability and understandable recovery, not broad product scope. Dispatch keeps
retryable work recoverable, records per-destination outcomes, exposes operational signals, and makes
its limitations explicit.

## What V1 Guarantees

- Event acceptance is asynchronous: `202 Accepted` means Kafka accepted the event, not that a
  receiver accepted a webhook.
- Delivery is at least once. Receivers must deduplicate with `X-Event-ID` when duplicate calls
  matter.
- Each event freezes a stable destination set when the worker initializes delivery rows.
- Each event/subscription delivery has independent status, attempts, retry lease, replay generation,
  and frozen destination policy.
- Kafka offsets are committed only after delivery work reaches a durable PostgreSQL boundary.
- Retry claims are owner and deadline fenced, so stale workers cannot overwrite newer ownership.
- Failed deliveries can be replayed deliberately without editing the database.
- Webhook signatures use timestamped HMAC-SHA256 over the exact transmitted body when a subscription
  secret is configured.
- Redis-backed `max_delivery_rate` enforces subscription-scoped delivery rate across workers when
  Redis is configured.
- Health, readiness, metrics, dashboards, structured logs, and the validation harness expose the
  main operational state.

## What V1 Does Not Guarantee

- No exactly-once HTTP delivery.
- No FIFO, per-key, or cross-event ordering.
- No API authentication, authorization, tenant identity, or customer isolation.
- No managed-service contract, UI, billing, quotas, or support model.
- No payload transformation, receiver batching, destination verification, or multi-region behavior.
- No normalized destination identity: `max_delivery_rate` is scoped to subscription ID, not URL,
  host, or external server.
- No supported upgrade from pre-v1 experimental schemas. V1 is validated as a fresh-installation
  baseline.
- No built-in backup/restore automation or performed restore drill. Operators own datastore backup,
  restore, and disaster-recovery procedures.

## Validate Locally

Use the automated smoke harness first:

```bash
make validate-basic
```

For release review, run the full matrix:

```bash
GOCACHE=/tmp/dispatch-gocache go build ./...
GOCACHE=/tmp/dispatch-gocache go test ./...
GOCACHE=/tmp/dispatch-gocache go test -race ./internal/api/... ./internal/config/... ./internal/domain/... ./internal/kafka/... ./internal/observability/... ./internal/retention/... ./internal/retry/...
GOCACHE=/tmp/dispatch-gocache GOLANGCI_LINT_CACHE=/tmp/dispatch-golangci-lint-cache /tmp/dispatch-bin/golangci-lint run ./... --timeout=5m
make validate-basic
docker compose -f docker-compose.yaml config
docker compose -f docker-compose.benchmark.yaml config
docker compose -f docker-compose.kafka.yaml config
yq eval '.' k8s/*.yaml
git diff --check
```

The current verified state and latest command results are recorded in
[PROGRESS.md](../PROGRESS.md). Operational walkthroughs and failure notes live in
[operations.md](operations.md).

## Deferred Post-V1 Work

Deferred ideas remain optional future directions, not required completion work:

- API contract hardening;
- internal package-boundary refactoring;
- multi-tenancy or managed-service capabilities;
- stronger destination modeling and destination-level rate budgets;
- richer destination-protection algorithms;
- backup/restore automation and migration rollback drills;
- Kafka outcome-topic architecture.

See [limitations.md](limitations.md), [next-steps.md](next-steps.md), and
[spikes/](spikes/) for the preserved reasoning.
