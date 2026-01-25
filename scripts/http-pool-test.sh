#!/bin/bash
set -e
export LC_NUMERIC=C

# Test different HTTP connection pool sizes
COMPOSE_FILE="docker-compose.benchmark.yaml"
NUM_SUBS=5000
EVENTS_PER_SUB=10
TOTAL=$((NUM_SUBS * EVENTS_PER_SUB))

run_test() {
    local max_idle="$1"
    local max_per_host="$2"
    
    echo ""
    echo "=== MaxIdleConns=$max_idle, MaxIdleConnsPerHost=$max_per_host ==="
    
    docker compose -f $COMPOSE_FILE down -v 2>/dev/null || true
    
    export HTTP_MAX_IDLE_CONNS=$max_idle
    export HTTP_MAX_IDLE_CONNS_PER_HOST=$max_per_host
    
    docker compose -f $COMPOSE_FILE up -d --build 2>&1 | tail -3
    sleep 45
    
    curl -sf http://localhost:8080/health > /dev/null || { echo "ERROR: not healthy"; return 1; }
    
    go run scripts/benchmark/main.go \
        -subs $NUM_SUBS \
        -events $EVENTS_PER_SUB \
        -concurrency 500 \
        -wait 0 \
        -api http://localhost:8080 \
        -receiver http://receiver:9999/webhook 2>&1 | grep -E "done|events/s"
    
    local start=$(date +%s.%N)
    local max_wait=60
    
    while true; do
        local delivered=$(docker compose -f $COMPOSE_FILE exec -T postgres psql -U postgres -d dispatch -t -c \
            "SELECT COUNT(*) FROM events WHERE status = 'delivered' AND id LIKE 'bench-evt-%';" 2>/dev/null | tr -d ' ')
        
        local elapsed=$(echo "$(date +%s.%N) - $start" | bc)
        
        if [ "$delivered" -ge "$TOTAL" ] 2>/dev/null; then
            local rate=$(echo "scale=0; $delivered / $elapsed" | bc)
            echo "RESULT: MaxIdleConnsPerHost=$max_per_host → $rate events/s"
            break
        fi
        
        if [ $(echo "$elapsed > $max_wait" | bc) -eq 1 ]; then
            local rate=$(echo "scale=0; $delivered / $elapsed" | bc)
            echo "TIMEOUT: MaxIdleConnsPerHost=$max_per_host → $rate events/s"
            break
        fi
        
        sleep 2
    done
}

echo "=============================================="
echo "  HTTP CONNECTION POOL TEST"
echo "  $NUM_SUBS subs × $EVENTS_PER_SUB events = $TOTAL total"
echo "  All subscriptions point to same host (receiver)"
echo "=============================================="

# Test different MaxIdleConnsPerHost values
run_test 1000 10
run_test 1000 50
run_test 1000 100
run_test 1000 500

docker compose -f $COMPOSE_FILE down -v 2>/dev/null || true

echo ""
echo "=============================================="
echo "  TEST COMPLETE"
echo "=============================================="
