package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/felipemaragno/dispatch/internal/api"
	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/repository/postgres"
)

// BenchmarkEventIngestion measures how many events/second can be ingested via API.
// This tests: HTTP parsing → validation → PostgreSQL INSERT
func BenchmarkEventIngestion(b *testing.B) {
	ctx := context.Background()

	// Setup PostgreSQL container
	pgContainer, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("benchmark"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		b.Fatalf("failed to start postgres: %v", err)
	}
	defer func() { _ = pgContainer.Terminate(ctx) }()

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		b.Fatalf("failed to get connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		b.Fatalf("failed to connect: %v", err)
	}
	defer pool.Close()

	if err := runMigrations(ctx, pool); err != nil {
		b.Fatalf("failed to run migrations: %v", err)
	}

	eventRepo := postgres.NewEventRepository(pool)
	subRepo := postgres.NewSubscriptionRepository(pool)
	handler := api.NewHandler(eventRepo, subRepo, nil)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		eventPayload := map[string]interface{}{
			"id":     fmt.Sprintf("evt_bench_%d", i),
			"type":   "benchmark.test",
			"source": "benchmark",
			"data":   map[string]interface{}{"index": i},
		}
		body, _ := json.Marshal(eventPayload)

		req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.CreateEvent(rec, req)

		if rec.Code != http.StatusAccepted {
			b.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
		}
	}
}

// BenchmarkEventIngestionParallel measures concurrent ingestion throughput.
func BenchmarkEventIngestionParallel(b *testing.B) {
	ctx := context.Background()

	pgContainer, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("benchmark"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		b.Fatalf("failed to start postgres: %v", err)
	}
	defer func() { _ = pgContainer.Terminate(ctx) }()

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		b.Fatalf("failed to get connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		b.Fatalf("failed to connect: %v", err)
	}
	defer pool.Close()

	if err := runMigrations(ctx, pool); err != nil {
		b.Fatalf("failed to run migrations: %v", err)
	}

	eventRepo := postgres.NewEventRepository(pool)
	subRepo := postgres.NewSubscriptionRepository(pool)
	handler := api.NewHandler(eventRepo, subRepo, nil)

	var counter int64

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := atomic.AddInt64(&counter, 1)
			eventPayload := map[string]interface{}{
				"id":     fmt.Sprintf("evt_bench_p_%d", i),
				"type":   "benchmark.test",
				"source": "benchmark",
				"data":   map[string]interface{}{"index": i},
			}
			body, _ := json.Marshal(eventPayload)

			req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.CreateEvent(rec, req)

			if rec.Code != http.StatusAccepted {
				b.Errorf("expected 202, got %d", rec.Code)
			}
		}
	})
}

// BenchmarkEventIngestionParallelBatched measures concurrent ingestion with batching.
func BenchmarkEventIngestionParallelBatched(b *testing.B) {
	ctx := context.Background()

	pgContainer, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("benchmark"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		b.Fatalf("failed to start postgres: %v", err)
	}
	defer func() { _ = pgContainer.Terminate(ctx) }()

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		b.Fatalf("failed to get connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		b.Fatalf("failed to connect: %v", err)
	}
	defer pool.Close()

	if err := runMigrations(ctx, pool); err != nil {
		b.Fatalf("failed to run migrations: %v", err)
	}

	eventRepo := postgres.NewEventRepository(pool).WithBatcher(postgres.DefaultBatcherConfig())
	defer func() { _ = eventRepo.Shutdown(ctx) }()

	subRepo := postgres.NewSubscriptionRepository(pool)
	handler := api.NewHandler(eventRepo, subRepo, nil)

	var counter int64

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := atomic.AddInt64(&counter, 1)
			eventPayload := map[string]interface{}{
				"id":     fmt.Sprintf("evt_bench_pb_%d", i),
				"type":   "benchmark.test",
				"source": "benchmark",
				"data":   map[string]interface{}{"index": i},
			}
			body, _ := json.Marshal(eventPayload)

			req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.CreateEvent(rec, req)

			if rec.Code != http.StatusAccepted {
				b.Errorf("expected 202, got %d", rec.Code)
			}
		}
	})
}

// BenchmarkDatabaseInsert measures raw PostgreSQL INSERT performance (no batching).
func BenchmarkDatabaseInsert(b *testing.B) {
	ctx := context.Background()

	pgContainer, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("benchmark"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		b.Fatalf("failed to start postgres: %v", err)
	}
	defer func() { _ = pgContainer.Terminate(ctx) }()

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		b.Fatalf("failed to get connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		b.Fatalf("failed to connect: %v", err)
	}
	defer pool.Close()

	if err := runMigrations(ctx, pool); err != nil {
		b.Fatalf("failed to run migrations: %v", err)
	}

	eventRepo := postgres.NewEventRepository(pool)

	b.ResetTimer()
	b.ReportAllocs()

	now := time.Now()
	for i := 0; i < b.N; i++ {
		event := &domain.Event{
			ID:          fmt.Sprintf("evt_db_%d", i),
			Type:        "benchmark.test",
			Source:      "benchmark",
			Data:        []byte(`{"index": 1}`),
			Status:      domain.EventStatusPending,
			Attempts:    0,
			MaxAttempts: 5,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := eventRepo.Create(ctx, event); err != nil {
			b.Fatalf("insert failed: %v", err)
		}
	}
}

// BenchmarkDatabaseInsertBatched measures PostgreSQL INSERT with batching enabled.
func BenchmarkDatabaseInsertBatched(b *testing.B) {
	ctx := context.Background()

	pgContainer, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("benchmark"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		b.Fatalf("failed to start postgres: %v", err)
	}
	defer func() { _ = pgContainer.Terminate(ctx) }()

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		b.Fatalf("failed to get connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		b.Fatalf("failed to connect: %v", err)
	}
	defer pool.Close()

	if err := runMigrations(ctx, pool); err != nil {
		b.Fatalf("failed to run migrations: %v", err)
	}

	eventRepo := postgres.NewEventRepository(pool).WithBatcher(postgres.DefaultBatcherConfig())
	defer func() { _ = eventRepo.Shutdown(ctx) }()

	b.ResetTimer()
	b.ReportAllocs()

	now := time.Now()
	for i := 0; i < b.N; i++ {
		event := &domain.Event{
			ID:          fmt.Sprintf("evt_db_batch_%d", i),
			Type:        "benchmark.test",
			Source:      "benchmark",
			Data:        []byte(`{"index": 1}`),
			Status:      domain.EventStatusPending,
			Attempts:    0,
			MaxAttempts: 5,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := eventRepo.Create(ctx, event); err != nil {
			b.Fatalf("insert failed: %v", err)
		}
	}
}

// ThroughputTest runs a sustained load test and reports events/second.
func TestThroughputReport(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping throughput test in short mode")
	}

	ctx := context.Background()

	pgContainer, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("benchmark"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("failed to start postgres: %v", err)
	}
	defer func() { _ = pgContainer.Terminate(ctx) }()

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("failed to get connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer pool.Close()

	if err := runMigrations(ctx, pool); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	eventRepo := postgres.NewEventRepository(pool)
	subRepo := postgres.NewSubscriptionRepository(pool)
	handler := api.NewHandler(eventRepo, subRepo, nil)

	// Test parameters
	duration := 10 * time.Second
	concurrency := 10

	var totalEvents int64
	var totalErrors int64

	start := time.Now()
	deadline := start.Add(duration)

	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			localCount := int64(0)
			for time.Now().Before(deadline) {
				i := atomic.AddInt64(&totalEvents, 1)
				eventPayload := map[string]interface{}{
					"id":     fmt.Sprintf("evt_tp_%d_%d", workerID, localCount),
					"type":   "benchmark.test",
					"source": "benchmark",
					"data":   map[string]interface{}{"index": i},
				}
				body, _ := json.Marshal(eventPayload)

				req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				handler.CreateEvent(rec, req)

				if rec.Code != http.StatusAccepted {
					atomic.AddInt64(&totalErrors, 1)
				}
				localCount++
			}
		}(w)
	}
	wg.Wait()

	elapsed := time.Since(start)
	eventsPerSecond := float64(totalEvents) / elapsed.Seconds()

	t.Logf("\n=== Throughput Report ===")
	t.Logf("Duration:          %v", elapsed.Round(time.Millisecond))
	t.Logf("Concurrency:       %d workers", concurrency)
	t.Logf("Total Events:      %d", totalEvents)
	t.Logf("Errors:            %d", totalErrors)
	t.Logf("Throughput:        %.0f events/second", eventsPerSecond)
	t.Logf("Avg Latency:       %.2f ms/event", float64(elapsed.Milliseconds())/float64(totalEvents)*float64(concurrency))
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
