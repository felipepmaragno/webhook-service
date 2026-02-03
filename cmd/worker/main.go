// Worker service that consumes events from Kafka and delivers webhooks.
// Designed to run as multiple instances in a consumer group for horizontal scaling.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/felipemaragno/dispatch/internal/kafka"
	"github.com/felipemaragno/dispatch/internal/repository/postgres"
	"github.com/felipemaragno/dispatch/internal/resilience"
	"github.com/felipemaragno/dispatch/internal/retry"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Database connection
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/dispatch?sslmode=disable"
	}

	poolConfig, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		logger.Error("failed to parse database URL", "error", err)
		os.Exit(1)
	}

	// Configurable pool size for testing
	maxConns := int32(30)
	if v := os.Getenv("DB_MAX_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxConns = int32(n)
		}
	}
	poolConfig.MaxConns = maxConns
	poolConfig.MinConns = maxConns / 3

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
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

	// Repositories
	eventRepo := postgres.NewEventRepository(pool)
	subRepo := postgres.NewSubscriptionRepository(pool)

	// Resilience (Redis-backed for distributed rate limiting and circuit breaker)
	var rateLimiter resilience.RateLimiter
	var circuitBreaker resilience.CircuitBreaker

	redisURL := os.Getenv("REDIS_URL")
	if redisURL != "" {
		opt, err := redis.ParseURL(redisURL)
		if err != nil {
			logger.Error("failed to parse REDIS_URL", "error", err)
			os.Exit(1)
		}
		redisClient := redis.NewClient(opt)

		if err := redisClient.Ping(ctx).Err(); err != nil {
			logger.Warn("Redis not available, using in-memory resilience", "error", err)
			rateLimiter = resilience.NewInMemoryRateLimiterAdapter(resilience.DefaultRateLimiterConfig())
			circuitBreaker = resilience.NewInMemoryCircuitBreakerAdapter(resilience.DefaultCircuitBreakerConfig())
		} else {
			logger.Info("connected to Redis", "url", redisURL)
			rateLimiter = resilience.NewRedisRateLimiter(redisClient, resilience.DefaultRedisRateLimiterConfig(), logger)
			circuitBreaker = resilience.NewRedisCircuitBreaker(redisClient, resilience.DefaultRedisCircuitBreakerConfig(), logger)
		}
	} else {
		logger.Info("REDIS_URL not set, using in-memory resilience")
		rateLimiter = resilience.NewInMemoryRateLimiterAdapter(resilience.DefaultRateLimiterConfig())
		circuitBreaker = resilience.NewInMemoryCircuitBreakerAdapter(resilience.DefaultCircuitBreakerConfig())
	}

	// Kafka configuration
	kafkaBrokers := strings.Split(os.Getenv("KAFKA_BROKERS"), ",")
	if len(kafkaBrokers) == 0 || kafkaBrokers[0] == "" {
		kafkaBrokers = []string{"localhost:9092"}
	}

	kafkaTopic := os.Getenv("KAFKA_TOPIC")
	if kafkaTopic == "" {
		kafkaTopic = "events.pending"
	}

	kafkaGroup := os.Getenv("KAFKA_CONSUMER_GROUP")
	if kafkaGroup == "" {
		kafkaGroup = "dispatch-workers"
	}

	instanceID := os.Getenv("INSTANCE_ID")
	if instanceID == "" {
		instanceID = "worker-1"
	}

	// Delivery handler with functional options
	// - Rate limiter: 100 req/s fixed limit per subscription
	// - Circuit breaker: stops requests to failing destinations
	// - Semaphores: limit concurrent requests per subscription
	// - Retries go to DB, not Kafka
	handler := kafka.NewDeliveryHandler(
		eventRepo,
		subRepo,
		kafka.WithRetryPolicy(retry.DefaultPolicy()),
		kafka.WithRateLimiter(rateLimiter),
		kafka.WithCircuitBreaker(circuitBreaker),
		kafka.WithLogger(logger),
	)

	// Kafka consumer
	consumerConfig := kafka.DefaultConsumerConfig()
	consumerConfig.Brokers = kafkaBrokers
	consumerConfig.Topic = kafkaTopic
	consumerConfig.GroupID = kafkaGroup
	consumerConfig.InstanceID = instanceID

	consumer := kafka.NewConsumer(consumerConfig, handler, logger)
	consumer.Start(ctx)

	logger.Info("worker started",
		"instance_id", instanceID,
		"brokers", kafkaBrokers,
		"topic", kafkaTopic,
		"group", kafkaGroup,
	)

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down...")

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	cancel() // Cancel main context
	consumer.Stop()

	// Log final stats
	stats := consumer.Stats()
	logger.Info("consumer stats",
		"messages", stats.Messages,
		"bytes", stats.Bytes,
		"rebalances", stats.Rebalances,
		"errors", stats.Errors,
	)

	_ = shutdownCtx // Used for any additional cleanup

	logger.Info("shutdown complete")
}
