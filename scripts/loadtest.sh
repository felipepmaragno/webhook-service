#!/bin/bash
# Load test script for dispatch service
# Requires: k6 (https://k6.io/docs/get-started/installation/)

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Configuration
TARGET_URL="${TARGET_URL:-http://localhost:8080}"
VUS="${VUS:-50}"
DURATION="${DURATION:-30s}"

echo "=== Dispatch Load Test (k6) ==="
echo "Target: $TARGET_URL"
echo "Virtual Users: $VUS"
echo "Duration: $DURATION"
echo ""

# Check if k6 is installed
if ! command -v k6 &> /dev/null; then
    echo "Error: k6 is not installed"
    echo ""
    echo "Install with:"
    echo "  macOS:   brew install k6"
    echo "  Ubuntu:  sudo apt install k6"
    echo "  Windows: choco install k6"
    echo "  Other:   https://k6.io/docs/get-started/installation/"
    exit 1
fi

# Check if service is running
if ! curl -s "$TARGET_URL/health" > /dev/null; then
    echo "Error: Service is not running at $TARGET_URL"
    echo "Start with: docker-compose up -d"
    exit 1
fi

echo ""
echo "=== Running k6 Load Test ==="
echo ""

# Run k6 with the JavaScript test script
k6 run \
    --vus "$VUS" \
    --duration "$DURATION" \
    -e TARGET_URL="$TARGET_URL" \
    "$SCRIPT_DIR/loadtest.js"

echo ""
echo "=== Database Stats ==="
# If psql is available, show event counts
if command -v psql &> /dev/null; then
    PGPASSWORD=postgres psql -h localhost -U postgres -d dispatch -c \
        "SELECT status, COUNT(*) FROM events WHERE type = 'loadtest.event' GROUP BY status ORDER BY count DESC;" 2>/dev/null || true
fi

echo ""
echo "Load test completed at $(date)"
