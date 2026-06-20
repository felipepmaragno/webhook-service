package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/felipemaragno/dispatch/internal/config"
	"github.com/felipemaragno/dispatch/internal/domain"
	dispatchkafka "github.com/felipemaragno/dispatch/internal/kafka"
	"github.com/felipemaragno/dispatch/internal/observability"
	"github.com/felipemaragno/dispatch/internal/repository/postgres"
	"github.com/felipemaragno/dispatch/internal/retry"
	"github.com/felipemaragno/dispatch/internal/testutil"
)

func TestEndToEndValidation(t *testing.T) {
	t.Parallel()

	stack := setupE2EStack(t)

	t.Run("happy path", func(t *testing.T) {
		stack.receiver.reset()

		subscriptionID := "sub-e2e-happy"
		eventID := "evt-e2e-happy"

		createSubscription(t, stack.apiURL, map[string]any{
			"id":          subscriptionID,
			"url":         stack.receiver.url(),
			"event_types": []string{"order.created"},
			"secret":      "top-secret",
		})

		postEvent(t, stack.apiURL, map[string]any{
			"id":     eventID,
			"type":   "order.created",
			"source": "test-suite",
			"data": map[string]any{
				"attempt": 1,
			},
		})

		waitFor(t, 15*time.Second, func() error {
			if stack.receiver.requestCount() < 1 {
				return fmt.Errorf("webhook not received yet")
			}
			event, err := stack.eventRepo.GetByID(context.Background(), eventID)
			if err != nil {
				return err
			}
			if event.Status != domain.EventStatusDelivered {
				return fmt.Errorf("unexpected event status %s", event.Status)
			}
			attempts, err := stack.eventRepo.GetAttemptsByEventID(context.Background(), eventID)
			if err != nil {
				return err
			}
			if len(attempts) != 1 {
				return fmt.Errorf("expected 1 attempt, got %d", len(attempts))
			}
			req := stack.receiver.lastRequest()
			if req.EventID != eventID {
				return fmt.Errorf("expected event id %s, got %s", eventID, req.EventID)
			}
			if req.TraceID == "" {
				return fmt.Errorf("expected trace id header")
			}
			if err := verifyReceiverSignature(req, "top-secret"); err != nil {
				return err
			}
			return nil
		})
	})

	t.Run("retry path", func(t *testing.T) {
		stack.receiver.reset()
		stack.receiver.failFor(1)

		subscriptionID := "sub-e2e-retry"
		eventID := "evt-e2e-retry"

		createSubscription(t, stack.apiURL, map[string]any{
			"id":          subscriptionID,
			"url":         stack.receiver.url(),
			"event_types": []string{"invoice.failed"},
			"secret":      "retry-secret",
		})

		postEvent(t, stack.apiURL, map[string]any{
			"id":     eventID,
			"type":   "invoice.failed",
			"source": "test-suite",
			"data": map[string]any{
				"attempt": 2,
			},
		})

		waitFor(t, 20*time.Second, func() error {
			event, err := stack.eventRepo.GetByID(context.Background(), eventID)
			if err != nil {
				return err
			}
			if event.Status != domain.EventStatusDelivered {
				return fmt.Errorf("unexpected event status %s", event.Status)
			}
			attempts, err := stack.eventRepo.GetAttemptsByEventID(context.Background(), eventID)
			if err != nil {
				return err
			}
			if len(attempts) < 2 {
				return fmt.Errorf("expected at least 2 attempts, got %d", len(attempts))
			}
			if stack.receiver.requestCount() < 2 {
				return fmt.Errorf("expected at least 2 receiver requests, got %d", stack.receiver.requestCount())
			}
			if err := verifyReceiverSignature(stack.receiver.lastRequest(), "retry-secret"); err != nil {
				return err
			}
			return nil
		})
	})

	t.Run("rotated secret signs future delivery", func(t *testing.T) {
		stack.receiver.reset()

		subscriptionID := "sub-e2e-rotation"
		eventID := "evt-e2e-rotation"
		createSubscription(t, stack.apiURL, map[string]any{
			"id":          subscriptionID,
			"url":         stack.receiver.url(),
			"event_types": []string{"secret.rotated"},
			"secret":      "old-secret",
		})
		rotateSubscriptionSecret(t, stack.apiURL, subscriptionID, "new-secret")
		postEvent(t, stack.apiURL, map[string]any{
			"id":     eventID,
			"type":   "secret.rotated",
			"source": "test-suite",
			"data":   map[string]any{"rotation": true},
		})

		waitFor(t, 15*time.Second, func() error {
			if stack.receiver.requestCount() < 1 {
				return fmt.Errorf("rotated-secret webhook not received")
			}
			return verifyReceiverSignature(stack.receiver.lastRequest(), "new-secret")
		})
	})

	t.Run("expired retry claim is recovered", func(t *testing.T) {
		stack.receiver.reset()

		subscriptionID := "sub-e2e-lease-recovery"
		eventID := "evt-e2e-lease-recovery"
		createSubscription(t, stack.apiURL, map[string]any{
			"id":          subscriptionID,
			"url":         stack.receiver.url(),
			"event_types": []string{"lease.recovery"},
		})

		_, err := stack.dbPool.Exec(context.Background(), `
			INSERT INTO events (
				id, type, source, data, status, attempts, max_attempts,
				next_attempt_at, created_at, updated_at
			) VALUES ($1, $2, $3, $4, 'processing', 1, 5, NOW(), NOW(), NOW())
		`, eventID, "lease.recovery", "test-suite", `{"abandoned":true}`)
		if err != nil {
			t.Fatalf("seed abandoned event: %v", err)
		}
		_, err = stack.dbPool.Exec(context.Background(), `
			INSERT INTO deliveries (
				id, event_id, subscription_id, event_type, source, data,
				subscription_url, rate_limit, burst_size, concurrency_limit,
				status, attempts, max_attempts, next_attempt_at,
				processing_owner, processing_deadline, created_at, updated_at
			) VALUES (
				$1, $2, $3, 'lease.recovery', 'test-suite', $4,
				$5, 100, 10, 100,
				'processing', 1, 5, NOW(),
				$6, NOW() + INTERVAL '300 milliseconds', NOW(), NOW()
			)
		`, domain.DeliveryID(eventID, subscriptionID), eventID, subscriptionID, `{"abandoned":true}`, stack.receiver.url(), "abandoned-worker")
		if err != nil {
			t.Fatalf("seed abandoned delivery claim: %v", err)
		}

		waitFor(t, 15*time.Second, func() error {
			event, err := stack.eventRepo.GetByID(context.Background(), eventID)
			if err != nil {
				return err
			}
			if event.Status != domain.EventStatusDelivered {
				return fmt.Errorf("unexpected event status %s", event.Status)
			}
			delivery, err := stack.eventRepo.GetDeliveryByID(context.Background(), domain.DeliveryID(eventID, subscriptionID))
			if err != nil {
				return err
			}
			if delivery.ProcessingOwner != nil || delivery.ProcessingDeadline != nil {
				return fmt.Errorf("delivery lease metadata was not cleared")
			}
			if stack.receiver.requestCount() < 1 {
				return fmt.Errorf("recovered webhook not received")
			}
			return nil
		})
	})

	t.Run("seeded retry backlog drains without stuck leases", func(t *testing.T) {
		stack.receiver.reset()

		const backlogSize = 25
		createSubscription(t, stack.apiURL, map[string]any{
			"id":          "sub-e2e-retry-backlog",
			"url":         stack.receiver.url(),
			"event_types": []string{"backlog.retry"},
		})

		for i := 0; i < backlogSize; i++ {
			eventID := fmt.Sprintf("evt-e2e-backlog-%02d", i)
			_, err := stack.dbPool.Exec(context.Background(), `
				INSERT INTO events (
					id, type, source, data, status, attempts, max_attempts,
					next_attempt_at, created_at, updated_at
				) VALUES ($1, 'backlog.retry', 'test-suite', $2, 'retrying', 1, 5, NOW() - INTERVAL '1 second', NOW(), NOW())
			`, eventID, fmt.Sprintf(`{"index":%d}`, i))
			if err != nil {
				t.Fatalf("seed retry backlog event %d: %v", i, err)
			}
			_, err = stack.dbPool.Exec(context.Background(), `
				INSERT INTO deliveries (
					id, event_id, subscription_id, event_type, source, data,
					subscription_url, rate_limit, burst_size, concurrency_limit,
					status, attempts, max_attempts, next_attempt_at, created_at, updated_at
				) VALUES (
					$1, $2, 'sub-e2e-retry-backlog', 'backlog.retry', 'test-suite', $3,
					$4, 100, 10, 100,
					'retrying', 1, 5, NOW() - INTERVAL '1 second', NOW(), NOW()
				)
			`, domain.DeliveryID(eventID, "sub-e2e-retry-backlog"), eventID, fmt.Sprintf(`{"index":%d}`, i), stack.receiver.url())
			if err != nil {
				t.Fatalf("seed retry backlog delivery %d: %v", i, err)
			}
		}

		waitFor(t, 15*time.Second, func() error {
			var delivered, processing int
			if err := stack.dbPool.QueryRow(context.Background(), `
				SELECT
					COUNT(*) FILTER (WHERE status = 'delivered'),
					COUNT(*) FILTER (WHERE status = 'processing')
				FROM deliveries
				WHERE event_type = 'backlog.retry'
			`).Scan(&delivered, &processing); err != nil {
				return err
			}
			if delivered != backlogSize {
				return fmt.Errorf("delivered %d/%d backlog events; processing=%d", delivered, backlogSize, processing)
			}
			if processing != 0 {
				return fmt.Errorf("%d backlog events still hold processing leases", processing)
			}
			if stack.receiver.requestCount() != backlogSize {
				return fmt.Errorf("receiver observed %d/%d backlog calls", stack.receiver.requestCount(), backlogSize)
			}
			return nil
		})
	})
}

type e2eStack struct {
	apiURL       string
	apiServer    *APIServer
	consumer     *dispatchkafka.Consumer
	retryPoller  *retry.Poller
	cancelWorker context.CancelFunc
	dbPool       *pgxpool.Pool
	eventRepo    *postgres.EventRepository
	receiver     *receiverServer
}

func setupE2EStack(t *testing.T) *e2eStack {
	t.Helper()

	if err := testutil.DockerAvailable(); err != nil {
		t.Skipf("docker not available for testcontainers: %v", err)
	}

	ctx := context.Background()
	logWriter := io.Writer(io.Discard)
	if testing.Verbose() {
		logWriter = os.Stdout
	}
	logger := slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: slog.LevelDebug}))

	pgPool, pgCleanup, dbURL := setupPostgresDB(t)
	t.Cleanup(pgCleanup)

	redisURL, redisCleanup := setupRedis(t)
	t.Cleanup(redisCleanup)

	kafkaAddr, kafkaCleanup := setupKafka(t)
	t.Cleanup(kafkaCleanup)

	receiver := newReceiverServer(t)
	t.Cleanup(func() { receiver.close() })

	apiCfg := config.APIConfig{
		Addr:         "127.0.0.1:0",
		DatabaseURL:  dbURL,
		KafkaBrokers: []string{kafkaAddr},
		KafkaTopic:   "events.pending",
		LogLevel:     "info",
	}
	apiServer, err := StartAPIServer(ctx, apiCfg, logger)
	if err != nil {
		t.Fatalf("failed to start api server: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = apiServer.Shutdown(shutdownCtx)
	})

	workerCfg := config.WorkerConfig{
		DatabaseURL:               dbURL,
		DBMaxConns:                10,
		RedisURL:                  redisURL,
		KafkaBrokers:              []string{kafkaAddr},
		KafkaTopic:                "events.pending",
		KafkaConsumerGroup:        "dispatch-workers-e2e",
		InstanceID:                "worker-e2e",
		MetricsAddr:               "127.0.0.1:0",
		RetryPollInterval:         100 * time.Millisecond,
		RetryBatchSize:            10,
		RetryMaxConcurrentBatches: 3,
		RetryLeaseDuration:        500 * time.Millisecond,
		LogLevel:                  "debug",
	}
	consumer, retryPoller, cancelWorker, err := startE2EWorker(ctx, workerCfg, pgPool, logger)
	if err != nil {
		t.Fatalf("failed to start e2e worker: %v", err)
	}
	t.Cleanup(func() {
		cancelWorker()
		consumer.Stop()
		retryPoller.Stop()
	})

	stack := &e2eStack{
		apiURL:       apiServer.URL(),
		apiServer:    apiServer,
		consumer:     consumer,
		retryPoller:  retryPoller,
		cancelWorker: cancelWorker,
		dbPool:       pgPool,
		eventRepo:    postgres.NewEventRepository(pgPool),
		receiver:     receiver,
	}

	waitFor(t, 10*time.Second, func() error {
		resp, err := http.Get(stack.apiURL + "/health")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("unexpected health status %d", resp.StatusCode)
		}
		return nil
	})

	warmupPipeline(t, stack)
	stack.receiver.reset()

	return stack
}

func setupPostgresDB(t *testing.T) (*pgxpool.Pool, func(), string) {
	t.Helper()

	ctx := context.Background()
	pgContainer, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("failed to start postgres container: %v", err)
	}

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		t.Fatalf("failed to get postgres connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		t.Fatalf("failed to connect to postgres: %v", err)
	}

	if err := applyMigrations(ctx, pool); err != nil {
		pool.Close()
		_ = pgContainer.Terminate(ctx)
		t.Fatalf("failed to apply migrations: %v", err)
	}

	cleanup := func() {
		pool.Close()
		_ = pgContainer.Terminate(ctx)
	}
	return pool, cleanup, connStr
}

func startE2EWorker(parent context.Context, cfg config.WorkerConfig, pool *pgxpool.Pool, logger *slog.Logger) (*dispatchkafka.Consumer, *retry.Poller, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(parent)

	eventRepo := postgres.NewEventRepository(pool)
	subRepo := postgres.NewSubscriptionRepository(pool)

	rateLimiter, circuitBreaker, semaphore, redisClient := initResilience(ctx, cfg, logger)
	if redisClient != nil {
		go func() {
			<-ctx.Done()
			_ = redisClient.Close()
		}()
	}

	metrics := observability.NewMetrics("dispatch_worker_e2e")
	handler := buildDeliveryHandler(cfg, eventRepo, subRepo, rateLimiter, circuitBreaker, semaphore, metrics, logger)

	consumerConfig := dispatchkafka.DefaultConsumerConfig()
	consumerConfig.Brokers = cfg.KafkaBrokers
	consumerConfig.Topic = cfg.KafkaTopic
	consumerConfig.InstanceID = cfg.InstanceID

	reader := &nonGroupMessageReader{Reader: kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:         cfg.KafkaBrokers,
		Topic:           cfg.KafkaTopic,
		Partition:       0,
		MinBytes:        1,
		MaxBytes:        10e6,
		MaxWait:         consumerConfig.BatchTimeout,
		ReadLagInterval: -1,
		CommitInterval:  0,
		StartOffset:     kafkago.FirstOffset,
	})}
	consumer := dispatchkafka.NewConsumerWithReader(consumerConfig, reader, handler, logger)
	consumer.Start(ctx)

	poller := startRetryPoller(ctx, cfg, eventRepo, handler, metrics, logger)
	return consumer, poller, cancel, nil
}

type nonGroupMessageReader struct {
	*kafkago.Reader
}

func (r *nonGroupMessageReader) CommitMessages(ctx context.Context, msgs ...kafkago.Message) error {
	return nil
}

func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	migrationsDir := "../../migrations"
	migrations := []string{
		migrationsDir + "/001_initial_schema.up.sql",
		migrationsDir + "/002_add_throttled_status.up.sql",
		migrationsDir + "/003_add_retry_claim_lease.up.sql",
	}

	for _, path := range migrations {
		sql, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			return err
		}
	}
	return nil
}

func setupRedis(t *testing.T) (string, func()) {
	t.Helper()

	ctx := context.Background()
	redisContainer, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatalf("failed to start redis container: %v", err)
	}

	connStr, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		_ = redisContainer.Terminate(ctx)
		t.Fatalf("failed to get redis connection string: %v", err)
	}

	opts, err := redis.ParseURL(connStr)
	if err != nil {
		_ = redisContainer.Terminate(ctx)
		t.Fatalf("failed to parse redis connection string: %v", err)
	}

	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		_ = redisContainer.Terminate(ctx)
		t.Fatalf("failed to ping redis: %v", err)
	}
	_ = client.Close()

	return connStr, func() {
		_ = redisContainer.Terminate(ctx)
	}
}

func setupKafka(t *testing.T) (string, func()) {
	t.Helper()

	ctx := context.Background()
	brokerPort := reserveTCPPort(t)
	controllerPort := reserveTCPPort(t)
	hostAddr := fmt.Sprintf("127.0.0.1:%d", brokerPort)

	kafkaContainer, err := testcontainers.Run(ctx, "apache/kafka:3.7.0",
		testcontainers.WithEnv(map[string]string{
			"KAFKA_NODE_ID":                                  "1",
			"KAFKA_PROCESS_ROLES":                            "broker,controller",
			"KAFKA_CONTROLLER_QUORUM_VOTERS":                 fmt.Sprintf("1@localhost:%d", controllerPort),
			"KAFKA_LISTENERS":                                fmt.Sprintf("PLAINTEXT://:%d,CONTROLLER://:%d", brokerPort, controllerPort),
			"KAFKA_ADVERTISED_LISTENERS":                     fmt.Sprintf("PLAINTEXT://%s", hostAddr),
			"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP":           "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT",
			"KAFKA_CONTROLLER_LISTENER_NAMES":                "CONTROLLER",
			"KAFKA_INTER_BROKER_LISTENER_NAME":               "PLAINTEXT",
			"KAFKA_AUTO_CREATE_TOPICS_ENABLE":                "true",
			"KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR":         "1",
			"KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR": "1",
			"KAFKA_TRANSACTION_STATE_LOG_MIN_ISR":            "1",
			"CLUSTER_ID":                                     "MkU3OEVBNTcwNTJENDM2Qk",
		}),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.NetworkMode = "host"
		}),
		testcontainers.WithWaitStrategy(
			wait.ForLog("Kafka Server started").WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("failed to start kafka container: %v", err)
	}

	if err := waitForKafka(ctx, hostAddr); err != nil {
		_ = kafkaContainer.Terminate(ctx)
		t.Fatalf("failed waiting for kafka readiness: %v", err)
	}

	exitCode, reader, err := kafkaContainer.Exec(ctx, []string{
		"/opt/kafka/bin/kafka-topics.sh",
		"--bootstrap-server", hostAddr,
		"--create",
		"--if-not-exists",
		"--topic", "events.pending",
		"--partitions", "1",
		"--replication-factor", "1",
	})
	if err != nil {
		_ = kafkaContainer.Terminate(ctx)
		t.Fatalf("failed to create kafka topic: %v", err)
	}
	if exitCode != 0 {
		output, _ := io.ReadAll(reader)
		_ = kafkaContainer.Terminate(ctx)
		t.Fatalf("failed to create kafka topic, exit=%d output=%s", exitCode, string(output))
	}

	return hostAddr, func() {
		_ = kafkaContainer.Terminate(ctx)
	}
}

func warmupPipeline(t *testing.T, stack *e2eStack) {
	t.Helper()

	const (
		subscriptionID = "sub-e2e-warmup"
		eventID        = "evt-e2e-warmup"
	)

	createSubscription(t, stack.apiURL, map[string]any{
		"id":          subscriptionID,
		"url":         stack.receiver.url(),
		"event_types": []string{"warmup.ready"},
	})

	postEvent(t, stack.apiURL, map[string]any{
		"id":     eventID,
		"type":   "warmup.ready",
		"source": "test-suite",
		"data": map[string]any{
			"warmup": true,
		},
	})

	waitFor(t, 15*time.Second, func() error {
		if stack.receiver.requestCount() < 1 {
			return fmt.Errorf("warmup webhook not received yet")
		}
		event, err := stack.eventRepo.GetByID(context.Background(), eventID)
		if err != nil {
			return err
		}
		if event.Status != domain.EventStatusDelivered {
			return fmt.Errorf("warmup event status %s", event.Status)
		}
		return nil
	})
}

func waitForKafka(ctx context.Context, broker string) error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := kafkago.DialContext(ctx, "tcp", broker)
		if err == nil {
			controller, err := conn.Controller()
			_ = conn.Close()
			if err == nil {
				controllerAddr := net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port))
				controllerConn, err := kafkago.DialContext(ctx, "tcp", controllerAddr)
				if err == nil {
					err = controllerConn.CreateTopics(kafkago.TopicConfig{
						Topic:             "events.pending",
						NumPartitions:     1,
						ReplicationFactor: 1,
					})
					_ = controllerConn.Close()
					if err == nil {
						return nil
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("kafka broker %s did not become ready for topic operations", broker)
}

func reserveTCPPort(t *testing.T) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve tcp port: %v", err)
	}
	defer l.Close()

	return l.Addr().(*net.TCPAddr).Port
}

type receiverRequest struct {
	EventID   string
	TraceID   string
	Timestamp string
	Signature string
	Body      []byte
}

type receiverServer struct {
	server       *http.Server
	listener     net.Listener
	failuresLeft atomic.Int32
	requests     atomic.Int32
	mu           sync.Mutex
	last         receiverRequest
}

func newReceiverServer(t *testing.T) *receiverServer {
	t.Helper()

	receiver := &receiverServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receiver.mu.Lock()
		receiver.last = receiverRequest{
			EventID:   r.Header.Get("X-Event-ID"),
			TraceID:   r.Header.Get("X-Trace-ID"),
			Timestamp: r.Header.Get("X-Dispatch-Timestamp"),
			Signature: r.Header.Get("X-Dispatch-Signature"),
			Body:      body,
		}
		receiver.mu.Unlock()
		receiver.requests.Add(1)

		if receiver.failuresLeft.Load() > 0 {
			receiver.failuresLeft.Add(-1)
			http.Error(w, "simulated failure", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start receiver listener: %v", err)
	}
	server := &http.Server{Handler: mux}

	receiver.server = server
	receiver.listener = listener

	go func() {
		_ = server.Serve(listener)
	}()

	return receiver
}

func (r *receiverServer) url() string {
	return "http://" + r.listener.Addr().String()
}

func (r *receiverServer) failFor(n int) {
	r.failuresLeft.Store(int32(n))
}

func (r *receiverServer) requestCount() int {
	return int(r.requests.Load())
}

func (r *receiverServer) lastRequest() receiverRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

func (r *receiverServer) reset() {
	r.failuresLeft.Store(0)
	r.requests.Store(0)
	r.mu.Lock()
	r.last = receiverRequest{}
	r.mu.Unlock()
}

func (r *receiverServer) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.server.Shutdown(ctx)
}

func verifyReceiverSignature(req receiverRequest, secret string) error {
	if req.Timestamp == "" || req.Signature == "" {
		return fmt.Errorf("signature headers are incomplete")
	}
	timestamp, err := strconv.ParseInt(req.Timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid signature timestamp: %w", err)
	}
	if delta := time.Since(time.Unix(timestamp, 0)); delta < -5*time.Minute || delta > 5*time.Minute {
		return fmt.Errorf("signature timestamp outside tolerance: %s", delta)
	}
	if !strings.HasPrefix(req.Signature, "v1=") {
		return fmt.Errorf("unsupported signature version")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(req.Timestamp))
	_, _ = mac.Write([]byte{'.'})
	_, _ = mac.Write(req.Body)
	want := fmt.Sprintf("v1=%x", mac.Sum(nil))
	if !hmac.Equal([]byte(req.Signature), []byte(want)) {
		return fmt.Errorf("signature does not match raw body")
	}
	return nil
}

func createSubscription(t *testing.T, apiURL string, body map[string]any) {
	t.Helper()

	resp := postJSON(t, apiURL+"/subscriptions", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected subscription status %d: %s", resp.StatusCode, string(raw))
	}
}

func postEvent(t *testing.T, apiURL string, body map[string]any) {
	t.Helper()

	resp := postJSON(t, apiURL+"/events", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected event status %d: %s", resp.StatusCode, string(raw))
	}
}

func rotateSubscriptionSecret(t *testing.T, apiURL, subscriptionID, secret string) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"secret": secret})
	if err != nil {
		t.Fatalf("marshal rotation request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut, apiURL+"/subscriptions/"+subscriptionID+"/secret", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("create rotation request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("rotate subscription secret: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotation status %d: %s", resp.StatusCode, string(body))
	}
	if bytes.Contains(body, []byte(secret)) || bytes.Contains(body, []byte(`"secret"`)) {
		t.Fatalf("rotation response exposed secret: %s", string(body))
	}
}

func postJSON(t *testing.T, url string, body map[string]any) *http.Response {
	t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("failed to marshal request body: %v", err)
	}

	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("failed to post %s: %v", url, err)
	}
	return resp
}

func waitFor(t *testing.T, timeout time.Duration, fn func() error) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := fn(); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %v", timeout, lastErr)
}
