package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felipemaragno/dispatch/internal/domain"
)

var ErrNotFound = errors.New("not found")

type EventRepository struct {
	pool *pgxpool.Pool
}

func NewEventRepository(pool *pgxpool.Pool) *EventRepository {
	return &EventRepository{pool: pool}
}

func (r *EventRepository) Create(ctx context.Context, event *domain.Event) error {
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
			WHERE status IN ('pending', 'retrying')
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
