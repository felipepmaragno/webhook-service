package app

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/felipemaragno/dispatch/internal/config"
	"github.com/felipemaragno/dispatch/internal/kafka"
	"github.com/felipemaragno/dispatch/internal/observability"
	"github.com/felipemaragno/dispatch/internal/repository"
	"github.com/felipemaragno/dispatch/internal/repository/postgres"
	"github.com/felipemaragno/dispatch/internal/resilience"
	"github.com/felipemaragno/dispatch/internal/retry"
)

type WorkerService struct {
	cancel          context.CancelFunc
	pool            *pgxpool.Pool
	redisClient     *redis.Client
	consumer        *kafka.Consumer
	retryPoller     *retry.Poller
	metricsServer   *http.Server
	metricsListener net.Listener
	logger          *slog.Logger
}

func StartWorkerService(parent context.Context, cfg config.WorkerConfig, logger *slog.Logger) (*WorkerService, error) {
	ctx, cancel := context.WithCancel(parent)

	pool, err := connectDB(ctx, cfg)
	if err != nil {
		cancel()
		return nil, err
	}
	logger.Info("connected to database")

	eventRepo := postgres.NewEventRepository(pool)
	subRepo := postgres.NewSubscriptionRepository(pool)

	rateLimiter, circuitBreaker, semaphore, redisClient := initResilience(ctx, cfg, logger)

	metrics := observability.NewMetrics("dispatch_worker")
	metricsServer, metricsListener, err := startMetricsServer(cfg.MetricsAddr, logger)
	if err != nil {
		cancel()
		if redisClient != nil {
			_ = redisClient.Close()
		}
		pool.Close()
		return nil, err
	}

	handler := buildDeliveryHandler(eventRepo, subRepo, rateLimiter, circuitBreaker, semaphore, metrics, logger)
	consumer := startConsumer(ctx, cfg, handler, logger)
	retryPoller := startRetryPoller(ctx, cfg, eventRepo, handler, metrics, logger)

	logger.Info("worker started",
		"instance_id", cfg.InstanceID,
		"brokers", cfg.KafkaBrokers,
		"topic", cfg.KafkaTopic,
		"group", cfg.KafkaConsumerGroup,
		"retry_poll_interval", cfg.RetryPollInterval,
		"retry_batch_size", cfg.RetryBatchSize,
		"retry_max_concurrent_batches", cfg.RetryMaxConcurrentBatches,
		"retry_lease_duration", cfg.RetryLeaseDuration,
		"metrics_addr", metricsListener.Addr().String(),
	)

	return &WorkerService{
		cancel:          cancel,
		pool:            pool,
		redisClient:     redisClient,
		consumer:        consumer,
		retryPoller:     retryPoller,
		metricsServer:   metricsServer,
		metricsListener: metricsListener,
		logger:          logger,
	}, nil
}

func (w *WorkerService) MetricsAddr() string {
	addr := w.metricsListener.Addr().String()
	if strings.HasPrefix(addr, "[::]") {
		addr = "127.0.0.1" + strings.TrimPrefix(addr, "[::]")
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	return addr
}

func (w *WorkerService) Shutdown(ctx context.Context) error {
	w.cancel()
	w.consumer.Stop()
	w.retryPoller.Stop()

	stats := w.consumer.Stats()
	w.logger.Info("consumer stats",
		"messages", stats.Messages,
		"bytes", stats.Bytes,
		"rebalances", stats.Rebalances,
		"errors", stats.Errors,
	)

	var firstErr error
	if err := w.metricsServer.Shutdown(ctx); err != nil {
		firstErr = err
	}
	if w.redisClient != nil {
		if err := w.redisClient.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	w.pool.Close()
	w.logger.Info("shutdown complete")
	return firstErr
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

func initResilience(ctx context.Context, cfg config.WorkerConfig, logger *slog.Logger) (resilience.RateLimiter, resilience.CircuitBreaker, resilience.Semaphore, *redis.Client) {
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
				_ = redisClient.Close()
			} else {
				logger.Info("connected to Redis", "url", cfg.RedisURL)
				rateLimiter = resilience.NewRedisRateLimiter(redisClient, resilience.DefaultRedisRateLimiterConfig(), logger)
				circuitBreaker = resilience.NewRedisCircuitBreaker(redisClient, resilience.DefaultRedisCircuitBreakerConfig(), logger)
				semaphore = resilience.NewRedisSemaphore(redisClient, resilience.DefaultRedisSemaphoreConfig(), logger)
				return rateLimiter, circuitBreaker, semaphore, redisClient
			}
		}
	} else {
		logger.Info("REDIS_URL not set, using in-memory resilience")
	}

	rateLimiter = resilience.NewInMemoryRateLimiterAdapter(resilience.DefaultRateLimiterConfig())
	circuitBreaker = resilience.NewInMemoryCircuitBreakerAdapter(resilience.DefaultCircuitBreakerConfig())
	return rateLimiter, circuitBreaker, semaphore, nil
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

func startMetricsServer(addr string, logger *slog.Logger) (*http.Server, net.Listener, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	server := &http.Server{
		Addr:    addr,
		Handler: promhttp.Handler(),
	}
	go func() {
		logger.Info("starting metrics server", "addr", listener.Addr().String())
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server error", "error", err)
		}
	}()
	return server, listener, nil
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

func startRetryPoller(ctx context.Context, cfg config.WorkerConfig, eventRepo *postgres.EventRepository, handler *kafka.DeliveryHandler, metrics *observability.Metrics, logger *slog.Logger) *retry.Poller {
	pollerConfig := retry.DefaultPollerConfig()
	pollerConfig.PollInterval = cfg.RetryPollInterval
	pollerConfig.BatchSize = cfg.RetryBatchSize
	pollerConfig.MaxConcurrentBatches = cfg.RetryMaxConcurrentBatches
	pollerConfig.InstanceID = cfg.InstanceID
	pollerConfig.LeaseDuration = cfg.RetryLeaseDuration

	poller := retry.NewPoller(eventRepo, handler, pollerConfig, logger).WithMetrics(retry.PollerMetrics{
		Claimed:      func(count int) { metrics.RetryEventsClaimed.Add(float64(count)) },
		Reclaimed:    func(count int) { metrics.RetryEventsReclaimed.Add(float64(count)) },
		EmptyPoll:    func() { metrics.RetryEmptyPolls.Inc() },
		ClaimFailure: func() { metrics.RetryClaimFailures.Inc() },
		PersistenceFailure: func(staleOwner bool) {
			metrics.RetryPersistenceFailures.Inc()
			if staleOwner {
				metrics.RetryStaleOwnerRejections.Inc()
			}
		},
		ActiveBatches: func(delta int) { metrics.RetryActiveBatches.Add(float64(delta)) },
		SchedulingLag: func(seconds float64) { metrics.RetrySchedulingLag.Observe(seconds) },
		Backlog: func(stats repository.RetryBacklogStats, oldestDueAgeSeconds float64) {
			metrics.RetryDueEvents.Set(float64(stats.DueCount))
			metrics.RetryExpiredClaims.Set(float64(stats.ExpiredProcessingCount))
			metrics.RetryLeasedEvents.Set(float64(stats.LeasedCount))
			metrics.RetryOldestDueAge.Set(oldestDueAgeSeconds)
		},
	})
	go poller.Start(ctx)
	return poller
}

func circuitStateToFloat(state string) float64 {
	switch state {
	case "open":
		return 2
	case "half-open":
		return 1
	default:
		return 0
	}
}
