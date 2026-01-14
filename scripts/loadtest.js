/**
 * k6 Load Test Script for Dispatch Service
 * 
 * Installation:
 *   brew install k6          # macOS
 *   sudo apt install k6      # Ubuntu/Debian
 *   choco install k6         # Windows
 *   # Or: https://k6.io/docs/get-started/installation/
 * 
 * Usage:
 *   # Quick test (10 VUs, 30s)
 *   k6 run scripts/loadtest.js
 * 
 *   # Custom load
 *   k6 run --vus 50 --duration 60s scripts/loadtest.js
 * 
 *   # Ramp up test
 *   k6 run --stage 10s:10,30s:50,10s:0 scripts/loadtest.js
 * 
 *   # With custom target
 *   k6 run -e TARGET_URL=http://localhost:8080 scripts/loadtest.js
 */

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

// Custom metrics
const eventsCreated = new Counter('events_created');
const eventsFailed = new Counter('events_failed');
const successRate = new Rate('success_rate');
const eventLatency = new Trend('event_latency', true);

// Configuration
const TARGET_URL = __ENV.TARGET_URL || 'http://localhost:8080';

// Test options - can be overridden via CLI
export const options = {
  // Default: ramp up to 50 VUs over 10s, hold for 30s, ramp down
  stages: [
    { duration: '10s', target: 10 },   // Ramp up
    { duration: '30s', target: 50 },   // Hold at 50 VUs
    { duration: '10s', target: 0 },    // Ramp down
  ],
  
  // Thresholds - test fails if these aren't met
  thresholds: {
    http_req_duration: ['p(95)<500'],  // 95% of requests under 500ms
    success_rate: ['rate>0.99'],        // 99% success rate
    http_req_failed: ['rate<0.01'],     // Less than 1% failures
  },
};

// Setup: create test subscription (runs once before test)
export function setup() {
  const subscriptionPayload = JSON.stringify({
    id: 'sub_k6_loadtest',
    url: 'https://httpbin.org/post',  // Echo service for testing
    event_types: ['loadtest.*'],
    rate_limit: 10000,
  });

  const res = http.post(`${TARGET_URL}/subscriptions`, subscriptionPayload, {
    headers: { 'Content-Type': 'application/json' },
  });

  // Subscription might already exist (409), that's OK
  if (res.status !== 201 && res.status !== 409) {
    console.warn(`Setup warning: subscription creation returned ${res.status}`);
  }

  return { startTime: Date.now() };
}

// Main test function - runs for each VU iteration
export default function(data) {
  // Generate unique event ID using VU id and iteration
  const eventId = `evt_k6_${__VU}_${__ITER}_${Date.now()}`;
  
  const payload = JSON.stringify({
    id: eventId,
    type: 'loadtest.event',
    source: 'k6-loadtest',
    data: {
      vu: __VU,
      iter: __ITER,
      timestamp: new Date().toISOString(),
    },
  });

  const params = {
    headers: {
      'Content-Type': 'application/json',
    },
    tags: { name: 'CreateEvent' },
  };

  const start = Date.now();
  const res = http.post(`${TARGET_URL}/events`, payload, params);
  const duration = Date.now() - start;

  // Record custom metrics
  eventLatency.add(duration);

  const success = check(res, {
    'status is 202': (r) => r.status === 202,
    'response has id': (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.id === eventId;
      } catch {
        return false;
      }
    },
  });

  if (success) {
    eventsCreated.add(1);
    successRate.add(1);
  } else {
    eventsFailed.add(1);
    successRate.add(0);
    console.error(`Failed: ${res.status} - ${res.body}`);
  }

  // Small sleep to avoid hammering (remove for max throughput)
  // sleep(0.01);
}

// Teardown: print summary (runs once after test)
export function teardown(data) {
  const duration = (Date.now() - data.startTime) / 1000;
  console.log(`\n=== Load Test Complete ===`);
  console.log(`Duration: ${duration.toFixed(1)}s`);
  console.log(`Target: ${TARGET_URL}`);
}
