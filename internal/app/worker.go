package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/felipemaragno/dispatch/internal/config"
	"github.com/felipemaragno/dispatch/internal/kafka"
	"github.com/felipemaragno/dispatch/internal/observability"
	"github.com/felipemaragno/dispatch/internal/repository"
	"github.com/felipemaragno/dispatch/internal/repository/postgres"
	"github.com/felipemaragno/dispatch/internal/resilience"
	"github.com/felipemaragno/dispatch/internal/retention"
	"github.com/felipemaragno/dispatch/internal/retry"
)

type WorkerService struct {
	cancel           context.CancelFunc
	pool             *pgxpool.Pool
	redisClient      *redis.Client
	consumer         *kafka.Consumer
	retryPoller      *retry.Poller
	retentionCleaner *retention.Cleaner
	metricsServer    *http.Server
	metricsListener  net.Listener
	logger           *slog.Logger
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

	rateLimiter, redisClient := initRateLimiter(ctx, cfg, logger)

	metrics := observability.NewMetrics("dispatch_worker")
	readinessChecks := []observability.ReadinessCheck{
		databaseReadinessCheck(pool),
		kafkaReadinessCheck(cfg.KafkaBrokers, cfg.KafkaTopic),
	}
	if redisCheck := redisReadinessCheck(cfg.RedisURL, redisClient); redisCheck != nil {
		readinessChecks = append(readinessChecks, *redisCheck)
	}
	healthHandler := observability.NewHealthHandler(readinessChecks...)
	metricsServer, metricsListener, err := startMetricsServer(cfg.MetricsAddr, healthHandler, logger)
	if err != nil {
		cancel()
		if redisClient != nil {
			_ = redisClient.Close()
		}
		pool.Close()
		return nil, err
	}

	handler := buildDeliveryHandler(cfg, eventRepo, subRepo, rateLimiter, metrics, logger)
	consumer := startConsumer(ctx, cfg, handler, logger)
	retryPoller := startRetryPoller(ctx, cfg, eventRepo, handler, metrics, logger)
	retentionCleaner := startRetentionCleaner(ctx, cfg, pool, metrics, logger)
	healthHandler.SetReady(true)

	logger.Info("worker started",
		"instance_id", cfg.InstanceID,
		"brokers", cfg.KafkaBrokers,
		"topic", cfg.KafkaTopic,
		"group", cfg.KafkaConsumerGroup,
		"retry_poll_interval", cfg.RetryPollInterval,
		"retry_batch_size", cfg.RetryBatchSize,
		"retry_max_concurrent_batches", cfg.RetryMaxConcurrentBatches,
		"retry_lease_duration", cfg.RetryLeaseDuration,
		"attempt_body_retention", cfg.AttemptBodyRetention,
		"event_retention", cfg.EventRetention,
		"retention_cleanup_interval", cfg.RetentionCleanupInterval,
		"retention_batch_size", cfg.RetentionBatchSize,
		"metrics_addr", metricsListener.Addr().String(),
	)

	return &WorkerService{
		cancel:           cancel,
		pool:             pool,
		redisClient:      redisClient,
		consumer:         consumer,
		retryPoller:      retryPoller,
		retentionCleaner: retentionCleaner,
		metricsServer:    metricsServer,
		metricsListener:  metricsListener,
		logger:           logger,
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
	w.retentionCleaner.Stop()

	stats := w.consumer.Stats()
	w.logger.Info("consumer stats",
		"messages", stats.Messages,
		"bytes", stats.Bytes,
		"rebalances", stats.Rebalances,
		"errors", stats.Errors,
	)

	metricsErr := w.metricsServer.Shutdown(ctx)
	var redisErr error
	if w.redisClient != nil {
		redisErr = w.redisClient.Close()
	}
	w.pool.Close()
	w.logger.Info("shutdown complete")
	return errors.Join(metricsErr, redisErr)
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

func initRateLimiter(ctx context.Context, cfg config.WorkerConfig, logger *slog.Logger) (resilience.RateLimiter, *redis.Client) {
	if cfg.RedisURL == "" {
		logger.Info("using local destination max-delivery-rate limiter")
		return resilience.NewInMemoryRateLimiterAdapter(resilience.DefaultRateLimiterConfig()), nil
	}

	opt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		logger.Error("failed to parse REDIS_URL; max-delivery-rate will fail closed", "error", err)
		return resilience.NewFailClosedRateLimiter(time.Second), nil
	}
	configureRedisTimeouts(opt)

	client := redis.NewClient(opt)
	if err := client.Ping(ctx).Err(); err != nil {
		logger.Warn("Redis unavailable at startup; max-delivery-rate will fail closed", "error", err)
	} else {
		logger.Info("using Redis distributed max-delivery-rate limiter", "url", cfg.RedisURL)
	}
	return resilience.NewRedisRateLimiter(client, resilience.DefaultRedisRateLimiterConfig(), logger), client
}

func configureRedisTimeouts(opt *redis.Options) {
	const timeout = 500 * time.Millisecond
	opt.DialTimeout = timeout
	opt.ReadTimeout = timeout
	opt.WriteTimeout = timeout
}

func buildDeliveryHandler(
	cfg config.WorkerConfig,
	eventRepo *postgres.EventRepository,
	subRepo *postgres.SubscriptionRepository,
	rateLimiter resilience.RateLimiter,
	metrics *observability.Metrics,
	logger *slog.Logger,
) *kafka.DeliveryHandler {
	handlerOpts := []kafka.HandlerOption{
		kafka.WithRetryPolicy(retry.DefaultPolicy()),
		kafka.WithClaimIdentity(cfg.InstanceID, cfg.RetryLeaseDuration),
		kafka.WithRateLimiter(rateLimiter),
		kafka.WithLogger(logger),
		kafka.WithDeliveryObserver(deliveryMetricsObserver{metrics: metrics}),
	}
	return kafka.NewDeliveryHandler(eventRepo, subRepo, handlerOpts...)
}

type deliveryMetricsObserver struct {
	metrics *observability.Metrics
}

func (o deliveryMetricsObserver) Delivered() {
	o.metrics.EventsDelivered.Inc()
}

func (o deliveryMetricsObserver) Failed() {
	o.metrics.EventsFailed.Inc()
}

func (o deliveryMetricsObserver) Retrying() {
	o.metrics.EventsRetrying.Inc()
}

func (o deliveryMetricsObserver) Throttled() {
	o.metrics.EventsThrottled.Inc()
}

func (o deliveryMetricsObserver) AttemptStarted() {
	o.metrics.DeliveryAttempts.Inc()
}

func (o deliveryMetricsObserver) AttemptDuration(seconds float64) {
	o.metrics.DeliveryDuration.Observe(seconds)
}

func (o deliveryMetricsObserver) RateLimited(subscriptionID string) {
	o.metrics.RateLimiterRejections.WithLabelValues(subscriptionID).Inc()
}

func startMetricsServer(addr string, healthHandler *observability.HealthHandler, logger *slog.Logger) (*http.Server, net.Listener, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	server := &http.Server{
		Addr:    addr,
		Handler: workerObservabilityHandler(healthHandler),
	}
	go func() {
		logger.Info("starting metrics server", "addr", listener.Addr().String())
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server error", "error", err)
		}
	}()
	return server, listener, nil
}

func workerObservabilityHandler(healthHandler *observability.HealthHandler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	if healthHandler != nil {
		mux.HandleFunc("/health", healthHandler.Health)
		mux.HandleFunc("/ready", healthHandler.Ready)
	}
	return mux
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

func startRetentionCleaner(ctx context.Context, cfg config.WorkerConfig, pool *pgxpool.Pool, metrics *observability.Metrics, logger *slog.Logger) *retention.Cleaner {
	repo := postgres.NewRetentionRepository(pool)
	cleaner := retention.NewCleaner(repo, retention.Config{
		AttemptBodyRetention: cfg.AttemptBodyRetention,
		EventRetention:       cfg.EventRetention,
		Interval:             cfg.RetentionCleanupInterval,
		BatchSize:            cfg.RetentionBatchSize,
	}, logger, retentionMetricsObserver{metrics: metrics})
	go cleaner.Start(ctx)
	return cleaner
}

type retentionMetricsObserver struct {
	metrics *observability.Metrics
}

func (o retentionMetricsObserver) AttemptBodiesRedacted(count int64) {
	o.metrics.RetentionAttemptBodiesRedacted.Add(float64(count))
}

func (o retentionMetricsObserver) TerminalEventsDeleted(count int64) {
	o.metrics.RetentionTerminalEventsDeleted.Add(float64(count))
}

func (o retentionMetricsObserver) CycleFailed() {
	o.metrics.RetentionCleanupFailures.Inc()
}

func (o retentionMetricsObserver) CycleCompleted(duration time.Duration, completedAt time.Time) {
	o.metrics.RetentionCleanupDuration.Observe(duration.Seconds())
	o.metrics.RetentionLastSuccessTimestamp.Set(float64(completedAt.Unix()))
}
