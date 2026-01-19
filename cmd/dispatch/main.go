// Dispatch API service - HTTP API for event ingestion.
// Events are published directly to Kafka for delivery by workers.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felipemaragno/dispatch/internal/api"
	"github.com/felipemaragno/dispatch/internal/kafka"
	"github.com/felipemaragno/dispatch/internal/observability"
	"github.com/felipemaragno/dispatch/internal/repository/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Database (for subscriptions and event status queries)
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

	// Kafka producer (for publishing events)
	kafkaBrokers := os.Getenv("KAFKA_BROKERS")
	if kafkaBrokers == "" {
		kafkaBrokers = "localhost:9092"
	}
	kafkaTopic := os.Getenv("KAFKA_TOPIC")
	if kafkaTopic == "" {
		kafkaTopic = "events.pending"
	}

	producerConfig := kafka.DefaultProducerConfig()
	producerConfig.Brokers = strings.Split(kafkaBrokers, ",")
	producerConfig.Topic = kafkaTopic

	producer := kafka.NewProducer(producerConfig, logger)
	defer producer.Close()
	logger.Info("kafka producer initialized", "brokers", kafkaBrokers, "topic", kafkaTopic)

	// Repositories (for subscriptions and event status)
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
}
