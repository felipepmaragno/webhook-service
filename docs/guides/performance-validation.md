# Performance Validation Guide

Dispatch performance characterization is automated. The harness creates clean infrastructure,
applies the required schema, builds the application, seeds each workload, starts timing at a
controlled boundary, verifies durable PostgreSQL state, captures diagnostics, and removes the
stack.

This is a pre-v0.10.0 baseline, not final v1 capacity certification. Repeat the authoritative
tests after v0.11.0 and during v0.14.0 operational readiness.

## Run it

Prerequisites are Go 1.24, Docker with the Compose plugin, `curl`, and standard Linux shell tools.
Run from the repository root.

Quickly verify that the harness and all scenarios work:

```bash
make validate-basic
```

`validate-basic` is the default functional smoke entry point. It reuses the smoke performance
harness because that harness already owns clean setup, readiness checks, deterministic seed data,
PostgreSQL correctness assertions, evidence capture, and cleanup.

Run the same smoke workload through the performance-facing target:

```bash
make perf-smoke
```

Run the complete local baseline:

```bash
make perf-baseline
```

The full baseline generates:

- 10,000 API-accepted events across 1,000 subscriptions;
- a timed drain of those 10,000 events from Kafka through HTTP delivery and PostgreSQL;
- 100,000 already-due retry events across 1,000 subscriptions;
- a timed retry-backlog drain through the real scheduler, receiver, and persistence path.

The command fails when correctness checks fail. The baseline evaluates API acceptance against
its reference target. Kafka delivery is reported as a cold-start drain diagnostic because its
timer includes worker startup and consumer-group rebalance; it is not a sustained-throughput test.

## Results

Each run writes a timestamped directory under:

```text
artifacts/performance/<timestamp>-<mode>/
```

Start with `summary.txt`. It contains acceptance, cold-start delivery drain, and retry drain
rates plus API target status. Supporting evidence includes:

| File | Evidence |
|------|----------|
| `environment.txt` | Git revision, configuration, and tool versions |
| `api-acceptance.log` | Benchmark response count and API acceptance rate |
| `delivery-database-report.txt` | Final event states, attempt count, and remaining leases |
| `retry-database-report.txt` | Final retry states and remaining leases |
| `*-worker-metrics.txt` | Prometheus worker metrics at scenario completion |
| `*-api-metrics.txt` | Prometheus API metrics at scenario completion |
| `*-docker-stats.txt` | Container resource snapshot |
| `*-compose.log` | API, worker, receiver, Kafka, and PostgreSQL logs |

PostgreSQL is the authority for completion. Receiver counters and Prometheus rates are diagnostic
evidence because an HTTP success may occur before a persistence failure and subsequent duplicate.

## What is checked automatically

### API acceptance

- The requested number of events receives a successful API response.
- The benchmark reports no failed sends.
- The API acceptance rate is recorded independently from delivery throughput.

### Kafka backlog delivery

- Events are seeded while the worker is stopped.
- The inactive benchmark consumer group is reset to the earliest seeded offsets.
- Timing starts immediately before the worker starts.
- Every event reaches `delivered` with exactly one attempt against the zero-failure receiver.
- No event remains failed, retrying, throttled, processing, or leased.

### Retry backlog

- Already-due retry rows are inserted directly into PostgreSQL while the worker is stopped.
- Timing starts immediately before the worker starts.
- Every retry reaches `delivered`.
- No waiting, processing, failed, or leased row remains.
- Claim, persistence, and stale-owner failure counters remain zero.

## Reference targets

| Workload | Provisional target |
|----------|--------------------|
| API acceptance | 10,000 accepted events/second |
| Successful delivery | 1,000 sustained deliveries/second; not evaluated by the current cold-start drain |
| Retry recovery | Complete drain with no stuck leases or scheduler failures |

Enable API target enforcement when running on a controlled host:

```bash
STRICT_TARGETS=1 make perf-baseline
```

Do not use strict mode on an unprofiled shared developer machine. A target miss there is capacity
evidence, not necessarily a software defect.

## Configuration

Override one variable at a time to identify causality:

```bash
RECEIVER_LATENCY_MS=500 make perf-baseline
RETRY_BATCH_SIZE=250 RETRY_MAX_CONCURRENT_BATCHES=4 make perf-baseline
DB_MAX_CONNS=50 make perf-baseline
```

Supported controls:

| Variable | Baseline default | Purpose |
|----------|------------------|---------|
| `API_SUBSCRIPTIONS` | `1000` | Number of benchmark subscriptions |
| `EVENTS_PER_SUBSCRIPTION` | `10` | New events routed to each subscription |
| `RETRY_SUBSCRIPTIONS` | `1000` | Subscriptions used by the retry seed |
| `RETRY_EVENTS` | `100000` | Already-due retry rows |
| `CONCURRENCY` | `500` | Concurrent benchmark API requests |
| `RECEIVER_LATENCY_MS` | `100` | Simulated destination latency |
| `DB_MAX_CONNS` | `30` | Worker PostgreSQL pool size |
| `RETRY_BATCH_SIZE` | `100` | Retry claim size |
| `RETRY_MAX_CONCURRENT_BATCHES` | `2` | Retry scheduler capacity |
| `TIMEOUT_SECONDS` | `600` | Per-drain timeout |
| `STRICT_TARGETS` | `0` | Fail when the baseline API target is missed |
| `KEEP_STACK` | `0` | Preserve containers after the run for inspection |
| `RESULTS_DIR` | generated | Override the evidence directory |

For a controlled comparison, run each configuration three times from a quiet host and compare
the median. Do not change several controls in one experiment.

## Investigate a failure

Preserve the stack when the failure needs live inspection:

```bash
KEEP_STACK=1 make perf-smoke
```

Then inspect:

```bash
docker compose ps
docker compose logs dispatch-api dispatch-worker receiver
curl -fsS http://localhost:8081/metrics
```

Useful live interfaces are Prometheus at `http://localhost:9090`, Grafana at
`http://localhost:3000` (`admin` / `admin`), and PostgreSQL through:

```bash
docker compose exec postgres psql -U postgres -d dispatch
```

Clean up a preserved stack with:

```bash
docker compose down -v --remove-orphans
```

## Interpretation

| Observation | First investigation |
|-------------|---------------------|
| API target missed with no errors | API CPU, Kafka producer latency, and request p95 |
| Kafka delivery slow with growing lag | Worker CPU, receiver latency, DB waits, and controls |
| Receiver rate exceeds durable delivery rate | Outcome persistence and duplicate processing |
| Retry oldest age grows at maximum active batches | HTTP and DB latency before raising concurrency |
| Expired claims increase | Lease duration versus real batch processing time |
| Memory or goroutines grow continuously | Missing global bound, blocked calls, or lifecycle leak |
| Zero-failure receiver produces failed rows | Correctness fault; do not interpret throughput |

Historical values in [performance.md](performance.md) came from earlier development stages and
are not current v1 guarantees.
