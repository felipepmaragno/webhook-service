// Dispatch API service - HTTP API for event ingestion.
// Events are published directly to Kafka for delivery by workers.
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
	"github.com/felipemaragno/dispatch/internal/config"
	"github.com/felipemaragno/dispatch/internal/kafka"
	"github.com/felipemaragno/dispatch/internal/observability"
	"github.com/felipemaragno/dispatch/internal/repository/postgres"
)

func main() {
	cfg := config.ParseAPIConfig()
	if err := cfg.Validate(); err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if err := run(cfg, logger); err != nil {
		logger.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.APIConfig, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Database
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return err
	}
	logger.Info("connected to database")

	// Kafka producer
	producerConfig := kafka.DefaultProducerConfig()
	producerConfig.Brokers = cfg.KafkaBrokers
	producerConfig.Topic = cfg.KafkaTopic

	producer := kafka.NewProducer(producerConfig, logger)
	defer func() { _ = producer.Close() }()
	logger.Info("kafka producer initialized", "brokers", cfg.KafkaBrokers, "topic", cfg.KafkaTopic)

	// Repositories
	eventRepo := postgres.NewEventRepository(pool)
	subRepo := postgres.NewSubscriptionRepository(pool)

	// Observability
	metrics := observability.NewMetrics("dispatch")
	healthHandler := observability.NewHealthHandler(pool)

	// HTTP API
	handler := api.NewHandler(producer, eventRepo, subRepo, logger).WithMetrics(metrics)
	router := api.NewRouter(api.RouterConfig{
		Handler:       handler,
		HealthHandler: healthHandler,
		Metrics:       metrics,
		Logger:        logger,
	})

	healthHandler.SetReady(true)

	server := &http.Server{
		Addr:         cfg.Addr,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		logger.Info("starting HTTP server", "addr", cfg.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error", "error", err)
		}
	}()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("failed to shutdown HTTP server", "error", err)
	}

	logger.Info("shutdown complete")
	return nil
}
