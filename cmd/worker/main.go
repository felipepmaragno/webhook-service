// Worker service that consumes events from Kafka and delivers webhooks.
// Designed to run as multiple instances in a consumer group for horizontal scaling.
//
// The worker runs two concurrent processes:
// 1. Kafka consumer: processes new events from Kafka topic
// 2. Retry poller: polls database for events that need retry
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/felipemaragno/dispatch/internal/app"
	"github.com/felipemaragno/dispatch/internal/config"
)

func main() {
	cfg := config.ParseWorkerConfig()
	if err := cfg.Validate(); err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	if err := run(cfg, logger); err != nil {
		logger.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.WorkerConfig, logger *slog.Logger) error {
	service, err := app.StartWorkerService(context.Background(), cfg, logger)
	if err != nil {
		return err
	}
	defer func() { _ = service.Shutdown(context.Background()) }()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	return service.Shutdown(shutdownCtx)
}
