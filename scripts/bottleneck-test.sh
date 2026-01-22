#!/bin/bash
set -e
export LC_NUMERIC=C

# Bottleneck identification test
# Tests different configurations to find the real bottleneck
# Keeps <100 events per subscription to avoid rate limiting

COMPOSE_FILE="docker-compose.benchmark.yaml"
TOTAL_EVENTS=100000

echo "=============================================="
echo "  BOTTLENECK IDENTIFICATION TEST"
echo "=============================================="
echo "  Total events: $TOTAL_EVENTS"
echo "  Events per subscription: <100 (no rate limiting)"
echo "=============================================="
echo ""

# Cleanup
cleanup() {
    docker compose -f $COMPOSE_FILE down -v 2>/dev/null || true
}
trap cleanup EXIT

run_test() {
    local test_name="$1"
    local num_subs="$2"
    local events_per_sub="$3"
    local db_max_conns="$4"
    local extra_env="$5"
    
    echo ""
    echo "----------------------------------------------"
    echo "TEST: $test_name"
    echo "  Subscriptions: $num_subs"
    echo "  Events/sub: $events_per_sub"
    echo "  DB pool: $db_max_conns connections"
    echo "  Extra: $extra_env"
    echo "----------------------------------------------"
    
    # Stop previous
    docker compose -f $COMPOSE_FILE down -v 2>/dev/null || true
    
    # Modify docker-compose with env vars
    export DB_MAX_CONNS=$db_max_conns
    
    # Start services
    docker compose -f $COMPOSE_FILE up -d --build 2>&1 | grep -E "(Building|Created|Started)" | head -5 || true
    
    # Wait for services
    sleep 45
    
    # Check health
    curl -sf http://localhost:8080/health > /dev/null || { echo "ERROR: dispatch not healthy"; return 1; }
    curl -sf http://localhost:9999/health > /dev/null || { echo "ERROR: receiver not healthy"; return 1; }
    
    # Create subscriptions
    go run scripts/benchmark.go \
        -subs $num_subs \
        -events 0 \
        -concurrency 500 \
        -wait 0 \
        -api http://localhost:8080 \
        -receiver http://receiver:9999/webhook 2>&1 | grep -E "Creating|done" || true
    
    # Send events
    local total=$((num_subs * events_per_sub))
    go run scripts/benchmark.go \
        -subs $num_subs \
        -events $events_per_sub \
        -concurrency 1000 \
        -wait 0 \
        -api http://localhost:8080 \
        -receiver http://receiver:9999/webhook 2>&1 | grep -E "Sending|done" || true
    
    # Measure delivery
    local start_time=$(date +%s.%N)
    local max_wait=120
    
    while true; do
        local stats=$(docker compose -f $COMPOSE_FILE exec -T postgres psql -U postgres -d dispatch -t -c "
            SELECT COALESCE(SUM(CASE WHEN status = 'delivered' THEN 1 ELSE 0 END), 0) as delivered,
                   COUNT(*) as total
            FROM events WHERE id LIKE 'bench-evt-%';
        " 2>/dev/null | tr -d ' ')
        
        local delivered=$(echo $stats | cut -d'|' -f1)
        local db_total=$(echo $stats | cut -d'|' -f2)
        
        local current_time=$(date +%s.%N)
        local elapsed=$(echo "$current_time - $start_time" | bc)
        
        if [ "$delivered" -ge "$total" ] 2>/dev/null; then
            local throughput=$(echo "scale=0; $delivered / $elapsed" | bc)
            echo "  RESULT: $delivered delivered in ${elapsed}s = $throughput events/s"
            echo "$test_name,$num_subs,$events_per_sub,$db_max_conns,$delivered,$elapsed,$throughput" >> /tmp/bottleneck_results.csv
            break
        fi
        
        if [ $(echo "$elapsed > $max_wait" | bc) -eq 1 ]; then
            local throughput=$(echo "scale=0; $delivered / $elapsed" | bc)
            echo "  TIMEOUT: $delivered/$total delivered in ${elapsed}s = $throughput events/s"
            echo "$test_name,$num_subs,$events_per_sub,$db_max_conns,$delivered,$elapsed,$throughput" >> /tmp/bottleneck_results.csv
            break
        fi
        
        sleep 2
    done
}

# Initialize results file
echo "test_name,subscriptions,events_per_sub,db_pool,delivered,duration,throughput" > /tmp/bottleneck_results.csv

# Test 1: Baseline (current config)
run_test "baseline_30conn" 20000 5 30 ""

# Test 2: Reduce DB pool to 10
run_test "pool_10conn" 20000 5 10 ""

# Test 3: Reduce DB pool to 5
run_test "pool_5conn" 20000 5 5 ""

# Test 4: Increase DB pool to 50
run_test "pool_50conn" 20000 5 50 ""

# Test 5: More subscriptions, fewer events each (50k subs × 2 events)
run_test "more_subs_50k" 50000 2 30 ""

echo ""
echo "=============================================="
echo "  RESULTS SUMMARY"
echo "=============================================="
cat /tmp/bottleneck_results.csv | column -t -s','
echo "=============================================="
