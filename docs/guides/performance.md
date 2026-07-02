# Performance & Load Test Report

> This file contains historical measurements from several development stages. They are not
> current v1 capacity guarantees. Use the repeatable [performance validation guide](performance-validation.md)
> for new baseline runs and retain the environment and raw evidence with every result.

## Current V1 Capacity Evidence

Use `make validate-basic` as the reviewer-facing full-stack validation command. It runs the smoke
performance harness and proves the basic delivery and retry flows with PostgreSQL assertions.

Use `make perf-smoke` when the intent is explicitly capacity characterization. It records:

- API acceptance for a small deterministic dataset;
- Kafka cold-start backlog drain through the worker and receiver;
- due retry backlog drain through the retry scheduler;
- environment metadata and raw evidence under `artifacts/performance/`.

Use `make perf-baseline` only on a machine where a larger local run is acceptable. Baseline results
must include the generated `environment.txt` and `summary.txt`. They are bounded evidence for that
machine and configuration, not a Dispatch SLO.

### V0.14 Smoke Evidence - June 30, 2026

Command:

```bash
make validate-basic
```

Evidence directory:

```text
artifacts/performance/20260630T192431Z-smoke/
```

Environment:

- Go `go1.24.0 linux/amd64`
- Docker client/server `29.6.1`
- Docker Compose `v5.2.0`
- smoke dataset: 10 subscriptions, 100 accepted events, 200 due retry events
- receiver latency: 100ms
- benchmark concurrency: 500
- worker database pool: 30
- retry batch size: 100
- retry max concurrent batches: 2

Observed smoke results:

| Workload | Result |
|----------|--------|
| API acceptance | 100/100 events accepted at 462 events/s |
| Kafka cold-start backlog drain | 100/100 delivered, 100 attempts, zero remaining leases |
| Kafka cold-start diagnostic rate | 5.18 events/s over 19.323s |
| Retry backlog drain | 200/200 delivered, zero remaining leases |
| Retry diagnostic rate | 13.57 events/s over 14.740s |

Interpretation:

- This run validates the full-stack smoke path and the current performance harness against the
  per-delivery runtime model.
- The Kafka delivery rate includes worker startup and consumer-group catch-up, so it is a diagnostic
  cold-start drain rate, not sustained delivery throughput.
- The smoke dataset is intentionally small and is not evaluated against API throughput targets.
- Treat these numbers as local evidence for the recorded environment, not a product SLO.

**Date:** January 21, 2026  
**Environment:** Docker Compose. Older runs included Redis; current v1 runs use Kafka and PostgreSQL.

## Load Test Results

### Test Configuration

```javascript
// k6 load test (scripts/loadtest.js)
stages: [
  { duration: '10s', target: 10 },   // Ramp up
  { duration: '30s', target: 50 },   // Hold at 50 VUs
  { duration: '10s', target: 0 },    // Ramp down
]

thresholds: {
  http_req_duration: ['p(95)<500'],  // 95% under 500ms
  success_rate: ['rate>0.99'],        // 99% success
  http_req_failed: ['rate<0.01'],     // <1% failures
}
```

### Results: PostgreSQL Polling (Before Kafka)

| Configuration | Throughput | Latency | Notes |
|---------------|------------|---------|-------|
| 1 instance, 10 workers | **6,361 req/s** | 15ms | Baseline |
| 3 instances, 30 workers | 3,006 req/s | 33ms | ❌ Lock contention |

**Problem:** Adding more instances **decreased** throughput due to `FOR UPDATE SKIP LOCKED` contention.

### Results: Kafka Architecture (Current)

| Metric | Value |
|--------|-------|
| Event ingestion (API → Kafka) | **~100,000 events/s** |
| Delivery throughput | ~8,000 delivered in ~30s |
| Success rate | 82-99% (depends on destination) |

### Scalability Test: Events per Subscription

| Subscriptions | Delivered | Retry | Success Rate |
|---------------|-----------|-------|--------------|
| 1,000 | 1,000 | 0 | **100%** |
| 5,000 | 4,982 | 18 | **99.6%** |
| 10,000 | 8,221 | 1,779 | 82% |

> **Note:** Retries at 10k subscriptions were due to destination (httpbin.org) rate limiting, not system limitations.

### CI Load Test (GitHub Actions)

```
k6 run --vus 20 --duration 15s scripts/loadtest.js
```

| Metric | Target | Actual |
|--------|--------|--------|
| p(95) latency | <500ms | ✅ ~50ms |
| Success rate | >99% | ✅ 100% |
| HTTP failures | <1% | ✅ 0% |

### End-to-End Benchmark (Local Receiver)

**Test setup:**
- Local webhook receiver with ~100ms latency (simulating real-world conditions)
- 3 worker replicas
- 12 Kafka partitions

```bash
./scripts/benchmark-e2e.sh <subscriptions> <events_per_sub>
```

**Results (January 21, 2026):**

| Subscriptions | Events | Delivered | Success Rate | Ingestion Rate |
|---------------|--------|-----------|--------------|----------------|
| 1,000 | 1,000 | 1,000 | **100%** | 2,237 events/s |
| 5,000 | 5,000 | 5,000 | **100%** | 4,183 events/s |

### Stress Test (Delivery Throughput)

Measures actual **delivery throughput** - HTTP requests delivered per second.

```bash
./scripts/stress-test.sh <subscriptions> <events_per_sub>
```

**Results (January 22-23, 2026):**

| Subscriptions | Total Events | Delivered | Success Rate | **Delivery Throughput** |
|---------------|--------------|-----------|--------------|-------------------------|
| 1,000 | 10,000 | 10,000 | **100%** | **15,760 events/s** |
| 5,000 | 50,000 | 50,000 | **100%** | **8,938 events/s** |

**Peak receiver throughput observed:** **3,743 req/s**

### Performance Optimizations Applied

Two key optimizations increased throughput by **~10x**:

#### 1. Batch INSERT for Events (4.5x improvement)

**Before:** Sequential `INSERT` per event in a loop
```go
for _, evt := range eventsToCreate {
    h.eventRepo.Create(ctx, evt)  // One INSERT per event
}
```

**After:** Single batch `INSERT` with multiple VALUES
```go
h.eventRepo.CreateBatch(ctx, eventsToCreate)  // One INSERT for all events
```

**Results:**
| Events | Before | After | Improvement |
|--------|--------|-------|-------------|
| 50,000 | 1,394/s | 6,216/s | **4.5x** |

#### 2. HTTP Connection Pool Tuning (1.4x improvement)

**Before:** Default `http.Client` with `MaxIdleConnsPerHost=2`
```go
httpClient: &http.Client{Timeout: config.HTTPTimeout}
```

**After:** Configured Transport matching concurrency limits
```go
transport := &http.Transport{
    MaxIdleConns:        1000,
    MaxIdleConnsPerHost: 100,
    IdleConnTimeout:     90 * time.Second,
}
```

**Results (empirical testing):**
| MaxIdleConnsPerHost | Throughput |
|---------------------|------------|
| 10 | 6,250/s |
| 50 | 6,216/s |
| **100** | **8,938/s** |
| 500 | 8,935/s |

The optimal value in that historical run was 100 idle connections per host.

### What is NOT a Bottleneck

Tested and confirmed these are **not** limiting factors:

| Component | Test | Result |
|-----------|------|--------|
| PostgreSQL pool size | 5 vs 30 connections | No difference (~6,100/s both) |
| Destination limiter | Historical Redis comparison | Minimal impact in that run |

**Analysis:**
- Delivery throughput scales with number of subscriptions (more parallelism)
- With 100ms receiver latency, theoretical max per subscription = 10 events/s
- 5,000 subscriptions × 10 events/s = 50,000 events/s theoretical
- Actual ~9,000 events/s due to Kafka batching and goroutine scheduling overhead

**Throughput analysis:**

The historical benchmark created one goroutine per event with no global limit. Current v1 still
depends on worker, HTTP, Kafka, and PostgreSQL capacity, while destination protection is expressed
as `max_delivery_rate`.

```
Concurrency model:
- 1 event to sub A + 1 event to sub B = 2 parallel goroutines
- 100 events to sub A = bounded by worker scheduling, HTTP transport, and destination rate checks
- 1000 events to 1000 different subs = 1000 parallel goroutines
```

**Theoretical max (N different subscriptions, 100ms latency):**
```
Batch of N events → N parallel goroutines → all complete in ~100ms
Throughput = N events / 0.1s = N × 10 events/s
```

With 1,000 different subscriptions: **10,000 events/s theoretical**

**Measured throughput with parallel producer:**

| Concurrency | Subscriptions | Ingestion Rate |
|-------------|---------------|----------------|
| 200 | 1,000 | 2,237 events/s |
| 500 | 5,000 | 4,183 events/s |

The system scales with more concurrent requests. The limit is network/Kafka throughput, not the application.

## Performance Characteristics

### Architecture Performance Features

1. **Kafka-based Event Queue**
   - Horizontal scaling via consumer groups
   - Partitioned by event type for parallelism
   - Manual offset commit for at-least-once delivery

2. **Destination Protection**
   - Local per-subscription `max_delivery_rate` guardrail
   - `throttled` outcomes do not consume HTTP attempts

3. **Worker Parallelism**
   - Kafka partitions and worker replicas provide parallelism
   - HTTP connection pooling and receiver latency remain practical bounds

4. **Intelligent Retry**
   - Permanent failures (4xx) → No retry
   - Retryable failures (5xx, 408, 429) → Exponential backoff
   - Max 5 attempts with jitter

### Bottleneck Analysis

| Component | Potential Bottleneck | Mitigation |
|-----------|---------------------|------------|
| Kafka Consumer | Single consumer per worker | Scale workers horizontally |
| HTTP Delivery | Network latency | Concurrent deliveries (100/subscription) |
| PostgreSQL | Write throughput | Batch inserts, connection pooling |
| Destination limiter | Local guardrail precision | Measure effective throughput under worker scale |

## Key Findings

### Why Kafka?

The migration from PostgreSQL polling to Kafka solved the **horizontal scaling problem**:

| Approach | 1 Instance | 3 Instances | Scaling |
|----------|------------|-------------|---------|
| PostgreSQL `FOR UPDATE SKIP LOCKED` | 6,361/s | 3,006/s | ❌ Negative |
| Kafka consumer groups | ~33k/s | ~100k/s | ✅ Linear |

### Bottlenecks Identified

1. **Destination capacity** — At 10k subscriptions, httpbin.org became the bottleneck (82% success)
2. **Not the system** — Internal throughput exceeds 100k events/s

### Recommendations

1. **For high-volume deployments:** Use dedicated webhook receivers, not shared services like httpbin
2. **For scaling:** Add more Kafka partitions and worker instances
3. **For reliability:** Monitor throttled totals, retry backlog age, stale claims, and terminal failures

## Conclusion

The Kafka-based architecture achieves:
- **~100k events/s** ingestion throughput
- **~16k events/s** delivery throughput (with 100ms receiver latency)
- **Linear horizontal scaling** via consumer groups
- **99%+ success rate** when destinations can handle the load
- **p95 latency < 50ms** for event ingestion

### Key Optimizations Summary

| Optimization | Impact | Details |
|--------------|--------|---------|
| Batch INSERT | **4.5x** | Single INSERT with multiple VALUES vs loop |
| HTTP Pool Tuning | **1.4x** | MaxIdleConnsPerHost=100 in the historical benchmark |
| **Combined** | **~10x** | From ~1,400/s to ~16,000/s |

## Retry Scheduler Capacity - June 14, 2026

A deterministic scheduler benchmark isolates claim/drain behavior from PostgreSQL and
HTTP variance. It uses 20 full batches of five events, a two-millisecond synthetic batch
processor, and a one-hour poll interval so all progress after the first claim must come
from immediate backlog draining.

Command:

```bash
go test ./internal/retry/... -run '^$' \
  -bench BenchmarkPollerBacklogDrain -benchtime=5x -count=1
```

Environment: Linux/amd64, Intel i5-1335U, Go 1.24 toolchain.

| Concurrent batch slots | Time for 100-event synthetic backlog | Relative result |
|------------------------|--------------------------------------|-----------------|
| 1 | 44.3 ms/op | Baseline |
| 4 | 11.2 ms/op | Approximately 4.0x faster |

This result demonstrates that bounded concurrency can improve drain rate when batch work
is parallelizable. It is not an end-to-end throughput claim: real performance is bounded
by PostgreSQL connections, destination rate and concurrency policy, HTTP latency, event
fan-out, and worker resources. `RETRY_POLL_INTERVAL` controls idle discovery; it does not
limit sustained draining after a full claim.

## Automated Baseline - June 15, 2026

The automated local harness ran successfully on commit `af4b694` with one worker, 12 Kafka
partitions, a 100 ms local receiver, retry batch size 100, and two concurrent retry batches.

| Scenario | Result | Integrity result |
|----------|--------|------------------|
| API acceptance, 10,000 events | 11,777 events/s | 10,000 successful API responses |
| Kafka cold-start backlog drain | 799.68 events/s over 12.505 s | 10,000 delivered, 10,000 attempts, zero leases |
| Due retry backlog drain | 957.58 events/s over 104.430 s | 100,000 delivered, zero leases or scheduler failures |

The API acceptance reference of 10,000 events/s was met. The Kafka result includes process
startup and consumer-group rebalance and therefore must not be compared directly with the
1,000/s sustained-delivery objective. A future steady-state workload must evaluate that target.

Run `make perf-smoke` to validate the harness or `make perf-baseline` to repeat this baseline.
Generated evidence is written under `artifacts/performance/` and intentionally ignored by Git.
