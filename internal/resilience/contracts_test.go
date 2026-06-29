package resilience

import (
	"context"
	"fmt"
	"testing"

	"github.com/felipemaragno/dispatch/internal/domain"
)

func TestRateLimiterContract_InMemory(t *testing.T) {
	limiter := NewInMemoryRateLimiterAdapter(DefaultRateLimiterConfig())
	runRateLimiterContract(t, limiter)
}

func runRateLimiterContract(t *testing.T, limiter RateLimiter) {
	t.Helper()
	ctx := context.Background()
	policy := domain.RatePolicy{RequestsPerSecond: 1}
	subID := uniqueSubscriptionID(t, "contract-rate")

	decision, err := limiter.Allow(ctx, subID, policy)
	if err != nil {
		t.Fatalf("allow first request: %v", err)
	}
	if !decision.Allowed {
		t.Fatal("first request should be allowed")
	}
	decision, err = limiter.Allow(ctx, subID, policy)
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

func uniqueSubscriptionID(t *testing.T, prefix string) string {
	t.Helper()
	return fmt.Sprintf("%s-%s", prefix, t.Name())
}
