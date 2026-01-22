#!/bin/bash
set -e

# End-to-end benchmark script
# Tests the full flow: API -> Kafka -> Worker -> Webhook Receiver

COMPOSE_FILE="docker-compose.benchmark.yaml"
NUM_SUBSCRIPTIONS=${1:-1000}
EVENTS_PER_SUB=${2:-1}
TOTAL_EVENTS=$((NUM_SUBSCRIPTIONS * EVENTS_PER_SUB))

echo "=============================================="
echo "  Dispatch E2E Benchmark"
echo "=============================================="
echo "  Subscriptions: $NUM_SUBSCRIPTIONS"
echo "  Events per subscription: $EVENTS_PER_SUB"
echo "  Total events: $TOTAL_EVENTS"
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
echo "[1/6] Starting services..."
docker compose -f $COMPOSE_FILE up -d --build 2>&1 | grep -E "(Created|Started|Building)" || true

echo "[2/6] Waiting for services to be healthy..."
sleep 45

# Check health
echo "[3/6] Checking service health..."
curl -sf http://localhost:8080/health > /dev/null || { echo "ERROR: dispatch not healthy"; exit 1; }
curl -sf http://localhost:9999/health > /dev/null || { echo "ERROR: receiver not healthy"; exit 1; }
echo "  All services healthy"

# Create subscriptions pointing to local receiver
echo "[4/6] Creating $NUM_SUBSCRIPTIONS subscriptions..."
for i in $(seq 1 $NUM_SUBSCRIPTIONS); do
    curl -sf -X POST http://localhost:8080/subscriptions \
        -H "Content-Type: application/json" \
        -d "{\"id\":\"bench-sub-$i\",\"url\":\"http://receiver:9999/webhook\",\"event_types\":[\"benchmark.event\"]}" \
        > /dev/null 2>&1 || true
done
echo "  Subscriptions created"

# Send events and measure time
echo "[5/6] Sending $TOTAL_EVENTS events..."
START_TIME=$(date +%s.%N)

for i in $(seq 1 $NUM_SUBSCRIPTIONS); do
    for j in $(seq 1 $EVENTS_PER_SUB); do
        curl -sf -X POST http://localhost:8080/events \
            -H "Content-Type: application/json" \
            -d "{\"id\":\"bench-evt-$i-$j\",\"type\":\"benchmark.event\",\"source\":\"benchmark\",\"data\":{\"sub\":$i,\"seq\":$j}}" \
            > /dev/null 2>&1 &
    done
done
wait

INGEST_TIME=$(date +%s.%N)
INGEST_DURATION=$(echo "$INGEST_TIME - $START_TIME" | bc)
INGEST_RATE=$(echo "scale=2; $TOTAL_EVENTS / $INGEST_DURATION" | bc)

echo "  Events sent in ${INGEST_DURATION}s (${INGEST_RATE} events/s)"

# Wait for delivery (estimate: 100ms latency * events / concurrency)
ESTIMATED_DELIVERY_TIME=$(echo "scale=0; ($TOTAL_EVENTS * 0.1) / 100 + 10" | bc)
echo "[6/6] Waiting for delivery (~${ESTIMATED_DELIVERY_TIME}s estimated)..."
sleep $ESTIMATED_DELIVERY_TIME

END_TIME=$(date +%s.%N)
TOTAL_DURATION=$(echo "$END_TIME - $START_TIME" | bc)

# Get stats from receiver
echo ""
echo "=============================================="
echo "  BENCHMARK RESULTS"
echo "=============================================="
echo ""
echo "  Configuration:"
echo "    Subscriptions: $NUM_SUBSCRIPTIONS"
echo "    Events per subscription: $EVENTS_PER_SUB"
echo "    Total events: $TOTAL_EVENTS"
echo "    Receiver latency: ~100ms"
echo "    Workers: 3 replicas"
echo ""
echo "  Ingestion (API -> Kafka):"
echo "    Duration: ${INGEST_DURATION}s"
echo "    Throughput: ${INGEST_RATE} events/s"
echo ""
echo "  End-to-end (API -> Delivery):"
echo "    Duration: ${TOTAL_DURATION}s"
echo "    Throughput: $(echo "scale=2; $TOTAL_EVENTS / $TOTAL_DURATION" | bc) events/s"
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
