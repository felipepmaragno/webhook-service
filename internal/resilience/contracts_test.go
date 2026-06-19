package resilience

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
)

func TestRateLimiterContract_InMemory(t *testing.T) {
	limiter := NewInMemoryRateLimiterAdapter(DefaultRateLimiterConfig())
	runRateLimiterContract(t, limiter)
}

func TestRateLimiterContract_Redis(t *testing.T) {
	client, cleanup := setupRedisClient(t)
	defer cleanup()

	limiter := NewRedisRateLimiter(client, RedisRateLimiterConfig{Window: time.Second}, nil)
	runRateLimiterContract(t, limiter)
}

func runRateLimiterContract(t *testing.T, limiter RateLimiter) {
	t.Helper()
	ctx := context.Background()
	policy := domain.RatePolicy{RequestsPerSecond: 2, BurstSize: 2}
	subID := uniqueSubscriptionID(t, "contract-rate")

	for i := 0; i < policy.RequestsPerSecond; i++ {
		decision, err := limiter.Allow(ctx, subID, policy)
		if err != nil {
			t.Fatalf("allow request %d: %v", i+1, err)
		}
		if !decision.Allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
		if decision.Degraded {
			t.Fatalf("request %d should not be degraded", i+1)
		}
	}

	decision, err := limiter.Allow(ctx, subID, policy)
	if err != nil {
		t.Fatalf("deny after policy limit: %v", err)
	}
	if decision.Allowed {
		t.Fatal("request after policy limit should be denied")
	}
	if decision.RetryAfter <= 0 {
		t.Fatalf("denied request should include retry delay, got %s", decision.RetryAfter)
	}

	otherSubID := uniqueSubscriptionID(t, "contract-rate-other")
	decision, err = limiter.Allow(ctx, otherSubID, policy)
	if err != nil {
		t.Fatalf("allow isolated subscription: %v", err)
	}
	if !decision.Allowed {
		t.Fatal("rate limit should be isolated per subscription")
	}

	defaultSubID := uniqueSubscriptionID(t, "contract-rate-default")
	decision, err = limiter.Allow(ctx, defaultSubID, domain.RatePolicy{})
	if err != nil {
		t.Fatalf("allow default policy: %v", err)
	}
	if !decision.Allowed {
		t.Fatal("zero-valued policy should use defaults and allow the first request")
	}
}

func TestCircuitBreakerContract_InMemory(t *testing.T) {
	cb := NewInMemoryCircuitBreakerAdapter(CircuitBreakerConfig{
		MaxRequests:  1,
		Interval:     time.Second,
		Timeout:      20 * time.Millisecond,
		FailureRatio: 1,
		MinRequests:  2,
	})
	runCircuitBreakerContract(t, cb)
}

func TestCircuitBreakerContract_Redis(t *testing.T) {
	client, cleanup := setupRedisClient(t)
	defer cleanup()

	cb := NewRedisCircuitBreaker(client, RedisCircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 1,
		Timeout:          20 * time.Millisecond,
		Window:           time.Second,
	}, nil)
	runCircuitBreakerContract(t, cb)
}

func runCircuitBreakerContract(t *testing.T, cb CircuitBreaker) {
	t.Helper()
	ctx := context.Background()
	subID := uniqueSubscriptionID(t, "contract-circuit")

	allowed, err := cb.Allow(ctx, subID)
	if err != nil {
		t.Fatalf("initial allow: %v", err)
	}
	if !allowed {
		t.Fatal("closed circuit should allow the first request")
	}

	for i := 0; i < 2; i++ {
		if err := cb.RecordFailure(ctx, subID); err != nil {
			t.Fatalf("record failure %d: %v", i+1, err)
		}
	}

	state, err := cb.State(ctx, subID)
	if err != nil {
		t.Fatalf("state after failures: %v", err)
	}
	if state != CircuitStateOpen {
		t.Fatalf("expected open circuit after threshold failures, got %s", state)
	}

	allowed, err = cb.Allow(ctx, subID)
	if err != nil {
		t.Fatalf("allow while open: %v", err)
	}
	if allowed {
		t.Fatal("open circuit should deny before timeout")
	}

	time.Sleep(30 * time.Millisecond)

	allowed, err = cb.Allow(ctx, subID)
	if err != nil {
		t.Fatalf("allow after timeout: %v", err)
	}
	if !allowed {
		t.Fatal("open circuit should allow a half-open probe after timeout")
	}

	state, err = cb.State(ctx, subID)
	if err != nil {
		t.Fatalf("state after timeout probe: %v", err)
	}
	if state != CircuitStateHalfOpen {
		t.Fatalf("expected half-open circuit after timeout probe, got %s", state)
	}

	if err := cb.RecordSuccess(ctx, subID); err != nil {
		t.Fatalf("record half-open success: %v", err)
	}

	state, err = cb.State(ctx, subID)
	if err != nil {
		t.Fatalf("state after half-open success: %v", err)
	}
	if state != CircuitStateClosed {
		t.Fatalf("expected closed circuit after half-open success, got %s", state)
	}

	otherSubID := uniqueSubscriptionID(t, "contract-circuit-other")
	allowed, err = cb.Allow(ctx, otherSubID)
	if err != nil {
		t.Fatalf("allow isolated circuit: %v", err)
	}
	if !allowed {
		t.Fatal("circuit state should be isolated per subscription")
	}
}

func uniqueSubscriptionID(t *testing.T, prefix string) string {
	t.Helper()
	return fmt.Sprintf("%s:%s:%d", prefix, t.Name(), time.Now().UnixNano())
}
