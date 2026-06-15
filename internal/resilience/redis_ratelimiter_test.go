package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/redis/go-redis/v9"
)

func TestRedisRateLimiter_Allow(t *testing.T) {
	client, cleanup := setupRedisClient(t)
	defer cleanup()

	ctx := context.Background()

	// Clean up test keys
	client.Del(ctx, "ratelimit:test_sub")

	config := RedisRateLimiterConfig{
		Window: time.Second,
	}
	limiter := NewRedisRateLimiter(client, config, nil)

	subID := "test_sub"
	policy := domain.RatePolicy{RequestsPerSecond: 5, BurstSize: 5}

	// Should allow requests up to the subscription policy.
	for i := 0; i < policy.RequestsPerSecond; i++ {
		decision, err := limiter.Allow(ctx, subID, policy)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !decision.Allowed {
			t.Errorf("request %d should be allowed", i+1)
		}
	}

	// Next request should be rate limited
	decision, err := limiter.Allow(ctx, subID, policy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Allowed {
		t.Error("request after configured policy should be rate limited")
	}

	// Clean up
	client.Del(ctx, "ratelimit:test_sub")
}

func TestRedisRateLimiter_WindowExpiry(t *testing.T) {
	client, cleanup := setupRedisClient(t)
	defer cleanup()

	ctx := context.Background()

	// Clean up test keys
	client.Del(ctx, "ratelimit:test_window")

	config := RedisRateLimiterConfig{
		Window: time.Second,
	}
	limiter := NewRedisRateLimiter(client, config, nil)

	subID := "test_window"
	policy := domain.RatePolicy{RequestsPerSecond: 5, BurstSize: 5}

	// Use up all requests in the configured policy.
	for i := 0; i < policy.RequestsPerSecond; i++ {
		decision, _ := limiter.Allow(ctx, subID, policy)
		if !decision.Allowed {
			t.Errorf("request %d should be allowed", i+1)
		}
	}

	// Should be rate limited now
	decision, _ := limiter.Allow(ctx, subID, policy)
	if decision.Allowed {
		t.Error("should be rate limited after configured requests")
	}

	// Wait for window to expire
	time.Sleep(1100 * time.Millisecond)

	// Should be allowed again after window
	decision, _ = limiter.Allow(ctx, subID, policy)
	if !decision.Allowed {
		t.Error("should be allowed after window expiry")
	}

	// Clean up
	client.Del(ctx, "ratelimit:test_window")
}

func TestRedisRateLimiter_Fallback(t *testing.T) {
	// Use invalid Redis address to trigger fallback
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:9999", // Invalid port
	})
	defer func() { _ = client.Close() }()

	config := DefaultRedisRateLimiterConfig()
	limiter := NewRedisRateLimiter(client, config, nil)

	ctx := context.Background()
	subID := "test_fallback"
	policy := domain.RatePolicy{RequestsPerSecond: 5, BurstSize: 5}

	// Should fall back to in-memory and still work
	decision, err := limiter.Allow(ctx, subID, policy)
	if err != nil {
		t.Fatalf("should not return error on fallback: %v", err)
	}
	if !decision.Allowed {
		t.Error("should be allowed via fallback")
	}
	if !decision.Degraded {
		t.Error("expected degraded fallback decision")
	}
}
