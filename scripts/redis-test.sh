#!/bin/bash
set -e
export LC_NUMERIC=C

# Test Redis impact - compare with and without Redis
COMPOSE_FILE="docker-compose.benchmark.yaml"
NUM_SUBS=5000
EVENTS_PER_SUB=10
TOTAL=$((NUM_SUBS * EVENTS_PER_SUB))

run_test() {
    local test_name="$1"
    local redis_url="$2"
    
    echo ""
    echo "=== $test_name ==="
    
    docker compose -f $COMPOSE_FILE down -v 2>/dev/null || true
    
    # Temporarily modify REDIS_URL if needed
    if [ "$redis_url" = "disabled" ]; then
        # Create a modified compose that disables Redis
        sed 's/REDIS_URL: redis:\/\/redis:6379\/0/REDIS_URL: ""/' $COMPOSE_FILE > /tmp/compose-no-redis.yaml
        docker compose -f /tmp/compose-no-redis.yaml up -d --build 2>&1 | tail -3
    else
        docker compose -f $COMPOSE_FILE up -d --build 2>&1 | tail -3
    fi
    
    sleep 45
    
    curl -sf http://localhost:8080/health > /dev/null || { echo "ERROR: not healthy"; return 1; }
    
    echo "Creating $NUM_SUBS subscriptions and sending $TOTAL events..."
    go run scripts/benchmark.go \
        -subs $NUM_SUBS \
        -events $EVENTS_PER_SUB \
        -concurrency 500 \
        -wait 0 \
        -api http://localhost:8080 \
        -receiver http://receiver:9999/webhook 2>&1 | grep -E "done|events/s"
    
    echo "Measuring delivery..."
    local start=$(date +%s.%N)
    local max_wait=60
    
    while true; do
        local delivered=$(docker compose -f $COMPOSE_FILE exec -T postgres psql -U postgres -d dispatch -t -c \
            "SELECT COUNT(*) FROM events WHERE status = 'delivered' AND id LIKE 'bench-evt-%';" 2>/dev/null | tr -d ' ')
        
        local elapsed=$(echo "$(date +%s.%N) - $start" | bc)
        
        if [ "$delivered" -ge "$TOTAL" ] 2>/dev/null; then
            local rate=$(echo "scale=0; $delivered / $elapsed" | bc)
            echo "RESULT: $test_name → $delivered in ${elapsed}s = $rate events/s"
            break
        fi
        
        if [ $(echo "$elapsed > $max_wait" | bc) -eq 1 ]; then
            local rate=$(echo "scale=0; $delivered / $elapsed" | bc)
            echo "TIMEOUT: $test_name → $delivered/$TOTAL in ${elapsed}s = $rate events/s"
            break
        fi
        
        sleep 2
    done
}

echo "=============================================="
echo "  REDIS IMPACT TEST"
echo "  $NUM_SUBS subs × $EVENTS_PER_SUB events = $TOTAL total"
echo "=============================================="

run_test "With Redis (rate limiter + circuit breaker)" "enabled"
run_test "Without Redis (in-memory fallback)" "disabled"

docker compose -f $COMPOSE_FILE down -v 2>/dev/null || true

echo ""
echo "=============================================="
