package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felipemaragno/dispatch/internal/api"
	"github.com/felipemaragno/dispatch/internal/clock"
	"github.com/felipemaragno/dispatch/internal/observability"
	"github.com/felipemaragno/dispatch/internal/repository/postgres"
	"github.com/felipemaragno/dispatch/internal/resilience"
	"github.com/felipemaragno/dispatch/internal/retry"
	"github.com/felipemaragno/dispatch/internal/worker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/dispatch?sslmode=disable"
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		logger.Error("failed to ping database", "error", err)
		os.Exit(1)
	}
	logger.Info("connected to database")

	eventRepo := postgres.NewEventRepository(pool)
	subRepo := postgres.NewSubscriptionRepository(pool)

	metrics := observability.NewMetrics("dispatch")
	healthHandler := observability.NewHealthHandler(pool)

	handler := api.NewHandler(eventRepo, subRepo, logger).WithMetrics(metrics)
	router := api.NewRouter(api.RouterConfig{
		Handler:       handler,
		HealthHandler: healthHandler,
		Metrics:       metrics,
		Logger:        logger,
	})

	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	rateLimiter := resilience.NewRateLimiterManager(resilience.DefaultRateLimiterConfig())
	circuitBreaker := resilience.NewCircuitBreakerManager(resilience.DefaultCircuitBreakerConfig())

	workerPool := worker.NewPool(
		worker.DefaultConfig(),
		eventRepo,
		subRepo,
		httpClient,
		clock.RealClock{},
		retry.DefaultPolicy(),
		logger,
	).WithMetrics(metrics).WithResilience(rateLimiter, circuitBreaker)

	workerPool.Start(ctx)
	healthHandler.SetReady(true)

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	server := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		logger.Info("starting HTTP server", "addr", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	workerPool.Stop()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("failed to shutdown HTTP server", "error", err)
	}

	logger.Info("shutdown complete")
}
