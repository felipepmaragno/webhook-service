// Dispatch API service - HTTP API for event ingestion.
// Events are published directly to Kafka for delivery by workers.
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
	server, err := app.StartAPIServer(context.Background(), cfg, logger)
	if err != nil {
		return err
	}
	defer func() { _ = server.Shutdown(context.Background()) }()

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
