#!/bin/bash
set -e

# End-to-end benchmark script
# Tests the full flow: API -> Kafka -> Worker -> Webhook Receiver
# Uses Go benchmark tool for parallel event sending

COMPOSE_FILE="docker-compose.benchmark.yaml"
NUM_SUBSCRIPTIONS=${1:-1000}
EVENTS_PER_SUB=${2:-1}
CONCURRENCY=${3:-200}
WAIT_TIME=${4:-30}
TOTAL_EVENTS=$((NUM_SUBSCRIPTIONS * EVENTS_PER_SUB))

echo "=============================================="
echo "  Dispatch E2E Benchmark (Parallel)"
echo "=============================================="
echo "  Subscriptions: $NUM_SUBSCRIPTIONS"
echo "  Events per subscription: $EVENTS_PER_SUB"
echo "  Total events: $TOTAL_EVENTS"
echo "  Concurrency: $CONCURRENCY"
echo "  Receiver latency: ~100ms"
echo "=============================================="
echo ""

# Cleanup
cleanup() {
    echo ""
    echo "Cleaning up..."
    docker compose -f $COMPOSE_FILE down -v 2>/dev/null || true
}
trap cleanup EXIT

# Start services
echo "[1/4] Starting services..."
docker compose -f $COMPOSE_FILE up -d --build 2>&1 | grep -E "(Created|Started|Building)" || true

echo "[2/4] Waiting for services to be healthy..."
sleep 45

# Check health
echo "[3/4] Checking service health..."
curl -sf http://localhost:8080/health > /dev/null || { echo "ERROR: dispatch not healthy"; exit 1; }
curl -sf http://localhost:9999/health > /dev/null || { echo "ERROR: receiver not healthy"; exit 1; }
echo "  All services healthy"

# Run Go benchmark
echo "[4/4] Running benchmark..."
echo ""
go run scripts/benchmark.go \
    -subs $NUM_SUBSCRIPTIONS \
    -events $EVENTS_PER_SUB \
    -concurrency $CONCURRENCY \
    -wait $WAIT_TIME \
    -api http://localhost:8080 \
    -receiver http://receiver:9999/webhook

echo ""

# Query database for actual delivery stats
echo "  Delivery stats (from PostgreSQL):"
docker compose -f $COMPOSE_FILE exec -T postgres psql -U postgres -d dispatch -c "
SELECT 
    status,
    COUNT(*) as count,
    ROUND(COUNT(*) * 100.0 / SUM(COUNT(*)) OVER(), 2) as percentage
FROM events 
WHERE id LIKE 'bench-evt-%'
GROUP BY status
ORDER BY count DESC;
" 2>/dev/null || echo "    (Could not query database)"

echo ""
echo "=============================================="
