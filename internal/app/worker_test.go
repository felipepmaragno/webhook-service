package app

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felipemaragno/dispatch/internal/config"
	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/observability"
	"github.com/felipemaragno/dispatch/internal/resilience"
)

func TestInitRateLimiter_UsesLocalWhenRedisURLAbsent(t *testing.T) {
	limiter, redisClient := initRateLimiter(context.Background(), config.WorkerConfig{}, slog.Default())
	if redisClient != nil {
		t.Fatal("expected no redis client in local mode")
	}
	if _, ok := limiter.(*resilience.InMemoryRateLimiterAdapter); !ok {
		t.Fatalf("expected local limiter, got %T", limiter)
	}
}

func TestInitRateLimiter_FailsClosedForMalformedRedisURL(t *testing.T) {
	limiter, redisClient := initRateLimiter(context.Background(), config.WorkerConfig{
		RedisURL: "://bad-url",
	}, slog.Default())
	if redisClient != nil {
		t.Fatal("malformed Redis URL should not create a client")
	}

	decision, err := limiter.Allow(context.Background(), "sub-bad-redis-url", domain.RatePolicy{RequestsPerSecond: 100})
	if err != nil {
		t.Fatalf("fail-closed limiter should not return setup error at decision time: %v", err)
	}
	if decision.Allowed {
		t.Fatal("malformed Redis URL should fail closed")
	}
	if decision.RetryAfter <= 0 {
		t.Fatalf("fail-closed decision should include retry delay, got %s", decision.RetryAfter)
	}
}

func TestWorkerObservabilityHandler_HealthAndReady(t *testing.T) {
	healthHandler := observability.NewHealthHandler()
	healthHandler.SetReady(true)
	handler := workerObservabilityHandler(healthHandler)

	for _, path := range []string{"/health", "/ready", "/metrics"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected %s to return %d, got %d", path, http.StatusOK, rec.Code)
			}
		})
	}
}
