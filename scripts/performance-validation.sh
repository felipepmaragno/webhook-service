#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT_DIR"

MODE=${1:-baseline}
case "$MODE" in
  smoke)
    API_SUBSCRIPTIONS=${API_SUBSCRIPTIONS:-10}
    EVENTS_PER_SUBSCRIPTION=${EVENTS_PER_SUBSCRIPTION:-10}
    RETRY_EVENTS=${RETRY_EVENTS:-200}
    RETRY_SUBSCRIPTIONS=${RETRY_SUBSCRIPTIONS:-10}
    ;;
  baseline)
    API_SUBSCRIPTIONS=${API_SUBSCRIPTIONS:-1000}
    EVENTS_PER_SUBSCRIPTION=${EVENTS_PER_SUBSCRIPTION:-10}
    RETRY_EVENTS=${RETRY_EVENTS:-100000}
    RETRY_SUBSCRIPTIONS=${RETRY_SUBSCRIPTIONS:-1000}
    ;;
  *)
    echo "usage: $0 [smoke|baseline]" >&2
    exit 2
    ;;
esac

CONCURRENCY=${CONCURRENCY:-500}
RECEIVER_LATENCY_MS=${RECEIVER_LATENCY_MS:-100}
POLL_INTERVAL_SECONDS=${POLL_INTERVAL_SECONDS:-2}
TIMEOUT_SECONDS=${TIMEOUT_SECONDS:-600}
API_TARGET_RPS=${API_TARGET_RPS:-10000}
STRICT_TARGETS=${STRICT_TARGETS:-0}
KEEP_STACK=${KEEP_STACK:-0}
RUN_ID=${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}
RESULTS_DIR=${RESULTS_DIR:-artifacts/performance/$RUN_ID-$MODE}
TOTAL_EVENTS=$((API_SUBSCRIPTIONS * EVENTS_PER_SUBSCRIPTION))

mkdir -p "$RESULTS_DIR"
RESULTS_DIR=$(cd "$RESULTS_DIR" && pwd)
SUMMARY_FILE="$RESULTS_DIR/summary.txt"
COMPOSE=(docker compose)

log() {
  printf '[performance] %s\n' "$*" | tee -a "$SUMMARY_FILE"
}

fail() {
  log "FAIL: $*"
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

compose() {
  "${COMPOSE[@]}" "$@"
}

psql_value() {
  compose exec -T postgres psql -v ON_ERROR_STOP=1 -U postgres -d dispatch -Atc "$1" | tr -d '[:space:]'
}

psql_report() {
  compose exec -T postgres psql -v ON_ERROR_STOP=1 -U postgres -d dispatch -c "$1"
}

elapsed_seconds() {
  awk -v start="$1" -v end="$2" 'BEGIN { printf "%.3f", end - start }'
}

rate_per_second() {
  awk -v count="$1" -v seconds="$2" 'BEGIN { if (seconds <= 0) print 0; else printf "%.2f", count / seconds }'
}

target_status() {
  awk -v actual="$1" -v target="$2" 'BEGIN { print (actual >= target) ? "MET" : "MISSED" }'
}

report_acceptance_target() {
  local actual=$1
  if [[ "$MODE" == "smoke" ]]; then
    log "API acceptance: $actual events/s [NOT EVALUATED: smoke dataset]"
    return
  fi
  local status
  status=$(target_status "$actual" "$API_TARGET_RPS")
  log "API acceptance: $actual events/s; target $API_TARGET_RPS [$status]"
}

wait_for_url() {
  local name=$1
  local url=$2
  local deadline=$((SECONDS + 180))
  until curl -fsS "$url" >/dev/null 2>&1; do
    (( SECONDS >= deadline )) && fail "$name did not become ready: $url"
    sleep 2
  done
}

capture_evidence() {
  local scenario=$1
  compose ps >"$RESULTS_DIR/$scenario-compose-ps.txt" 2>&1 || true
  docker stats --no-stream >"$RESULTS_DIR/$scenario-docker-stats.txt" 2>&1 || true
  curl -fsS http://localhost:8081/metrics >"$RESULTS_DIR/$scenario-worker-metrics.txt" 2>/dev/null || true
  curl -fsS http://localhost:8090/metrics >"$RESULTS_DIR/$scenario-api-metrics.txt" 2>/dev/null || true
  compose logs --no-color >"$RESULTS_DIR/$scenario-compose.log" 2>&1 || true
}

cleanup() {
  local exit_code=$?
  capture_evidence final
  if [[ "$KEEP_STACK" == "1" ]]; then
    log "stack preserved because KEEP_STACK=1"
  else
    compose down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  exit "$exit_code"
}
trap cleanup EXIT

clean_stack() {
  compose down -v --remove-orphans >/dev/null 2>&1 || true
}

start_stack() {
  local start_worker=$1
  clean_stack
  log "starting clean infrastructure"
  compose up -d postgres redis kafka kafka-init >"$RESULTS_DIR/compose-up-infrastructure.log" 2>&1

  local postgres_deadline=$((SECONDS + 120))
  until compose exec -T postgres pg_isready -U postgres >/dev/null 2>&1; do
    (( SECONDS >= postgres_deadline )) && fail "PostgreSQL did not become ready"
    sleep 2
  done

  compose exec -T postgres psql -v ON_ERROR_STOP=1 -U postgres -d dispatch \
    <migrations/003_add_retry_claim_lease.up.sql \
    >"$RESULTS_DIR/migration-003.log" 2>&1
  compose exec -T postgres psql -v ON_ERROR_STOP=1 -U postgres -d dispatch \
    <migrations/004_add_subscription_policy_controls.up.sql \
    >"$RESULTS_DIR/migration-004.log" 2>&1
  compose exec -T postgres psql -v ON_ERROR_STOP=1 -U postgres -d dispatch \
    <migrations/005_add_delivery_identity.up.sql \
    >"$RESULTS_DIR/migration-005.log" 2>&1

  log "building and starting instrumented application stack"
  compose up --build -d dispatch-api receiver kafka-exporter prometheus grafana \
    >"$RESULTS_DIR/compose-up-application.log" 2>&1

  if [[ "$start_worker" == "1" ]]; then
    compose up --build -d dispatch-worker >>"$RESULTS_DIR/compose-up-application.log" 2>&1
  fi

  wait_for_url API http://localhost:8090/ready
  wait_for_url receiver http://localhost:9000/health
  wait_for_url Prometheus http://localhost:9090/-/ready
  wait_for_url Grafana http://localhost:3000/api/health

  # Prometheus depends on the worker, so Compose starts it transitively. Stop it
  # before seeding to keep the measurement boundary deterministic.
  if [[ "$start_worker" == "0" ]]; then
    compose stop dispatch-worker >/dev/null
  fi

  curl -fsS -X POST http://localhost:9000/control \
    -H 'Content-Type: application/json' \
    -d "{\"fail_rate\":0,\"latency_ms\":$RECEIVER_LATENCY_MS}" \
    >"$RESULTS_DIR/receiver-control.txt"
}

wait_for_terminal_events() {
  local prefix=$1
  local expected=$2
  local deadline=$((SECONDS + TIMEOUT_SECONDS))
  local terminal=0
  while (( terminal < expected )); do
    (( SECONDS >= deadline )) && fail "timed out waiting for $prefix: $terminal/$expected terminal"
    terminal=$(psql_value "
      SELECT COUNT(*) FROM events
      WHERE id LIKE '$prefix%'
        AND status IN ('delivered', 'failed');")
    log "$prefix terminal events: $terminal/$expected"
    (( terminal >= expected )) && break
    sleep "$POLL_INTERVAL_SECONDS"
  done
}

reset_consumer_group_to_earliest() {
  compose exec -T kafka /opt/kafka/bin/kafka-consumer-groups.sh \
    --bootstrap-server kafka:9092 \
    --group dispatch-workers \
    --topic events.pending \
    --reset-offsets \
    --to-earliest \
    --execute \
    >"$RESULTS_DIR/kafka-offset-reset.txt"
}

record_environment() {
  {
    echo "mode=$MODE"
    echo "git_sha=$(git rev-parse HEAD)"
    echo "git_status=$(git status --short | tr '\n' ';')"
    echo "timestamp_utc=$(date -u +%FT%TZ)"
    echo "api_subscriptions=$API_SUBSCRIPTIONS"
    echo "events_per_subscription=$EVENTS_PER_SUBSCRIPTION"
    echo "total_events=$TOTAL_EVENTS"
    echo "retry_subscriptions=$RETRY_SUBSCRIPTIONS"
    echo "retry_events=$RETRY_EVENTS"
    echo "concurrency=$CONCURRENCY"
    echo "receiver_latency_ms=$RECEIVER_LATENCY_MS"
    echo "retry_batch_size=${RETRY_BATCH_SIZE:-100}"
    echo "retry_max_concurrent_batches=${RETRY_MAX_CONCURRENT_BATCHES:-2}"
    echo "db_max_conns=${DB_MAX_CONNS:-30}"
    go version
    docker version --format 'docker_client={{.Client.Version}} docker_server={{.Server.Version}}'
    docker compose version
  } >"$RESULTS_DIR/environment.txt"
  lscpu >"$RESULTS_DIR/lscpu.txt" 2>&1 || true
  free -h >"$RESULTS_DIR/memory.txt" 2>&1 || true
}

run_acceptance_and_delivery() {
  start_stack 0
  log "running API acceptance: $TOTAL_EVENTS events"
  local benchmark_log="$RESULTS_DIR/api-acceptance.log"
  go run ./scripts/benchmark/main.go \
    -subs "$API_SUBSCRIPTIONS" \
    -events "$EVENTS_PER_SUBSCRIPTION" \
    -concurrency "$CONCURRENCY" \
    -wait 0 \
    -api http://localhost:8090 \
    -receiver http://receiver:9000/webhook \
    | tee "$benchmark_log"

  if grep -q 'WARNING: .* events failed to send' "$benchmark_log"; then
    fail "API benchmark reported failed sends"
  fi

  local accepted
  local acceptance_rate
  accepted=$(awk '/Events sent:/ {print $3; exit}' "$benchmark_log")
  acceptance_rate=$(awk '/Throughput:/ {print $2; exit}' "$benchmark_log")
  [[ "$accepted" == "$TOTAL_EVENTS" ]] || fail "accepted $accepted events, expected $TOTAL_EVENTS"

  report_acceptance_target "$acceptance_rate"

  # A new kafka-go consumer group defaults to the topic's latest offset when it
  # has no committed position. Initialize the inactive benchmark group at the
  # earliest offsets so the pre-seeded backlog is part of the timed drain.
  reset_consumer_group_to_earliest

  log "starting worker and measuring seeded Kafka backlog drain"
  local start end seconds delivery_rate
  start=$(date +%s.%N)
  compose start dispatch-worker >>"$RESULTS_DIR/compose-up-application.log" 2>&1
  wait_for_url worker http://localhost:8081/metrics
  wait_for_terminal_events 'bench-evt-' "$TOTAL_EVENTS"
  end=$(date +%s.%N)
  seconds=$(elapsed_seconds "$start" "$end")
  delivery_rate=$(rate_per_second "$TOTAL_EVENTS" "$seconds")

  psql_report "
    SELECT status, COUNT(*) FROM events
    WHERE id LIKE 'bench-evt-%' GROUP BY status ORDER BY status;
    SELECT COUNT(*) AS attempts, ROUND(AVG(duration_ms), 2) AS avg_ms,
           MAX(duration_ms) AS max_ms
    FROM delivery_attempts WHERE event_id LIKE 'bench-evt-%';
    SELECT COUNT(*) AS leases_remaining FROM events
    WHERE id LIKE 'bench-evt-%'
      AND (processing_owner IS NOT NULL OR processing_deadline IS NOT NULL);" \
    | tee "$RESULTS_DIR/delivery-database-report.txt"

  local delivered failed waiting processing leases attempts
  delivered=$(psql_value "SELECT COUNT(*) FROM events WHERE id LIKE 'bench-evt-%' AND status='delivered';")
  failed=$(psql_value "SELECT COUNT(*) FROM events WHERE id LIKE 'bench-evt-%' AND status='failed';")
  waiting=$(psql_value "SELECT COUNT(*) FROM events WHERE id LIKE 'bench-evt-%' AND status IN ('retrying','throttled');")
  processing=$(psql_value "SELECT COUNT(*) FROM events WHERE id LIKE 'bench-evt-%' AND status='processing';")
  leases=$(psql_value "SELECT COUNT(*) FROM events WHERE id LIKE 'bench-evt-%' AND (processing_owner IS NOT NULL OR processing_deadline IS NOT NULL);")
  attempts=$(psql_value "SELECT COUNT(*) FROM delivery_attempts WHERE event_id LIKE 'bench-evt-%';")

  [[ "$delivered" == "$TOTAL_EVENTS" ]] || fail "delivered $delivered events, expected $TOTAL_EVENTS"
  [[ "$failed" == "0" && "$waiting" == "0" && "$processing" == "0" ]] || fail "non-delivered terminal state detected"
  [[ "$leases" == "0" ]] || fail "$leases delivery leases remain"
  [[ "$attempts" == "$TOTAL_EVENTS" ]] || fail "recorded $attempts attempts, expected $TOTAL_EVENTS"

  log "Kafka cold-start backlog drain: $delivery_rate events/s over ${seconds}s [DIAGNOSTIC]"
  capture_evidence delivery

  if [[ "$STRICT_TARGETS" == "1" && "$MODE" == "baseline" ]]; then
    local acceptance_target
    acceptance_target=$(target_status "$acceptance_rate" "$API_TARGET_RPS")
    if [[ "$acceptance_target" == "MISSED" ]]; then
      fail "API acceptance target was missed with STRICT_TARGETS=1"
    fi
  fi
}

run_retry_backlog() {
  start_stack 0
  log "seeding retry backlog: $RETRY_EVENTS events across $RETRY_SUBSCRIPTIONS subscriptions"
  compose exec -T postgres psql -v ON_ERROR_STOP=1 -U postgres -d dispatch -c "
    INSERT INTO subscriptions (id, url, event_types, rate_limit)
    SELECT 'perf-retry-sub-' || i,
           'http://receiver:9000/webhook',
           ARRAY['perf.retry.' || i],
           100
    FROM generate_series(1, $RETRY_SUBSCRIPTIONS) AS i;

    INSERT INTO events (
      id, type, source, data, status, attempts, max_attempts,
      next_attempt_at, created_at, updated_at
    )
    SELECT 'perf-retry-event-' || i,
           'perf.retry.' || (((i - 1) % $RETRY_SUBSCRIPTIONS) + 1),
           'performance-baseline',
           jsonb_build_object('sequence', i),
           'retrying', 1, 5, NOW(), NOW(), NOW()
    FROM generate_series(1, $RETRY_EVENTS) AS i;" \
    >"$RESULTS_DIR/retry-seed.log"

  local seeded
  seeded=$(psql_value "SELECT COUNT(*) FROM events WHERE id LIKE 'perf-retry-event-%' AND status='retrying';")
  [[ "$seeded" == "$RETRY_EVENTS" ]] || fail "seeded $seeded retry events, expected $RETRY_EVENTS"

  log "starting worker and measuring retry backlog drain"
  local start end seconds retry_rate
  start=$(date +%s.%N)
  compose start dispatch-worker >>"$RESULTS_DIR/compose-up-application.log" 2>&1
  wait_for_url worker http://localhost:8081/metrics
  wait_for_terminal_events 'perf-retry-event-' "$RETRY_EVENTS"
  end=$(date +%s.%N)
  seconds=$(elapsed_seconds "$start" "$end")
  retry_rate=$(rate_per_second "$RETRY_EVENTS" "$seconds")

  local delivered failed waiting processing leases
  delivered=$(psql_value "SELECT COUNT(*) FROM events WHERE id LIKE 'perf-retry-event-%' AND status='delivered';")
  failed=$(psql_value "SELECT COUNT(*) FROM events WHERE id LIKE 'perf-retry-event-%' AND status='failed';")
  waiting=$(psql_value "SELECT COUNT(*) FROM events WHERE id LIKE 'perf-retry-event-%' AND status IN ('retrying','throttled');")
  processing=$(psql_value "SELECT COUNT(*) FROM events WHERE id LIKE 'perf-retry-event-%' AND status='processing';")
  leases=$(psql_value "SELECT COUNT(*) FROM events WHERE id LIKE 'perf-retry-event-%' AND (processing_owner IS NOT NULL OR processing_deadline IS NOT NULL);")

  psql_report "
    SELECT status, COUNT(*) FROM events
    WHERE id LIKE 'perf-retry-event-%' GROUP BY status ORDER BY status;
    SELECT COUNT(*) AS leases_remaining FROM events
    WHERE id LIKE 'perf-retry-event-%'
      AND (processing_owner IS NOT NULL OR processing_deadline IS NOT NULL);" \
    | tee "$RESULTS_DIR/retry-database-report.txt"

  [[ "$delivered" == "$RETRY_EVENTS" ]] || fail "retry delivered $delivered events, expected $RETRY_EVENTS"
  [[ "$failed" == "0" && "$waiting" == "0" && "$processing" == "0" ]] || fail "retry backlog did not finish cleanly"
  [[ "$leases" == "0" ]] || fail "$leases retry leases remain"

  local failure_metrics
  failure_metrics=$(curl -fsS http://localhost:8081/metrics | awk '
    /^dispatch_worker_retry_(claim_failures|persistence_failures|stale_owner_rejections)_total / { total += $2 }
    END { print total + 0 }')
  [[ "$failure_metrics" == "0" ]] || fail "retry scheduler failure counters total $failure_metrics"

  log "Retry backlog drain: $retry_rate events/s over ${seconds}s"
  capture_evidence retry
}

for command in go docker curl awk grep git; do
  require_command "$command"
done

: >"$SUMMARY_FILE"
record_environment
log "results directory: $RESULTS_DIR"
run_acceptance_and_delivery
run_retry_backlog
log "PASS: correctness checks completed; throughput targets are reported above"
