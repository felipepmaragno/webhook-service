#!/bin/bash
# Benchmark different worker counts to find optimal configuration

set -e

WORKERS_TO_TEST="5 10 20 50"
DURATION="30s"
VUS=100

echo "=== Worker Count Benchmark ==="
echo "Testing: $WORKERS_TO_TEST workers"
echo "Duration: $DURATION, VUs: $VUS"
echo ""

# Stop any running containers
docker compose -f docker-compose.yaml down 2>/dev/null || true

for WORKERS in $WORKERS_TO_TEST; do
    echo "========================================"
    echo "Testing with $WORKERS workers..."
    echo "========================================"
    
    # Start services with specific worker count
    WORKER_COUNT=$WORKERS docker compose up -d --build
    
    # Wait for healthy
    echo "Waiting for services to be healthy..."
    sleep 15
    
    # Check health
    if ! curl -sf http://localhost:8080/health > /dev/null; then
        echo "ERROR: Service not healthy"
        docker compose logs dispatch
        docker compose down
        exit 1
    fi
    
    # Run load test
    echo "Running k6 load test..."
    k6 run --vus $VUS --duration $DURATION -e TARGET_URL=http://localhost:8080 scripts/loadtest.js 2>&1 | grep -E "(events_created|http_req_duration|success_rate)" | head -5
    
    # Stop services
    docker compose down
    
    echo ""
done

echo "=== Benchmark Complete ==="
