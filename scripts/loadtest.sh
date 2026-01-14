#!/bin/bash
# Load test script for dispatch service
# Requires: hey (go install github.com/rakyll/hey@latest)

set -e

# Configuration
TARGET_URL="${TARGET_URL:-http://localhost:8080}"
REQUESTS="${REQUESTS:-10000}"
CONCURRENCY="${CONCURRENCY:-100}"

echo "=== Dispatch Load Test ==="
echo "Target: $TARGET_URL"
echo "Requests: $REQUESTS"
echo "Concurrency: $CONCURRENCY"
echo ""

# Check if hey is installed
if ! command -v hey &> /dev/null; then
    echo "Error: hey is not installed"
    echo "Install with: go install github.com/rakyll/hey@latest"
    exit 1
fi

# Check if service is running
if ! curl -s "$TARGET_URL/health" > /dev/null; then
    echo "Error: Service is not running at $TARGET_URL"
    echo "Start with: docker-compose up -d"
    exit 1
fi

TIMESTAMP=$(date +%s)

# Create a subscription for testing
echo "Setting up test subscription..."
curl -s -X POST "$TARGET_URL/subscriptions" \
    -H "Content-Type: application/json" \
    -d '{
        "id": "sub_loadtest",
        "url": "http://httpbin.org/post",
        "event_types": ["loadtest.*"],
        "rate_limit": 10000
    }' > /dev/null 2>&1 || true

echo ""
echo "=== Running Load Test ==="

# Run hey with POST request
hey -n "$REQUESTS" -c "$CONCURRENCY" \
    -m POST \
    -H "Content-Type: application/json" \
    -d "{\"id\":\"evt_load_$TIMESTAMP\",\"type\":\"loadtest.event\",\"source\":\"loadtest\",\"data\":{\"test\":true}}" \
    "$TARGET_URL/events"

echo ""
echo "=== Database Stats ==="
# If psql is available, show event counts
if command -v psql &> /dev/null; then
    PGPASSWORD=postgres psql -h localhost -U postgres -d dispatch -c \
        "SELECT status, COUNT(*) FROM events WHERE type = 'loadtest.event' GROUP BY status;" 2>/dev/null || true
fi

echo ""
echo "Load test completed at $(date)"
