package postgres

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felipemaragno/dispatch/internal/domain"
)

// BatcherConfig configures the event batcher behavior.
type BatcherConfig struct {
	// MaxSize is the maximum number of events to batch before flushing.
	MaxSize int
	// MaxWait is the maximum time to wait before flushing a partial batch.
	MaxWait time.Duration
}

// DefaultBatcherConfig returns sensible defaults for batching.
func DefaultBatcherConfig() BatcherConfig {
	return BatcherConfig{
		MaxSize: 50,
		MaxWait: 5 * time.Millisecond,
	}
}

// pendingEvent holds an event and its completion channel.
type pendingEvent struct {
	event *domain.Event
	done  chan error
}

// EventBatcher batches event inserts for improved throughput.
// It collects events and flushes them in batches, either when the batch
// is full or after a timeout, whichever comes first.
// Each caller blocks until their event is persisted.
type EventBatcher struct {
	pool   *pgxpool.Pool
	config BatcherConfig

	mu      sync.Mutex
	pending []pendingEvent
	timer   *time.Timer

	shutdown chan struct{}
	done     chan struct{}
}

// NewEventBatcher creates a new batcher with the given configuration.
func NewEventBatcher(pool *pgxpool.Pool, config BatcherConfig) *EventBatcher {
	b := &EventBatcher{
		pool:     pool,
		config:   config,
		pending:  make([]pendingEvent, 0, config.MaxSize),
		shutdown: make(chan struct{}),
		done:     make(chan struct{}),
	}
	go b.run()
	return b
}

// Add adds an event to the batch and blocks until it's persisted.
// Returns nil on success, or an error if the insert failed.
func (b *EventBatcher) Add(ctx context.Context, event *domain.Event) error {
	done := make(chan error, 1)

	b.mu.Lock()
	b.pending = append(b.pending, pendingEvent{event: event, done: done})
	shouldFlush := len(b.pending) >= b.config.MaxSize

	// Start timer on first event in batch
	if len(b.pending) == 1 && b.timer == nil {
		b.timer = time.AfterFunc(b.config.MaxWait, func() {
			b.mu.Lock()
			b.flushLocked()
			b.mu.Unlock()
		})
	}

	if shouldFlush {
		b.flushLocked()
	}
	b.mu.Unlock()

	// Wait for completion or context cancellation
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Shutdown gracefully shuts down the batcher, flushing any pending events.
func (b *EventBatcher) Shutdown(ctx context.Context) error {
	close(b.shutdown)

	// Wait for run loop to finish
	select {
	case <-b.done:
	case <-ctx.Done():
		return ctx.Err()
	}

	// Final flush
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) > 0 {
		b.flushLocked()
	}
	return nil
}

// run is the background goroutine that handles timer-based flushes.
func (b *EventBatcher) run() {
	defer close(b.done)
	<-b.shutdown
}

// flushLocked flushes all pending events. Must be called with mu held.
func (b *EventBatcher) flushLocked() {
	if len(b.pending) == 0 {
		return
	}

	// Stop timer if running
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}

	// Take ownership of pending slice
	toFlush := b.pending
	b.pending = make([]pendingEvent, 0, b.config.MaxSize)

	// Execute batch insert in background to release lock quickly
	go b.executeBatch(toFlush)
}

// executeBatch performs the actual batch INSERT.
func (b *EventBatcher) executeBatch(events []pendingEvent) {
	ctx := context.Background()
	err := b.batchInsert(ctx, events)

	// Notify all waiters
	for _, pe := range events {
		pe.done <- err
		close(pe.done)
	}
}

// batchInsert performs a single INSERT with multiple VALUES.
func (b *EventBatcher) batchInsert(ctx context.Context, events []pendingEvent) error {
	if len(events) == 0 {
		return nil
	}

	// Build query with multiple value sets
	// INSERT INTO events (...) VALUES ($1, $2, ...), ($11, $12, ...), ...
	var queryBuilder strings.Builder
	queryBuilder.WriteString(`
		INSERT INTO events (id, type, source, data, status, attempts, max_attempts, next_attempt_at, created_at, updated_at)
		VALUES `)

	args := make([]interface{}, 0, len(events)*10)
	for i, pe := range events {
		if i > 0 {
			queryBuilder.WriteString(", ")
		}
		base := i * 10
		queryBuilder.WriteString(fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10))

		e := pe.event
		args = append(args,
			e.ID,
			e.Type,
			e.Source,
			e.Data,
			e.Status,
			e.Attempts,
			e.MaxAttempts,
			e.NextAttemptAt,
			e.CreatedAt,
			e.UpdatedAt,
		)
	}

	queryBuilder.WriteString(" ON CONFLICT (id) DO NOTHING")

	_, err := b.pool.Exec(ctx, queryBuilder.String(), args...)
	return err
}
