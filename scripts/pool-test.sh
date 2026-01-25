#!/bin/bash
set -e
export LC_NUMERIC=C

# Simple pool size test - 5k subs × 10 events = 50k total
COMPOSE_FILE="docker-compose.benchmark.yaml"
NUM_SUBS=5000
EVENTS_PER_SUB=10
TOTAL=$((NUM_SUBS * EVENTS_PER_SUB))

run_single_test() {
    local pool_size="$1"
    
    echo ""
    echo "=== Testing with DB pool = $pool_size connections ==="
    
    # Stop and clean
    docker compose -f $COMPOSE_FILE down -v 2>/dev/null || true
    
    # Start with specific pool size
    export DB_MAX_CONNS=$pool_size
    docker compose -f $COMPOSE_FILE up -d --build 2>&1 | grep -v "^$" | tail -5
    
    sleep 45
    
    # Check health
    curl -sf http://localhost:8080/health > /dev/null || { echo "ERROR: not healthy"; return 1; }
    
    # Create subscriptions and send events
    echo "Creating $NUM_SUBS subscriptions and sending $TOTAL events..."
    go run scripts/benchmark/main.go \
        -subs $NUM_SUBS \
        -events $EVENTS_PER_SUB \
        -concurrency 500 \
        -wait 0 \
        -api http://localhost:8080 \
        -receiver http://receiver:9999/webhook 2>&1 | grep -E "done|events/s"
    
    # Measure delivery
    echo "Measuring delivery..."
    local start=$(date +%s.%N)
    local max_wait=60
    
    while true; do
        local delivered=$(docker compose -f $COMPOSE_FILE exec -T postgres psql -U postgres -d dispatch -t -c \
            "SELECT COUNT(*) FROM events WHERE status = 'delivered' AND id LIKE 'bench-evt-%';" 2>/dev/null | tr -d ' ')
        
        local elapsed=$(echo "$(date +%s.%N) - $start" | bc)
        
        if [ "$delivered" -ge "$TOTAL" ] 2>/dev/null; then
            local rate=$(echo "scale=0; $delivered / $elapsed" | bc)
            echo "RESULT: pool=$pool_size → $delivered delivered in ${elapsed}s = $rate events/s"
            break
        fi
        
        if [ $(echo "$elapsed > $max_wait" | bc) -eq 1 ]; then
            local rate=$(echo "scale=0; $delivered / $elapsed" | bc)
            echo "TIMEOUT: pool=$pool_size → $delivered/$TOTAL in ${elapsed}s = $rate events/s"
            break
        fi
        
        sleep 2
    done
}

echo "=============================================="
echo "  DB POOL SIZE TEST"
echo "  $NUM_SUBS subs × $EVENTS_PER_SUB events = $TOTAL total"
echo "=============================================="

# Run tests with different pool sizes
run_single_test 30   # baseline
run_single_test 10   # reduced
run_single_test 5    # minimal

# Cleanup
docker compose -f $COMPOSE_FILE down -v 2>/dev/null || true

echo ""
echo "=============================================="
echo "  TEST COMPLETE"
echo "=============================================="
