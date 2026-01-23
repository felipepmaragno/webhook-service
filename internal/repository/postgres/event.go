package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felipemaragno/dispatch/internal/domain"
)

var ErrNotFound = errors.New("not found")

type EventRepository struct {
	pool    *pgxpool.Pool
	batcher *EventBatcher
}

func NewEventRepository(pool *pgxpool.Pool) *EventRepository {
	return &EventRepository{pool: pool}
}

// WithBatcher enables batch inserts for improved throughput.
// When enabled, Create() will batch events and flush them periodically.
func (r *EventRepository) WithBatcher(config BatcherConfig) *EventRepository {
	r.batcher = NewEventBatcher(r.pool, config)
	return r
}

// Shutdown gracefully shuts down the repository, flushing any pending batched events.
func (r *EventRepository) Shutdown(ctx context.Context) error {
	if r.batcher != nil {
		return r.batcher.Shutdown(ctx)
	}
	return nil
}

func (r *EventRepository) Create(ctx context.Context, event *domain.Event) error {
	// Use batcher if enabled
	if r.batcher != nil {
		return r.batcher.Add(ctx, event)
	}

	// Direct insert
	const query = `
		INSERT INTO events (id, type, source, data, status, attempts, max_attempts, next_attempt_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (id) DO NOTHING
	`

	_, err := r.pool.Exec(ctx, query,
		event.ID,
		event.Type,
		event.Source,
		event.Data,
		event.Status,
		event.Attempts,
		event.MaxAttempts,
		event.NextAttemptAt,
		event.CreatedAt,
		event.UpdatedAt,
	)
	return err
}

// CreateBatch inserts multiple events in a single query for improved throughput.
// PostgreSQL has a limit of 65535 parameters, so we chunk large batches.
func (r *EventRepository) CreateBatch(ctx context.Context, events []*domain.Event) error {
	if len(events) == 0 {
		return nil
	}

	// 12 parameters per event, max 65535 params → max ~5400 events per batch
	const maxEventsPerBatch = 5000

	for start := 0; start < len(events); start += maxEventsPerBatch {
		end := start + maxEventsPerBatch
		if end > len(events) {
			end = len(events)
		}
		if err := r.createBatchChunk(ctx, events[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (r *EventRepository) createBatchChunk(ctx context.Context, events []*domain.Event) error {
	if len(events) == 0 {
		return nil
	}

	// Build query with multiple value sets
	var queryBuilder strings.Builder
	queryBuilder.WriteString(`
		INSERT INTO events (id, type, source, data, status, attempts, max_attempts, next_attempt_at, last_error, created_at, updated_at, delivered_at)
		VALUES `)

	args := make([]interface{}, 0, len(events)*12)
	for i, e := range events {
		if i > 0 {
			queryBuilder.WriteString(", ")
		}
		base := i * 12
		queryBuilder.WriteString(fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11, base+12))

		args = append(args,
			e.ID,
			e.Type,
			e.Source,
			e.Data,
			e.Status,
			e.Attempts,
			e.MaxAttempts,
			e.NextAttemptAt,
			e.LastError,
			e.CreatedAt,
			e.UpdatedAt,
			e.DeliveredAt,
		)
	}

	queryBuilder.WriteString(" ON CONFLICT (id) DO NOTHING")

	_, err := r.pool.Exec(ctx, queryBuilder.String(), args...)
	return err
}

func (r *EventRepository) GetByID(ctx context.Context, id string) (*domain.Event, error) {
	const query = `
		SELECT id, type, source, data, status, attempts, max_attempts, 
		       next_attempt_at, last_error, created_at, updated_at, delivered_at
		FROM events
		WHERE id = $1
	`

	var event domain.Event
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&event.ID,
		&event.Type,
		&event.Source,
		&event.Data,
		&event.Status,
		&event.Attempts,
		&event.MaxAttempts,
		&event.NextAttemptAt,
		&event.LastError,
		&event.CreatedAt,
		&event.UpdatedAt,
		&event.DeliveredAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func (r *EventRepository) GetPendingEvents(ctx context.Context, limit int) ([]*domain.Event, error) {
	const query = `
		UPDATE events
		SET status = 'processing', updated_at = NOW()
		WHERE id IN (
			SELECT id FROM events
			WHERE status IN ('pending', 'retrying', 'throttled')
			AND (next_attempt_at IS NULL OR next_attempt_at <= NOW())
			ORDER BY next_attempt_at NULLS FIRST, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		RETURNING id, type, source, data, status, attempts, max_attempts,
		          next_attempt_at, last_error, created_at, updated_at, delivered_at
	`

	rows, err := r.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*domain.Event
	for rows.Next() {
		var event domain.Event
		err := rows.Scan(
			&event.ID,
			&event.Type,
			&event.Source,
			&event.Data,
			&event.Status,
			&event.Attempts,
			&event.MaxAttempts,
			&event.NextAttemptAt,
			&event.LastError,
			&event.CreatedAt,
			&event.UpdatedAt,
			&event.DeliveredAt,
		)
		if err != nil {
			return nil, err
		}
		events = append(events, &event)
	}

	return events, rows.Err()
}

func (r *EventRepository) UpdateStatus(ctx context.Context, event *domain.Event) error {
	const query = `
		UPDATE events
		SET status = $2, attempts = $3, next_attempt_at = $4, 
		    last_error = $5, updated_at = $6, delivered_at = $7
		WHERE id = $1
	`

	_, err := r.pool.Exec(ctx, query,
		event.ID,
		event.Status,
		event.Attempts,
		event.NextAttemptAt,
		event.LastError,
		event.UpdatedAt,
		event.DeliveredAt,
	)
	return err
}

func (r *EventRepository) RecordAttempt(ctx context.Context, attempt *domain.DeliveryAttempt) error {
	const query = `
		INSERT INTO delivery_attempts (event_id, attempt_number, status_code, response_body, error, duration_ms)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at
	`

	return r.pool.QueryRow(ctx, query,
		attempt.EventID,
		attempt.AttemptNumber,
		attempt.StatusCode,
		attempt.ResponseBody,
		attempt.Error,
		attempt.DurationMs,
	).Scan(&attempt.ID, &attempt.CreatedAt)
}

func (r *EventRepository) UpdateStatusBatch(ctx context.Context, events []*domain.Event) error {
	if len(events) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, event := range events {
		batch.Queue(`
			UPDATE events
			SET status = $2, attempts = $3, next_attempt_at = $4, 
			    last_error = $5, updated_at = $6, delivered_at = $7
			WHERE id = $1
		`, event.ID, event.Status, event.Attempts, event.NextAttemptAt,
			event.LastError, event.UpdatedAt, event.DeliveredAt)
	}

	br := r.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()

	for range events {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

func (r *EventRepository) RecordAttemptBatch(ctx context.Context, attempts []*domain.DeliveryAttempt) error {
	if len(attempts) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, attempt := range attempts {
		batch.Queue(`
			INSERT INTO delivery_attempts (event_id, attempt_number, status_code, response_body, error, duration_ms)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, attempt.EventID, attempt.AttemptNumber, attempt.StatusCode,
			attempt.ResponseBody, attempt.Error, attempt.DurationMs)
	}

	br := r.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()

	for range attempts {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

func (r *EventRepository) GetAttemptsByEventID(ctx context.Context, eventID string) ([]*domain.DeliveryAttempt, error) {
	const query = `
		SELECT id, event_id, attempt_number, status_code, response_body, error, duration_ms, created_at
		FROM delivery_attempts
		WHERE event_id = $1
		ORDER BY attempt_number
	`

	rows, err := r.pool.Query(ctx, query, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var attempts []*domain.DeliveryAttempt
	for rows.Next() {
		var attempt domain.DeliveryAttempt
		err := rows.Scan(
			&attempt.ID,
			&attempt.EventID,
			&attempt.AttemptNumber,
			&attempt.StatusCode,
			&attempt.ResponseBody,
			&attempt.Error,
			&attempt.DurationMs,
			&attempt.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, &attempt)
	}

	return attempts, rows.Err()
}
