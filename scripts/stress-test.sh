#!/bin/bash
set -e
export LC_NUMERIC=C

# Stress test - measures DELIVERY throughput (not ingestion)
# Pre-loads events into Kafka, then measures how fast they're delivered

COMPOSE_FILE="docker-compose.benchmark.yaml"
NUM_SUBSCRIPTIONS=${1:-1000}
EVENTS_PER_SUB=${2:-10}
TOTAL_EVENTS=$((NUM_SUBSCRIPTIONS * EVENTS_PER_SUB))

echo "=============================================="
echo "  Dispatch STRESS TEST"
echo "=============================================="
echo "  Subscriptions: $NUM_SUBSCRIPTIONS"
echo "  Events per subscription: $EVENTS_PER_SUB"
echo "  Total events: $TOTAL_EVENTS"
echo "  Receiver latency: ~100ms"
echo "  Workers: 3 replicas"
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
echo "[1/5] Starting services..."
docker compose -f $COMPOSE_FILE up -d --build 2>&1 | grep -E "(Created|Started|Building)" || true

echo "[2/5] Waiting for services to be healthy..."
sleep 45

# Check health
curl -sf http://localhost:8080/health > /dev/null || { echo "ERROR: dispatch not healthy"; exit 1; }
curl -sf http://localhost:9999/health > /dev/null || { echo "ERROR: receiver not healthy"; exit 1; }
echo "  All services healthy"

# Create subscriptions (fast, parallel)
echo "[3/5] Creating $NUM_SUBSCRIPTIONS subscriptions..."
go run scripts/benchmark/main.go \
    -subs $NUM_SUBSCRIPTIONS \
    -events 0 \
    -concurrency 500 \
    -wait 0 \
    -api http://localhost:8080 \
    -receiver http://receiver:9999/webhook 2>&1 | grep -E "Creating|done" || true

# Pre-load ALL events into Kafka (fast ingestion)
echo "[4/5] Pre-loading $TOTAL_EVENTS events into Kafka..."
INGEST_START=$(date +%s.%N)

go run scripts/benchmark/main.go \
    -subs $NUM_SUBSCRIPTIONS \
    -events $EVENTS_PER_SUB \
    -concurrency 1000 \
    -wait 0 \
    -api http://localhost:8080 \
    -receiver http://receiver:9999/webhook 2>&1 | grep -E "Sending|done" || true

INGEST_END=$(date +%s.%N)
INGEST_DURATION=$(echo "$INGEST_END - $INGEST_START" | bc)
echo "  Ingestion complete in ${INGEST_DURATION}s"

# Now measure delivery time
echo "[5/5] Measuring delivery throughput..."
echo "  Polling database for delivery status..."
echo ""

DELIVERY_START=$(date +%s.%N)
POLL_INTERVAL=2
MAX_WAIT=300  # 5 minutes max

while true; do
    # Get current delivery stats
    STATS=$(docker compose -f $COMPOSE_FILE exec -T postgres psql -U postgres -d dispatch -t -c "
        SELECT 
            COALESCE(SUM(CASE WHEN status = 'delivered' THEN 1 ELSE 0 END), 0) as delivered,
            COALESCE(SUM(CASE WHEN status = 'retrying' THEN 1 ELSE 0 END), 0) as retrying,
            COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) as failed,
            COUNT(*) as total
        FROM events 
        WHERE id LIKE 'bench-evt-%';
    " 2>/dev/null | tr -d ' ')
    
    DELIVERED=$(echo $STATS | cut -d'|' -f1)
    RETRYING=$(echo $STATS | cut -d'|' -f2)
    FAILED=$(echo $STATS | cut -d'|' -f3)
    TOTAL=$(echo $STATS | cut -d'|' -f4)
    
    CURRENT_TIME=$(date +%s.%N)
    ELAPSED=$(echo "$CURRENT_TIME - $DELIVERY_START" | bc)
    
    if [ "$DELIVERED" -gt 0 ]; then
        RATE=$(echo "scale=0; $DELIVERED / $ELAPSED" | bc)
    else
        RATE=0
    fi
    
    printf "\r  [%.0fs] Delivered: %d/%d (%.1f%%) | Retrying: %d | Failed: %d | Rate: %d/s    " \
        "$ELAPSED" "$DELIVERED" "$TOTAL_EVENTS" \
        $(echo "scale=1; $DELIVERED * 100 / $TOTAL_EVENTS" | bc) \
        "$RETRYING" "$FAILED" "$RATE"
    
    # Check if all delivered or failed
    COMPLETE=$((DELIVERED + FAILED))
    if [ "$COMPLETE" -ge "$TOTAL_EVENTS" ]; then
        break
    fi
    
    # Timeout check
    if [ $(echo "$ELAPSED > $MAX_WAIT" | bc) -eq 1 ]; then
        echo ""
        echo "  TIMEOUT: Not all events delivered within ${MAX_WAIT}s"
        break
    fi
    
    sleep $POLL_INTERVAL
done

DELIVERY_END=$(date +%s.%N)
DELIVERY_DURATION=$(echo "$DELIVERY_END - $DELIVERY_START" | bc)

echo ""
echo ""
echo "=============================================="
echo "  STRESS TEST RESULTS"
echo "=============================================="
echo ""
echo "  Configuration:"
echo "    Subscriptions: $NUM_SUBSCRIPTIONS"
echo "    Events per subscription: $EVENTS_PER_SUB"
echo "    Total events: $TOTAL_EVENTS"
echo "    Receiver latency: ~100ms"
echo "    Workers: 3 replicas"
echo ""
echo "  Delivery Performance:"
echo "    Events delivered: $DELIVERED"
echo "    Events failed: $FAILED"
echo "    Duration: ${DELIVERY_DURATION}s"
echo "    THROUGHPUT: $(echo "scale=0; $DELIVERED / $DELIVERY_DURATION" | bc) events/s"
echo ""
echo "  Success rate: $(echo "scale=2; $DELIVERED * 100 / $TOTAL_EVENTS" | bc)%"
echo ""
echo "=============================================="

# Get receiver stats
echo ""
echo "  Receiver stats (last 5 seconds):"
docker compose -f $COMPOSE_FILE logs receiver --tail 5 2>/dev/null | grep STATS || echo "    (no stats available)"
echo ""
