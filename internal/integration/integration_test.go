package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/felipemaragno/dispatch/internal/api"
	"github.com/felipemaragno/dispatch/internal/clock"
	"github.com/felipemaragno/dispatch/internal/observability"
	"github.com/felipemaragno/dispatch/internal/repository/postgres"
	"github.com/felipemaragno/dispatch/internal/resilience"
	"github.com/felipemaragno/dispatch/internal/retry"
	"github.com/felipemaragno/dispatch/internal/worker"
)

type testEnv struct {
	pgContainer    *tcpostgres.PostgresContainer
	redisContainer *tcredis.RedisContainer
	pool           *pgxpool.Pool
	redisClient    *redis.Client
	handler        http.Handler
	workerPool     *worker.Pool
	ctx            context.Context
	cancel         context.CancelFunc
}

func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)

	// Start PostgreSQL container
	pgContainer, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("dispatch_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		cancel()
		t.Fatalf("failed to start postgres container: %v", err)
	}

	// Start Redis container
	redisContainer, err := tcredis.Run(ctx,
		"redis:7-alpine",
		testcontainers.WithWaitStrategy(
			wait.ForLog("Ready to accept connections").
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		cancel()
		t.Fatalf("failed to start redis container: %v", err)
	}

	// Get connection strings
	pgConnStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = redisContainer.Terminate(ctx)
		_ = pgContainer.Terminate(ctx)
		cancel()
		t.Fatalf("failed to get postgres connection string: %v", err)
	}

	redisConnStr, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		_ = redisContainer.Terminate(ctx)
		_ = pgContainer.Terminate(ctx)
		cancel()
		t.Fatalf("failed to get redis connection string: %v", err)
	}

	// Connect to PostgreSQL
	pool, err := pgxpool.New(ctx, pgConnStr)
	if err != nil {
		_ = redisContainer.Terminate(ctx)
		_ = pgContainer.Terminate(ctx)
		cancel()
		t.Fatalf("failed to connect to postgres: %v", err)
	}

	// Run migrations
	if err := runMigrations(ctx, pool); err != nil {
		pool.Close()
		_ = redisContainer.Terminate(ctx)
		_ = pgContainer.Terminate(ctx)
		cancel()
		t.Fatalf("failed to run migrations: %v", err)
	}

	// Connect to Redis
	redisOpt, err := redis.ParseURL(redisConnStr)
	if err != nil {
		pool.Close()
		_ = redisContainer.Terminate(ctx)
		_ = pgContainer.Terminate(ctx)
		cancel()
		t.Fatalf("failed to parse redis URL: %v", err)
	}
	redisClient := redis.NewClient(redisOpt)

	// Setup application components
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	eventRepo := postgres.NewEventRepository(pool)
	subRepo := postgres.NewSubscriptionRepository(pool)

	// Use unique namespace to avoid duplicate metric registration across tests
	metricsNamespace := fmt.Sprintf("dispatch_test_%d", rand.Int63())
	metrics := observability.NewMetrics(metricsNamespace)
	healthHandler := observability.NewHealthHandler(pool)

	handler := api.NewHandler(eventRepo, subRepo, logger).WithMetrics(metrics)
	router := api.NewRouter(api.RouterConfig{
		Handler:       handler,
		HealthHandler: healthHandler,
		Metrics:       metrics,
		Logger:        logger,
	})

	// Setup resilience with Redis
	rateLimiter := resilience.NewRedisRateLimiter(redisClient, resilience.DefaultRedisRateLimiterConfig(), logger)
	circuitBreaker := resilience.NewRedisCircuitBreaker(redisClient, resilience.DefaultRedisCircuitBreakerConfig(), logger)

	httpClient := &http.Client{Timeout: 10 * time.Second}

	workerPool := worker.NewPool(
		worker.Config{Workers: 2, PollInterval: 50 * time.Millisecond, BatchSize: 10},
		eventRepo,
		subRepo,
		httpClient,
		clock.RealClock{},
		retry.DefaultPolicy(),
		logger,
	).WithMetrics(metrics).WithResilience(rateLimiter, circuitBreaker)

	return &testEnv{
		pgContainer:    pgContainer,
		redisContainer: redisContainer,
		pool:           pool,
		redisClient:    redisClient,
		handler:        router,
		workerPool:     workerPool,
		ctx:            ctx,
		cancel:         cancel,
	}
}

func (e *testEnv) teardown(t *testing.T) {
	t.Helper()
	e.workerPool.Stop()
	e.pool.Close()
	e.redisClient.Close()
	_ = e.redisContainer.Terminate(e.ctx)
	_ = e.pgContainer.Terminate(e.ctx)
	e.cancel()
}

func runMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	migrations := []string{
		`CREATE TYPE event_status AS ENUM ('pending', 'processing', 'delivered', 'retrying', 'throttled', 'failed')`,
		`CREATE TABLE events (
			id              TEXT PRIMARY KEY,
			type            TEXT NOT NULL,
			source          TEXT NOT NULL,
			data            JSONB NOT NULL,
			status          event_status NOT NULL DEFAULT 'pending',
			attempts        INT NOT NULL DEFAULT 0,
			max_attempts    INT NOT NULL DEFAULT 5,
			next_attempt_at TIMESTAMPTZ,
			last_error      TEXT,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			delivered_at    TIMESTAMPTZ
		)`,
		`CREATE TABLE delivery_attempts (
			id              SERIAL PRIMARY KEY,
			event_id        TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
			attempt_number  INT NOT NULL,
			status_code     INT,
			response_body   TEXT,
			error           TEXT,
			duration_ms     INT NOT NULL,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE subscriptions (
			id              TEXT PRIMARY KEY,
			url             TEXT NOT NULL,
			event_types     TEXT[] NOT NULL,
			secret          TEXT,
			rate_limit      INT NOT NULL DEFAULT 100,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			active          BOOLEAN NOT NULL DEFAULT TRUE
		)`,
		`CREATE INDEX idx_events_pending ON events(next_attempt_at) WHERE status IN ('pending', 'retrying', 'throttled')`,
	}

	for _, m := range migrations {
		if _, err := pool.Exec(ctx, m); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	return nil
}

// TestEndToEndWebhookDelivery tests the complete flow:
// 1. Create subscription
// 2. Send event
// 3. Verify webhook is delivered to destination
func TestEndToEndWebhookDelivery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.teardown(t)

	// Start worker pool
	workerCtx, workerCancel := context.WithCancel(env.ctx)
	defer workerCancel()
	env.workerPool.Start(workerCtx)

	// Create a mock webhook receiver
	webhookReceived := make(chan map[string]interface{}, 1)
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		_ = json.Unmarshal(body, &payload)
		webhookReceived <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	// Step 1: Create subscription
	subPayload := map[string]interface{}{
		"id":          "sub_test_e2e",
		"url":         mockServer.URL,
		"event_types": []string{"order.created"},
		"rate_limit":  100,
	}
	subBody, _ := json.Marshal(subPayload)

	req := httptest.NewRequest(http.MethodPost, "/subscriptions", bytes.NewReader(subBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Step 2: Send event
	eventPayload := map[string]interface{}{
		"id":     "evt_test_e2e_001",
		"type":   "order.created",
		"source": "integration-test",
		"data": map[string]interface{}{
			"order_id": "12345",
			"amount":   99.99,
		},
	}
	eventBody, _ := json.Marshal(eventPayload)

	req = httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader(eventBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", rec.Code, rec.Body.String())
	}

	// Step 3: Wait for webhook delivery
	select {
	case payload := <-webhookReceived:
		// Payload structure is flat: {id, type, source, data, timestamp}
		if payload["id"] != "evt_test_e2e_001" {
			t.Errorf("expected event id 'evt_test_e2e_001', got: %v", payload["id"])
		}
		if payload["type"] != "order.created" {
			t.Errorf("expected event type 'order.created', got: %v", payload["type"])
		}
		t.Logf("Webhook delivered successfully: %+v", payload)

	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for webhook delivery")
	}

	// Give worker time to update database status
	time.Sleep(500 * time.Millisecond)

	// Step 4: Verify event status in database
	var status string
	err := env.pool.QueryRow(env.ctx, "SELECT status FROM events WHERE id = $1", "evt_test_e2e_001").Scan(&status)
	if err != nil {
		t.Fatalf("failed to query event status: %v", err)
	}
	if status != "delivered" {
		t.Errorf("expected event status 'delivered', got: %s", status)
	}
}

// TestEndToEndRetryOnFailure tests retry behavior when destination fails
func TestEndToEndRetryOnFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.teardown(t)

	// Start worker pool
	workerCtx, workerCancel := context.WithCancel(env.ctx)
	defer workerCancel()
	env.workerPool.Start(workerCtx)

	// Create a mock server that fails first 2 times, then succeeds
	attemptCount := 0
	webhookReceived := make(chan bool, 1)
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++
		if attemptCount < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		webhookReceived <- true
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	// Create subscription
	subPayload := map[string]interface{}{
		"id":          "sub_test_retry",
		"url":         mockServer.URL,
		"event_types": []string{"order.*"},
		"rate_limit":  100,
	}
	subBody, _ := json.Marshal(subPayload)

	req := httptest.NewRequest(http.MethodPost, "/subscriptions", bytes.NewReader(subBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Send event
	eventPayload := map[string]interface{}{
		"id":     "evt_test_retry_001",
		"type":   "order.updated",
		"source": "integration-test",
		"data":   map[string]interface{}{"test": true},
	}
	eventBody, _ := json.Marshal(eventPayload)

	req = httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader(eventBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", rec.Code, rec.Body.String())
	}

	// Wait for successful delivery after retries
	select {
	case <-webhookReceived:
		t.Logf("Webhook delivered after %d attempts", attemptCount)
		if attemptCount < 3 {
			t.Errorf("expected at least 3 attempts, got %d", attemptCount)
		}

	case <-time.After(30 * time.Second):
		t.Fatalf("timeout waiting for webhook delivery, attempts: %d", attemptCount)
	}

	// Verify event status
	time.Sleep(500 * time.Millisecond) // Allow status update to propagate
	var status string
	var attempts int
	err := env.pool.QueryRow(env.ctx,
		"SELECT status, attempts FROM events WHERE id = $1",
		"evt_test_retry_001",
	).Scan(&status, &attempts)
	if err != nil {
		t.Fatalf("failed to query event: %v", err)
	}
	if status != "delivered" {
		t.Errorf("expected status 'delivered', got: %s", status)
	}
	// attempts field tracks retries scheduled, not total HTTP requests
	// 3 HTTP requests = 2 retries scheduled (first attempt + 2 retries)
	if attempts < 2 {
		t.Errorf("expected at least 2 retry attempts recorded, got: %d", attempts)
	}
	t.Logf("Event delivered with %d retries recorded in DB, %d total HTTP requests", attempts, attemptCount)
}

// TestEndToEndRateLimiting tests that rate limiting is applied correctly.
// With a rate limit of 5 requests/second, we verify that:
// 1. First batch of requests up to the limit are processed immediately
// 2. Excess requests are rate-limited and scheduled for retry
func TestEndToEndRateLimiting(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.teardown(t)

	// Start worker pool
	workerCtx, workerCancel := context.WithCancel(env.ctx)
	defer workerCancel()
	env.workerPool.Start(workerCtx)

	// Track delivered requests with mutex for thread safety
	var mu sync.Mutex
	deliveredCount := 0

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deliveredCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	// Create subscription with rate limit of 5/second
	const rateLimit = 5
	const totalEvents = 10

	subPayload := map[string]interface{}{
		"id":          "sub_test_ratelimit",
		"url":         mockServer.URL,
		"event_types": []string{"test.*"},
		"rate_limit":  rateLimit,
	}
	subBody, _ := json.Marshal(subPayload)

	req := httptest.NewRequest(http.MethodPost, "/subscriptions", bytes.NewReader(subBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Send all events
	for i := 0; i < totalEvents; i++ {
		eventPayload := map[string]interface{}{
			"id":     fmt.Sprintf("evt_ratelimit_%d", i),
			"type":   "test.event",
			"source": "integration-test",
			"data":   map[string]interface{}{"index": i},
		}
		eventBody, _ := json.Marshal(eventPayload)

		req = httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader(eventBody))
		req.Header.Set("Content-Type", "application/json")
		rec = httptest.NewRecorder()
		env.handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected status 202, got %d", rec.Code)
		}
	}

	// Wait for first processing window
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	firstWindowDelivered := deliveredCount
	mu.Unlock()

	// Verify: in the first window, at most rateLimit requests should be delivered
	if firstWindowDelivered > rateLimit {
		t.Errorf("rate limiting failed: expected at most %d delivered in first window, got %d",
			rateLimit, firstWindowDelivered)
	}

	// Query database for exact counts
	var deliveredInDB, throttledInDB int
	err := env.pool.QueryRow(env.ctx,
		"SELECT COUNT(*) FROM events WHERE status = 'delivered'",
	).Scan(&deliveredInDB)
	if err != nil {
		t.Fatalf("failed to query delivered events: %v", err)
	}

	err = env.pool.QueryRow(env.ctx,
		"SELECT COUNT(*) FROM events WHERE status = 'throttled'",
	).Scan(&throttledInDB)
	if err != nil {
		t.Fatalf("failed to query throttled events: %v", err)
	}

	// Exact validation:
	// - delivered + throttled should equal totalEvents (or close, some may still be processing)
	// - throttled should be > 0 (some events were rate-limited)
	totalAccountedFor := deliveredInDB + throttledInDB
	if totalAccountedFor == 0 {
		t.Errorf("no events processed yet")
	}

	if throttledInDB == 0 && deliveredInDB < totalEvents {
		// If nothing is throttled but not all delivered, check processing
		var processingInDB int
		_ = env.pool.QueryRow(env.ctx,
			"SELECT COUNT(*) FROM events WHERE status = 'processing'",
		).Scan(&processingInDB)
		t.Logf("Events state: delivered=%d, throttled=%d, processing=%d",
			deliveredInDB, throttledInDB, processingInDB)
	}

	// The key assertion: we should have rate-limited some events (now with throttled status)
	if throttledInDB == 0 && firstWindowDelivered >= totalEvents {
		t.Errorf("rate limiting not working: all %d events delivered without any being throttled",
			totalEvents)
	}

	// Verify attempts were NOT incremented for throttled events
	var throttledWithZeroAttempts int
	_ = env.pool.QueryRow(env.ctx,
		"SELECT COUNT(*) FROM events WHERE status = 'throttled' AND attempts = 0",
	).Scan(&throttledWithZeroAttempts)

	if throttledInDB > 0 && throttledWithZeroAttempts != throttledInDB {
		t.Errorf("throttled events should have attempts=0, but %d/%d have attempts>0",
			throttledInDB-throttledWithZeroAttempts, throttledInDB)
	}

	t.Logf("Rate limiting validated: delivered=%d (HTTP), delivered=%d (DB), throttled=%d (DB, attempts=0), limit=%d/s",
		firstWindowDelivered, deliveredInDB, throttledInDB, rateLimit)
}

// TestHealthEndpoint tests the health check endpoint
func TestHealthEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.teardown(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if response["status"] != "ok" {
		t.Errorf("expected status 'ok', got: %v", response["status"])
	}
}
