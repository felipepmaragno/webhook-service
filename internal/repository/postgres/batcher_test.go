package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/felipemaragno/dispatch/internal/domain"
)

func setupTestDB(t *testing.T) (*pgxpool.Pool, func()) {
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
		t.Fatalf("failed to start postgres: %v", err)
	}

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		t.Fatalf("failed to get connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		t.Fatalf("failed to connect: %v", err)
	}

	// Create table
	_, err = pool.Exec(ctx, `
		CREATE TYPE event_status AS ENUM ('pending', 'processing', 'delivered', 'retrying', 'throttled', 'failed');
		CREATE TABLE events (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			source TEXT NOT NULL,
			data JSONB NOT NULL,
			status event_status NOT NULL DEFAULT 'pending',
			attempts INT NOT NULL DEFAULT 0,
			max_attempts INT NOT NULL DEFAULT 5,
			next_attempt_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		pool.Close()
		_ = pgContainer.Terminate(ctx)
		t.Fatalf("failed to create table: %v", err)
	}

	cleanup := func() {
		pool.Close()
		_ = pgContainer.Terminate(ctx)
	}

	return pool, cleanup
}

func TestBatcher_SingleEvent(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	config := BatcherConfig{MaxSize: 10, MaxWait: 50 * time.Millisecond}
	batcher := NewEventBatcher(pool, config)
	defer func() { _ = batcher.Shutdown(ctx) }()

	event := &domain.Event{
		ID:          "evt_single_1",
		Type:        "test.event",
		Source:      "test",
		Data:        []byte(`{"test": true}`),
		Status:      domain.EventStatusPending,
		MaxAttempts: 5,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	err := batcher.Add(ctx, event)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Verify in DB
	var count int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM events WHERE id = $1", event.ID).Scan(&count)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 event, got %d", count)
	}
}

func TestBatcher_BatchFlush(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	config := BatcherConfig{MaxSize: 5, MaxWait: 1 * time.Second}
	batcher := NewEventBatcher(pool, config)
	defer func() { _ = batcher.Shutdown(ctx) }()

	// Add exactly MaxSize events - should trigger immediate flush
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			event := &domain.Event{
				ID:          fmt.Sprintf("evt_batch_%d", idx),
				Type:        "test.event",
				Source:      "test",
				Data:        []byte(`{"test": true}`),
				Status:      domain.EventStatusPending,
				MaxAttempts: 5,
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			}
			if err := batcher.Add(ctx, event); err != nil {
				t.Errorf("Add failed for event %d: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()

	// Verify all in DB
	var count int
	err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM events").Scan(&count)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if count != 5 {
		t.Errorf("expected 5 events, got %d", count)
	}
}

func TestBatcher_HighConcurrency(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	config := BatcherConfig{MaxSize: 50, MaxWait: 5 * time.Millisecond}
	batcher := NewEventBatcher(pool, config)

	numEvents := 5000
	var wg sync.WaitGroup
	errors := make(chan error, numEvents)

	start := time.Now()

	for i := 0; i < numEvents; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			event := &domain.Event{
				ID:          fmt.Sprintf("evt_concurrent_%d", idx),
				Type:        "test.event",
				Source:      "test",
				Data:        []byte(`{"test": true}`),
				Status:      domain.EventStatusPending,
				MaxAttempts: 5,
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			}
			if err := batcher.Add(ctx, event); err != nil {
				errors <- fmt.Errorf("event %d: %w", idx, err)
			}
		}(i)
	}
	wg.Wait()
	close(errors)

	duration := time.Since(start)

	// Check for errors
	var errCount int
	for err := range errors {
		t.Errorf("error: %v", err)
		errCount++
	}

	if err := batcher.Shutdown(ctx); err != nil {
		t.Errorf("shutdown failed: %v", err)
	}

	// Verify all in DB
	var count int
	err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM events").Scan(&count)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	t.Logf("Inserted %d events in %v (%.0f events/s)", count, duration, float64(count)/duration.Seconds())

	if count != numEvents {
		t.Errorf("expected %d events, got %d (errors: %d)", numEvents, count, errCount)
	}
}
