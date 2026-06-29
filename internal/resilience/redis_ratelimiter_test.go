package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/felipemaragno/dispatch/internal/domain"
)

func TestRateLimiterContract_Redis(t *testing.T) {
	client, cleanup := setupRedisClient(t)
	defer cleanup()

	limiter := NewRedisRateLimiter(client, RedisRateLimiterConfig{Window: time.Second}, nil)
	runRateLimiterContract(t, limiter)
}

func TestRedisRateLimiter_WindowExpiry(t *testing.T) {
	client, cleanup := setupRedisClient(t)
	defer cleanup()

	limiter := NewRedisRateLimiter(client, RedisRateLimiterConfig{Window: 50 * time.Millisecond}, nil)
	ctx := context.Background()
	subID := uniqueSubscriptionID(t, "redis-window")
	policy := domain.RatePolicy{RequestsPerSecond: 1}

	decision, err := limiter.Allow(ctx, subID, policy)
	if err != nil {
		t.Fatalf("allow first request: %v", err)
	}
	if !decision.Allowed {
		t.Fatal("first request should be allowed")
	}

	decision, err = limiter.Allow(ctx, subID, policy)
	if err != nil {
		t.Fatalf("deny second request: %v", err)
	}
	if decision.Allowed {
		t.Fatal("second request should be denied in the same window")
	}

	time.Sleep(75 * time.Millisecond)

	decision, err = limiter.Allow(ctx, subID, policy)
	if err != nil {
		t.Fatalf("allow after window expiry: %v", err)
	}
	if !decision.Allowed {
		t.Fatal("request should be allowed after window expiry")
	}
}

func TestRedisRateLimiter_FailsClosedOnRedisError(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  10 * time.Millisecond,
		ReadTimeout:  10 * time.Millisecond,
		WriteTimeout: 10 * time.Millisecond,
	})
	defer func() { _ = client.Close() }()

	limiter := NewRedisRateLimiter(client, RedisRateLimiterConfig{Window: 50 * time.Millisecond}, nil)

	decision, err := limiter.Allow(context.Background(), "sub-redis-down", domain.RatePolicy{RequestsPerSecond: 100})
	if err == nil {
		t.Fatal("expected redis error")
	}
	if decision.Allowed {
		t.Fatal("redis errors should fail closed")
	}
	if decision.RetryAfter <= 0 {
		t.Fatalf("fail-closed decision should include retry delay, got %s", decision.RetryAfter)
	}
}
