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
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/felipemaragno/dispatch/internal/config"
	"github.com/felipemaragno/dispatch/internal/kafka"
	"github.com/felipemaragno/dispatch/internal/observability"
	"github.com/felipemaragno/dispatch/internal/repository/postgres"
	"github.com/felipemaragno/dispatch/internal/resilience"
	"github.com/felipemaragno/dispatch/internal/retry"
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Database
	pool, err := connectDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	logger.Info("connected to database")

	// Repositories
	eventRepo := postgres.NewEventRepository(pool)
	subRepo := postgres.NewSubscriptionRepository(pool)

	// Resilience
	rateLimiter, circuitBreaker, semaphore := initResilience(ctx, cfg, logger)

	// Metrics
	metrics := observability.NewMetrics("dispatch_worker")
	metricsServer := startMetricsServer(cfg.MetricsAddr, logger)

	// Delivery handler
	handler := buildDeliveryHandler(eventRepo, subRepo, rateLimiter, circuitBreaker, semaphore, metrics, logger)

	// Kafka consumer
	consumer := startConsumer(ctx, cfg, handler, logger)

	// Retry poller
	retryPoller := startRetryPoller(ctx, cfg, eventRepo, handler, logger)

	logger.Info("worker started",
		"instance_id", cfg.InstanceID,
		"brokers", cfg.KafkaBrokers,
		"topic", cfg.KafkaTopic,
		"group", cfg.KafkaConsumerGroup,
		"retry_poll_interval", cfg.RetryPollInterval,
		"retry_batch_size", cfg.RetryBatchSize,
		"metrics_addr", cfg.MetricsAddr,
	)

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down...")
	return shutdown(ctx, cancel, consumer, retryPoller, metricsServer, logger)
}

func connectDB(ctx context.Context, cfg config.WorkerConfig) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	poolConfig.MaxConns = cfg.DBMaxConns
	poolConfig.MinConns = cfg.DBMaxConns / 3

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func initResilience(ctx context.Context, cfg config.WorkerConfig, logger *slog.Logger) (resilience.RateLimiter, resilience.CircuitBreaker, resilience.Semaphore) {
	var rateLimiter resilience.RateLimiter
	var circuitBreaker resilience.CircuitBreaker
	var semaphore resilience.Semaphore

	if cfg.RedisURL != "" {
		opt, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			logger.Error("failed to parse REDIS_URL, using in-memory resilience", "error", err)
		} else {
			redisClient := redis.NewClient(opt)
			if err := redisClient.Ping(ctx).Err(); err != nil {
				logger.Warn("Redis not available, using in-memory resilience", "error", err)
			} else {
				logger.Info("connected to Redis", "url", cfg.RedisURL)
				rateLimiter = resilience.NewRedisRateLimiter(redisClient, resilience.DefaultRedisRateLimiterConfig(), logger)
				circuitBreaker = resilience.NewRedisCircuitBreaker(redisClient, resilience.DefaultRedisCircuitBreakerConfig(), logger)
				semaphore = resilience.NewRedisSemaphore(redisClient, resilience.DefaultRedisSemaphoreConfig(), logger)
				return rateLimiter, circuitBreaker, semaphore
			}
		}
	} else {
		logger.Info("REDIS_URL not set, using in-memory resilience")
	}

	rateLimiter = resilience.NewInMemoryRateLimiterAdapter(resilience.DefaultRateLimiterConfig())
	circuitBreaker = resilience.NewInMemoryCircuitBreakerAdapter(resilience.DefaultCircuitBreakerConfig())
	return rateLimiter, circuitBreaker, semaphore
}

func buildDeliveryHandler(
	eventRepo *postgres.EventRepository,
	subRepo *postgres.SubscriptionRepository,
	rateLimiter resilience.RateLimiter,
	circuitBreaker resilience.CircuitBreaker,
	semaphore resilience.Semaphore,
	metrics *observability.Metrics,
	logger *slog.Logger,
) *kafka.DeliveryHandler {
	handlerOpts := []kafka.HandlerOption{
		kafka.WithRetryPolicy(retry.DefaultPolicy()),
		kafka.WithRateLimiter(rateLimiter),
		kafka.WithCircuitBreaker(circuitBreaker),
		kafka.WithLogger(logger),
		kafka.WithMetrics(
			func() { metrics.EventsDelivered.Inc() },
			func() { metrics.EventsFailed.Inc() },
			func() { metrics.EventsRetrying.Inc() },
			func() { metrics.EventsThrottled.Inc() },
			func(d float64) { metrics.DeliveryDuration.Observe(d) },
		),
		kafka.WithExtraMetrics(
			func(subID string) { metrics.RateLimiterRejections.WithLabelValues(subID).Inc() },
			func() { metrics.DeliveryAttempts.Inc() },
		),
		kafka.WithCircuitBreakerMetrics(
			func(subID, state string) {
				metrics.CircuitBreakerState.WithLabelValues(subID).Set(circuitStateToFloat(state))
			},
			func(subID string) {
				metrics.CircuitBreakerTrips.WithLabelValues(subID).Inc()
			},
		),
	}
	if semaphore != nil {
		handlerOpts = append(handlerOpts, kafka.WithSemaphore(semaphore))
	}
	return kafka.NewDeliveryHandler(eventRepo, subRepo, handlerOpts...)
}

func startMetricsServer(addr string, logger *slog.Logger) *http.Server {
	server := &http.Server{
		Addr:    addr,
		Handler: promhttp.Handler(),
	}
	go func() {
		logger.Info("starting metrics server", "addr", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server error", "error", err)
		}
	}()
	return server
}

func startConsumer(ctx context.Context, cfg config.WorkerConfig, handler *kafka.DeliveryHandler, logger *slog.Logger) *kafka.Consumer {
	consumerConfig := kafka.DefaultConsumerConfig()
	consumerConfig.Brokers = cfg.KafkaBrokers
	consumerConfig.Topic = cfg.KafkaTopic
	consumerConfig.GroupID = cfg.KafkaConsumerGroup
	consumerConfig.InstanceID = cfg.InstanceID

	consumer := kafka.NewConsumer(consumerConfig, handler, logger)
	consumer.Start(ctx)
	return consumer
}

func startRetryPoller(ctx context.Context, cfg config.WorkerConfig, eventRepo *postgres.EventRepository, handler *kafka.DeliveryHandler, logger *slog.Logger) *retry.Poller {
	pollerConfig := retry.DefaultPollerConfig()
	pollerConfig.PollInterval = cfg.RetryPollInterval
	pollerConfig.BatchSize = cfg.RetryBatchSize

	poller := retry.NewPoller(eventRepo, handler, pollerConfig, logger)
	go poller.Start(ctx)
	return poller
}

func shutdown(ctx context.Context, cancel context.CancelFunc, consumer *kafka.Consumer, retryPoller *retry.Poller, metricsServer *http.Server, logger *slog.Logger) error {
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	cancel() // Cancel main context
	consumer.Stop()
	retryPoller.Stop()

	// Log final stats
	stats := consumer.Stats()
	logger.Info("consumer stats",
		"messages", stats.Messages,
		"bytes", stats.Bytes,
		"rebalances", stats.Rebalances,
		"errors", stats.Errors,
	)

	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("failed to shutdown metrics server", "error", err)
	}

	_ = ctx // satisfy unused variable if needed
	logger.Info("shutdown complete")
	return nil
}

// circuitStateToFloat converts a circuit breaker state string to the float64
// value used in the Prometheus gauge (0=closed, 1=half-open, 2=open).
func circuitStateToFloat(state string) float64 {
	switch state {
	case "open":
		return 2
	case "half-open":
		return 1
	default: // "closed"
		return 0
	}
}
