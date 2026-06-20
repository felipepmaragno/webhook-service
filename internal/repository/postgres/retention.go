package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type RetentionRepository struct {
	pool *pgxpool.Pool
}

func NewRetentionRepository(pool *pgxpool.Pool) *RetentionRepository {
	return &RetentionRepository{pool: pool}
}

func (r *RetentionRepository) RedactAttemptBodies(ctx context.Context, before time.Time, limit int) (int64, error) {
	const query = `
		WITH candidates AS (
			SELECT id
			FROM delivery_attempts
			WHERE response_body IS NOT NULL AND created_at < $1
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE delivery_attempts AS attempt
		SET response_body = NULL
		FROM candidates
		WHERE attempt.id = candidates.id
	`
	tag, err := r.pool.Exec(ctx, query, before, limit)
	if err != nil {
		return 0, fmt.Errorf("redact attempt bodies: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *RetentionRepository) DeleteTerminalEvents(ctx context.Context, before time.Time, limit int) (int64, error) {
	const query = `
		WITH candidates AS (
			SELECT event.id
			FROM events AS event
			WHERE event.updated_at < $1
			  AND event.status IN ('delivered', 'failed')
			  AND NOT EXISTS (
			      SELECT 1
			      FROM deliveries AS delivery
			      WHERE delivery.event_id = event.id
			        AND delivery.status NOT IN ('delivered', 'failed')
			  )
			ORDER BY event.updated_at, event.id
			FOR UPDATE OF event SKIP LOCKED
			LIMIT $2
		)
		DELETE FROM events AS event
		USING candidates
		WHERE event.id = candidates.id
	`
	tag, err := r.pool.Exec(ctx, query, before, limit)
	if err != nil {
		return 0, fmt.Errorf("delete terminal events: %w", err)
	}
	return tag.RowsAffected(), nil
}
