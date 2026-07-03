# Minimal Operations Guide

This guide is the v1 local operations entry point. It is intentionally small: Dispatch is a
self-hosted service for one trusted environment, not a managed production platform.

## Prerequisites

- Go 1.24+
- Docker with the Compose plugin
- `curl`
- standard Linux shell tools

Run commands from the repository root.

For local overrides, copy the checked-in template:

```bash
cp .env.example .env
```

Docker Compose reads `.env` automatically, and the Makefile includes it when present.

## One-Command Validation

Use this first when reviewing the project:

```bash
make validate-basic
```

This starts a clean Docker Compose stack, applies the schema, waits for readiness, seeds deterministic
data, validates API-to-worker delivery, validates retry backlog drain, captures evidence, and removes
the stack.

Evidence is written under:

```text
artifacts/performance/<timestamp>-smoke/
```

Start with `summary.txt`, then inspect Compose logs, PostgreSQL reports, metrics snapshots, and
Docker stats in the same directory if something fails.

Preserve the stack for live debugging:

```bash
KEEP_STACK=1 make validate-basic
docker compose ps
docker compose logs dispatch-api dispatch-worker receiver
docker compose down -v --remove-orphans
```

## Manual Local Demo

Start the stack:

```bash
cp .env.example .env  # optional local overrides
make up
```

Seed a normal flow:

```bash
make seed
```

Seed a retry flow:

```bash
make seed-retry
```

Inspect:

| Surface | URL |
|---------|-----|
| API | <http://localhost:8090> |
| API health | <http://localhost:8090/health> |
| API readiness | <http://localhost:8090/ready> |
| Worker health | <http://localhost:8081/health> |
| Worker readiness | <http://localhost:8081/ready> |
| Worker metrics | <http://localhost:8081/metrics> |
| Receiver | <http://localhost:9000> |
| Prometheus | <http://localhost:9090> |
| Grafana | <http://localhost:3000> |

Grafana credentials are `admin` / `admin`.

Clean up:

```bash
make down
```

## Health And Readiness

`/health` is shallow liveness. It should stay successful while the process can serve HTTP, even if a
dependency is temporarily unavailable.

`/ready` is dependency-aware:

- API readiness checks application startup state, PostgreSQL, and Kafka topic metadata.
- Worker readiness checks application startup state, PostgreSQL, Kafka topic metadata, and Redis
  when `REDIS_URL` is configured.

Unready responses return `503` with safe dependency names and statuses. They intentionally do not
include raw connection errors, credentials, broker internals, or connection strings.

## Failure Notes

## Backup, Restore, And Upgrade Boundary

V1 validates a fresh-installation schema and documents runtime recovery behavior. It does not ship a
project-owned backup/restore automation program, perform a restore drill, or support upgrades from
pre-v1 experimental schemas.

Operators are responsible for PostgreSQL backups, Kafka retention policy, Redis availability,
secret protection, restore testing, and deployment rollback procedures appropriate to their
environment. Treat `make validate-basic` as application validation, not disaster-recovery
validation.

### PostgreSQL Unavailable

Expected behavior:

- API readiness fails.
- Worker readiness fails.
- API writes, status queries, delivery persistence, retry claims, and retention cleanup cannot
  complete safely.

Inspect:

- `/ready` on API and worker.
- `docker compose logs postgres dispatch-api dispatch-worker`.
- `dispatch_worker_retry_claim_failures_total`.
- `dispatch_worker_retry_persistence_failures_total`.

Recovery expectation:

- Restore PostgreSQL connectivity.
- API and worker readiness should recover without restarting the whole stack.
- Kafka messages whose outcomes were not persisted remain eligible for redelivery because offsets
  are committed only after durable outcome persistence.

### Kafka Unavailable

Expected behavior:

- API readiness fails because event acceptance depends on publishing to Kafka.
- Worker readiness fails because workers cannot verify topic metadata.
- Existing retry-poller work from PostgreSQL may still be structurally present, but the service is
  not considered ready while Kafka is unavailable.

Inspect:

- `/ready` on API and worker.
- `docker compose logs kafka dispatch-api dispatch-worker`.
- `kafka_consumergroup_lag` after Kafka recovers.

Recovery expectation:

- Restore Kafka and the `events.pending` topic.
- Readiness should recover.
- Producers should retry event submission at the client side if the API rejected requests while
  unready.

### Redis Unavailable

Expected behavior:

- Worker readiness fails only when `REDIS_URL` is configured.
- Delivery rate-limit decisions fail closed as `throttled`.
- No HTTP attempt is consumed for Redis-denied or Redis-unavailable decisions.
- Backlog can grow until Redis recovers.

Inspect:

- Worker `/ready`.
- `docker compose logs redis dispatch-worker`.
- `dispatch_worker_events_throttled_total`.
- `dispatch_worker_rate_limiter_rejections_total`.
- retry due/oldest-age metrics.

Recovery expectation:

- Restore Redis connectivity.
- Worker readiness should recover.
- Throttled work retries through the normal retry path.

## Capacity Smoke

Run the small capacity smoke:

```bash
make perf-smoke
```

This uses the same harness as `validate-basic` but is named for performance characterization. It
records API acceptance, Kafka cold-start delivery drain, and retry backlog drain for a small dataset.

For a larger local baseline:

```bash
make perf-baseline
```

Treat all results as environment-specific evidence, not an SLO. Record the generated
`environment.txt` and `summary.txt` with any claim.
