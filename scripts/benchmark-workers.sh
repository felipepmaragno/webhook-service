#!/bin/bash
# Benchmark Kafka-based workers with load test producer

set -e

EVENTS=10000
SUBSCRIPTIONS=1000

echo "=== Kafka Worker Benchmark ==="
echo "Events: $EVENTS, Subscriptions: $SUBSCRIPTIONS"
echo ""

# Ensure infrastructure is running
if ! docker compose -f docker-compose.kafka.yaml ps | grep -q "kafka-1"; then
    echo "Starting Kafka infrastructure..."
    docker compose -f docker-compose.kafka.yaml up -d postgres redis zookeeper kafka-1 kafka-2 kafka-3 kafka-init
    sleep 30
fi

# Rebuild and restart workers
echo "Rebuilding workers..."
docker compose -f docker-compose.kafka.yaml build dispatch-worker-1 dispatch-worker-2 dispatch-worker-3
docker compose -f docker-compose.kafka.yaml up -d dispatch-worker-1 dispatch-worker-2 dispatch-worker-3
sleep 5

# Check workers are healthy
echo "Checking workers..."
docker compose -f docker-compose.kafka.yaml logs --tail 5 dispatch-worker-1 | grep -q "worker started" && echo "Worker 1: OK"
docker compose -f docker-compose.kafka.yaml logs --tail 5 dispatch-worker-2 | grep -q "worker started" && echo "Worker 2: OK"
docker compose -f docker-compose.kafka.yaml logs --tail 5 dispatch-worker-3 | grep -q "worker started" && echo "Worker 3: OK"

# Create test subscriptions
echo ""
echo "Creating $SUBSCRIPTIONS test subscriptions..."
for i in $(seq 1 $SUBSCRIPTIONS); do
    curl -sf -X POST http://localhost:8080/subscriptions \
        -H "Content-Type: application/json" \
        -d "{\"id\":\"sub_bench_$i\",\"url\":\"http://172.17.0.1:9999/webhook\",\"event_types\":[\"bench.event.$i\"]}" > /dev/null 2>&1 || true
done
echo "Subscriptions created."

# Run load test with producer
echo ""
echo "Running load test producer..."
START=$(date +%s)
go run ./cmd/producer -count $EVENTS -type onesub -prefix bench.event
END=$(date +%s)

DURATION=$((END - START))
RATE=$((EVENTS / DURATION))
echo ""
echo "=== Results ==="
echo "Events produced: $EVENTS"
echo "Duration: ${DURATION}s"
echo "Production rate: ${RATE} events/s"

# Wait for delivery and check results
echo ""
echo "Waiting 10s for delivery..."
sleep 10

# Check delivery stats
echo ""
echo "=== Delivery Stats ==="
docker exec dispatch-postgres-1 psql -U postgres -d dispatch -c "
SELECT status, COUNT(*) as count 
FROM events 
WHERE id LIKE 'evt_onesub_%' 
GROUP BY status 
ORDER BY count DESC;"

echo ""
echo "=== Benchmark Complete ==="
