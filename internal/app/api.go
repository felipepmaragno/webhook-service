package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felipemaragno/dispatch/internal/api"
	"github.com/felipemaragno/dispatch/internal/config"
	"github.com/felipemaragno/dispatch/internal/kafka"
	"github.com/felipemaragno/dispatch/internal/observability"
	"github.com/felipemaragno/dispatch/internal/repository/postgres"
)

type APIServer struct {
	server   *http.Server
	listener net.Listener
	pool     *pgxpool.Pool
	producer *kafka.Producer
}

func StartAPIServer(ctx context.Context, cfg config.APIConfig, logger *slog.Logger) (*APIServer, error) {
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	logger.Info("connected to database")

	producerConfig := kafka.DefaultProducerConfig()
	producerConfig.Brokers = cfg.KafkaBrokers
	producerConfig.Topic = cfg.KafkaTopic

	producer := kafka.NewProducer(producerConfig, logger)
	logger.Info("kafka producer initialized", "brokers", cfg.KafkaBrokers, "topic", cfg.KafkaTopic)

	eventRepo := postgres.NewEventRepository(pool)
	subRepo := postgres.NewSubscriptionRepository(pool)

	metrics := observability.NewMetrics("dispatch")
	healthHandler := observability.NewHealthHandler(pool)

	handler := api.NewHandler(producer, eventRepo, subRepo, logger).WithMetrics(metrics)
	router := api.NewRouter(api.RouterConfig{
		Handler:       handler,
		HealthHandler: healthHandler,
		Metrics:       metrics,
		Logger:        logger,
	})

	healthHandler.SetReady(true)

	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		_ = producer.Close()
		pool.Close()
		return nil, err
	}

	server := &http.Server{
		Addr:         cfg.Addr,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		logger.Info("starting HTTP server", "addr", listener.Addr().String())
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error", "error", err)
		}
	}()

	return &APIServer{
		server:   server,
		listener: listener,
		pool:     pool,
		producer: producer,
	}, nil
}

func (s *APIServer) Addr() string {
	return s.listener.Addr().String()
}

func (s *APIServer) URL() string {
	addr := s.Addr()
	if strings.HasPrefix(addr, "[::]") {
		addr = "127.0.0.1" + strings.TrimPrefix(addr, "[::]")
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	return fmt.Sprintf("http://%s", addr)
}

func (s *APIServer) Shutdown(ctx context.Context) error {
	var firstErr error
	if err := s.server.Shutdown(ctx); err != nil {
		firstErr = err
	}
	if err := s.producer.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	s.pool.Close()
	return firstErr
}
