package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/repository"
)

// ErrNotFound is kept for backward compatibility but wraps domain.ErrNotFound.
// New code should use domain.ErrNotFound directly.
var ErrNotFound = domain.ErrNotFound

type EventRepository struct {
	pool    *pgxpool.Pool
	batcher *EventBatcher
}

type deliveryQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
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
		fmt.Fprintf(&queryBuilder, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11, base+12)

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
		       next_attempt_at, last_error, created_at, updated_at, delivered_at,
		       processing_owner, processing_deadline
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
		&event.ProcessingOwner,
		&event.ProcessingDeadline,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func (r *EventRepository) InitializeEventDeliveries(ctx context.Context, event *domain.Event, subscriptions []*domain.Subscription) ([]*domain.Delivery, error) {
	if event == nil {
		return nil, errors.New("initialize deliveries: event is nil")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin delivery initialization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := time.Now()
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	if event.UpdatedAt.IsZero() {
		event.UpdatedAt = event.CreatedAt
	}
	if event.MaxAttempts == 0 {
		event.MaxAttempts = 5
	}
	eventStatus := event.Status
	deliveredAt := event.DeliveredAt
	if len(subscriptions) == 0 {
		eventStatus = domain.EventStatusDelivered
		if deliveredAt == nil {
			deliveredAt = &now
		}
	} else if eventStatus == "" {
		eventStatus = domain.EventStatusPending
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO events (id, type, source, data, status, attempts, max_attempts, next_attempt_at, last_error, created_at, updated_at, delivered_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (id) DO NOTHING
	`, event.ID, event.Type, event.Source, event.Data, eventStatus, event.Attempts,
		event.MaxAttempts, event.NextAttemptAt, event.LastError, event.CreatedAt,
		event.UpdatedAt, deliveredAt)
	if err != nil {
		return nil, fmt.Errorf("insert event for deliveries: %w", err)
	}

	for _, sub := range subscriptions {
		if sub == nil {
			continue
		}
		delivery := domain.NewDelivery(event, sub)
		_, err := tx.Exec(ctx, `
			INSERT INTO deliveries (
				id, event_id, subscription_id, event_type, source, data,
				subscription_url, subscription_secret, rate_limit, burst_size,
				concurrency_limit, status, attempts, max_attempts, next_attempt_at,
				last_error, processing_owner, processing_deadline, created_at,
				updated_at, delivered_at
			)
			VALUES (
				$1, $2, $3, $4, $5, $6,
				$7, $8, $9, $10,
				$11, $12, $13, $14, $15,
				$16, $17, $18, $19,
				$20, $21
			)
			ON CONFLICT (event_id, subscription_id) DO NOTHING
		`, delivery.ID, delivery.EventID, delivery.SubscriptionID, delivery.EventType,
			delivery.Source, delivery.Data, delivery.SubscriptionURL, delivery.SubscriptionSecret,
			delivery.RateLimit, delivery.BurstSize, delivery.ConcurrencyLimit,
			delivery.Status, delivery.Attempts, delivery.MaxAttempts, delivery.NextAttemptAt,
			delivery.LastError, delivery.ProcessingOwner, delivery.ProcessingDeadline,
			delivery.CreatedAt, delivery.UpdatedAt, delivery.DeliveredAt)
		if err != nil {
			return nil, fmt.Errorf("insert delivery %s: %w", delivery.ID, err)
		}
	}

	deliveries, err := r.getDeliveriesByEventID(ctx, tx, event.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit delivery initialization: %w", err)
	}
	return deliveries, nil
}

func (r *EventRepository) GetDeliveriesByEventID(ctx context.Context, eventID string) ([]*domain.Delivery, error) {
	return r.getDeliveriesByEventID(ctx, r.pool, eventID)
}

func (r *EventRepository) GetDeliveryByID(ctx context.Context, id string) (*domain.Delivery, error) {
	const query = `
		SELECT id, event_id, subscription_id, event_type, source, data,
		       subscription_url, subscription_secret, rate_limit, burst_size,
		       concurrency_limit, status, attempts, max_attempts, next_attempt_at,
		       last_error, processing_owner, processing_deadline, created_at,
		       updated_at, delivered_at
		FROM deliveries
		WHERE id = $1
	`

	row := r.pool.QueryRow(ctx, query, id)
	delivery, err := scanDelivery(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return delivery, nil
}

func (r *EventRepository) ClaimDeliveries(ctx context.Context, owner string, leaseDuration time.Duration, limit int) ([]repository.ClaimedDelivery, error) {
	const query = `
		WITH candidates AS (
			SELECT id, status = 'processing' AS reclaimed
			FROM deliveries
			WHERE status = 'pending'
			   OR (status IN ('retrying', 'throttled') AND next_attempt_at <= NOW())
			   OR (status = 'processing' AND processing_deadline <= NOW())
			ORDER BY COALESCE(next_attempt_at, processing_deadline, created_at), created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE deliveries AS delivery
		SET status = 'processing',
		    processing_owner = $2,
		    processing_deadline = NOW() + $3::interval,
		    updated_at = NOW()
		FROM candidates
		WHERE delivery.id = candidates.id
		RETURNING delivery.id, delivery.event_id, delivery.subscription_id,
		          delivery.event_type, delivery.source, delivery.data,
		          delivery.subscription_url, delivery.subscription_secret,
		          delivery.rate_limit, delivery.burst_size,
		          delivery.concurrency_limit, delivery.status,
		          delivery.attempts, delivery.max_attempts,
		          delivery.next_attempt_at, delivery.last_error,
		          delivery.processing_owner, delivery.processing_deadline,
		          delivery.created_at, delivery.updated_at,
		          delivery.delivered_at, candidates.reclaimed
	`

	rows, err := r.pool.Query(ctx, query, limit, owner, postgresInterval(leaseDuration))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var claimed []repository.ClaimedDelivery
	for rows.Next() {
		delivery, reclaimed, err := scanClaimedDelivery(rows)
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, repository.ClaimedDelivery{Delivery: delivery, Reclaimed: reclaimed})
	}
	return claimed, rows.Err()
}

func (r *EventRepository) ClaimEventDeliveries(ctx context.Context, eventIDs []string, owner string, leaseDuration time.Duration, limit int) ([]repository.ClaimedDelivery, error) {
	if len(eventIDs) == 0 || limit <= 0 {
		return nil, nil
	}

	const query = `
		WITH candidates AS (
			SELECT id, status = 'processing' AS reclaimed
			FROM deliveries
			WHERE event_id = ANY($4)
			  AND (
			       status = 'pending'
			    OR (status IN ('retrying', 'throttled') AND next_attempt_at <= NOW())
			    OR (status = 'processing' AND processing_deadline <= NOW())
			  )
			ORDER BY event_id, COALESCE(next_attempt_at, processing_deadline, created_at), created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE deliveries AS delivery
		SET status = 'processing',
		    processing_owner = $2,
		    processing_deadline = NOW() + $3::interval,
		    updated_at = NOW()
		FROM candidates
		WHERE delivery.id = candidates.id
		RETURNING delivery.id, delivery.event_id, delivery.subscription_id,
		          delivery.event_type, delivery.source, delivery.data,
		          delivery.subscription_url, delivery.subscription_secret,
		          delivery.rate_limit, delivery.burst_size,
		          delivery.concurrency_limit, delivery.status,
		          delivery.attempts, delivery.max_attempts,
		          delivery.next_attempt_at, delivery.last_error,
		          delivery.processing_owner, delivery.processing_deadline,
		          delivery.created_at, delivery.updated_at,
		          delivery.delivered_at, candidates.reclaimed
	`

	rows, err := r.pool.Query(ctx, query, limit, owner, postgresInterval(leaseDuration), eventIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var claimed []repository.ClaimedDelivery
	for rows.Next() {
		delivery, reclaimed, err := scanClaimedDelivery(rows)
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, repository.ClaimedDelivery{Delivery: delivery, Reclaimed: reclaimed})
	}
	return claimed, rows.Err()
}

func (r *EventRepository) PersistDeliveryOutcome(ctx context.Context, delivery *domain.Delivery, attempts []*domain.DeliveryAttempt) error {
	if delivery == nil {
		return errors.New("persist delivery outcome: delivery is nil")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delivery outcome transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE deliveries
		SET status = $2, attempts = $3, next_attempt_at = $4, last_error = $5,
		    processing_owner = $6, processing_deadline = $7, updated_at = $8,
		    delivered_at = $9
		WHERE id = $1
	`, delivery.ID, delivery.Status, delivery.Attempts, delivery.NextAttemptAt,
		delivery.LastError, delivery.ProcessingOwner, delivery.ProcessingDeadline,
		delivery.UpdatedAt, delivery.DeliveredAt)
	if err != nil {
		return fmt.Errorf("update delivery %s: %w", delivery.ID, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("delivery %s: %w", delivery.ID, ErrNotFound)
	}

	for _, attempt := range attempts {
		normalizeDeliveryAttempt(delivery, attempt)
		_, err := tx.Exec(ctx, `
			INSERT INTO delivery_attempts (
				event_id, delivery_id, subscription_id, attempt_number,
				status_code, response_body, error, duration_ms
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, attempt.EventID, attempt.DeliveryID, attempt.SubscriptionID,
			attempt.AttemptNumber, attempt.StatusCode, attempt.ResponseBody,
			attempt.Error, attempt.DurationMs)
		if err != nil {
			return fmt.Errorf("insert delivery attempt %s: %w", delivery.ID, err)
		}
	}

	if err := updateEventProjection(ctx, tx, delivery.EventID); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delivery outcome transaction: %w", err)
	}
	return nil
}

func (r *EventRepository) PersistClaimedDeliveryOutcome(ctx context.Context, delivery *domain.Delivery, attempts []*domain.DeliveryAttempt) error {
	if delivery == nil || delivery.ProcessingOwner == nil || delivery.ProcessingDeadline == nil {
		return fmt.Errorf("persist claimed delivery outcome: %w", repository.ErrClaimLost)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin claimed delivery outcome transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE deliveries
		SET status = $2, attempts = $3, next_attempt_at = $4, last_error = $5,
		    processing_owner = NULL, processing_deadline = NULL, updated_at = $6,
		    delivered_at = $7
		WHERE id = $1 AND status = 'processing'
		  AND processing_owner = $8 AND processing_deadline = $9
	`, delivery.ID, delivery.Status, delivery.Attempts, delivery.NextAttemptAt,
		delivery.LastError, delivery.UpdatedAt, delivery.DeliveredAt,
		*delivery.ProcessingOwner, *delivery.ProcessingDeadline)
	if err != nil {
		return fmt.Errorf("update claimed delivery %s: %w", delivery.ID, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("delivery %s: %w", delivery.ID, repository.ErrClaimLost)
	}

	for _, attempt := range attempts {
		normalizeDeliveryAttempt(delivery, attempt)
		_, err := tx.Exec(ctx, `
			INSERT INTO delivery_attempts (
				event_id, delivery_id, subscription_id, attempt_number,
				status_code, response_body, error, duration_ms
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, attempt.EventID, attempt.DeliveryID, attempt.SubscriptionID,
			attempt.AttemptNumber, attempt.StatusCode, attempt.ResponseBody,
			attempt.Error, attempt.DurationMs)
		if err != nil {
			return fmt.Errorf("insert claimed delivery attempt %s: %w", delivery.ID, err)
		}
	}

	if err := updateEventProjection(ctx, tx, delivery.EventID); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit claimed delivery outcome transaction: %w", err)
	}
	return nil
}

func (r *EventRepository) getDeliveriesByEventID(ctx context.Context, q deliveryQuerier, eventID string) ([]*domain.Delivery, error) {
	const query = `
		SELECT id, event_id, subscription_id, event_type, source, data,
		       subscription_url, subscription_secret, rate_limit, burst_size,
		       concurrency_limit, status, attempts, max_attempts, next_attempt_at,
		       last_error, processing_owner, processing_deadline, created_at,
		       updated_at, delivered_at
		FROM deliveries
		WHERE event_id = $1
		ORDER BY created_at, id
	`

	rows, err := q.Query(ctx, query, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deliveries []*domain.Delivery
	for rows.Next() {
		delivery, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

type deliveryScanner interface {
	Scan(dest ...any) error
}

func scanDelivery(row deliveryScanner) (*domain.Delivery, error) {
	var delivery domain.Delivery
	err := row.Scan(
		&delivery.ID,
		&delivery.EventID,
		&delivery.SubscriptionID,
		&delivery.EventType,
		&delivery.Source,
		&delivery.Data,
		&delivery.SubscriptionURL,
		&delivery.SubscriptionSecret,
		&delivery.RateLimit,
		&delivery.BurstSize,
		&delivery.ConcurrencyLimit,
		&delivery.Status,
		&delivery.Attempts,
		&delivery.MaxAttempts,
		&delivery.NextAttemptAt,
		&delivery.LastError,
		&delivery.ProcessingOwner,
		&delivery.ProcessingDeadline,
		&delivery.CreatedAt,
		&delivery.UpdatedAt,
		&delivery.DeliveredAt,
	)
	if err != nil {
		return nil, err
	}
	return &delivery, nil
}

func scanClaimedDelivery(row deliveryScanner) (*domain.Delivery, bool, error) {
	var delivery domain.Delivery
	var reclaimed bool
	err := row.Scan(
		&delivery.ID,
		&delivery.EventID,
		&delivery.SubscriptionID,
		&delivery.EventType,
		&delivery.Source,
		&delivery.Data,
		&delivery.SubscriptionURL,
		&delivery.SubscriptionSecret,
		&delivery.RateLimit,
		&delivery.BurstSize,
		&delivery.ConcurrencyLimit,
		&delivery.Status,
		&delivery.Attempts,
		&delivery.MaxAttempts,
		&delivery.NextAttemptAt,
		&delivery.LastError,
		&delivery.ProcessingOwner,
		&delivery.ProcessingDeadline,
		&delivery.CreatedAt,
		&delivery.UpdatedAt,
		&delivery.DeliveredAt,
		&reclaimed,
	)
	if err != nil {
		return nil, false, err
	}
	return &delivery, reclaimed, nil
}

func normalizeDeliveryAttempt(delivery *domain.Delivery, attempt *domain.DeliveryAttempt) {
	if attempt.EventID == "" {
		attempt.EventID = delivery.EventID
	}
	if attempt.DeliveryID == nil {
		deliveryID := delivery.ID
		attempt.DeliveryID = &deliveryID
	}
	if attempt.SubscriptionID == nil {
		subscriptionID := delivery.SubscriptionID
		attempt.SubscriptionID = &subscriptionID
	}
}

func updateEventProjection(ctx context.Context, tx pgx.Tx, eventID string) error {
	repo := EventRepository{}
	deliveries, err := repo.getDeliveriesByEventID(ctx, tx, eventID)
	if err != nil {
		return fmt.Errorf("load deliveries for event projection %s: %w", eventID, err)
	}
	projection := domain.ProjectEventFromDeliveries(deliveries)
	tag, err := tx.Exec(ctx, `
		UPDATE events
		SET status = $2, attempts = $3, next_attempt_at = $4,
		    last_error = $5, updated_at = NOW(), delivered_at = $6
		WHERE id = $1
	`, eventID, projection.Status, projection.Attempts, projection.NextAttemptAt,
		projection.LastError, projection.DeliveredAt)
	if err != nil {
		return fmt.Errorf("update event projection %s: %w", eventID, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("event projection %s: %w", eventID, ErrNotFound)
	}
	return nil
}

func postgresInterval(d time.Duration) string {
	microseconds := d.Microseconds()
	if d > 0 && microseconds == 0 {
		microseconds = 1
	}
	return fmt.Sprintf("%d microseconds", microseconds)
}

func (r *EventRepository) ClaimRetryEvents(ctx context.Context, owner string, leaseDuration time.Duration, limit int) ([]repository.ClaimedEvent, error) {
	const query = `
		WITH candidates AS (
			SELECT id, status = 'processing' AS reclaimed
			FROM events
			WHERE (status IN ('retrying', 'throttled') AND next_attempt_at <= NOW())
			   OR (status = 'processing' AND processing_deadline <= NOW())
			ORDER BY COALESCE(next_attempt_at, processing_deadline), created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE events AS event
		SET status = 'processing',
		    processing_owner = $2,
		    processing_deadline = NOW() + $3::interval,
		    updated_at = NOW()
		FROM candidates
		WHERE event.id = candidates.id
		RETURNING event.id, event.type, event.source, event.data, event.status,
		          event.attempts, event.max_attempts, event.next_attempt_at,
		          event.last_error, event.created_at, event.updated_at,
		          event.delivered_at, event.processing_owner,
		          event.processing_deadline, candidates.reclaimed
	`

	rows, err := r.pool.Query(ctx, query, limit, owner, postgresInterval(leaseDuration))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var claimed []repository.ClaimedEvent
	for rows.Next() {
		var event domain.Event
		var reclaimed bool
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
			&event.ProcessingOwner,
			&event.ProcessingDeadline,
			&reclaimed,
		)
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, repository.ClaimedEvent{Event: &event, Reclaimed: reclaimed})
	}

	return claimed, rows.Err()
}

const retryBacklogStatsQuery = `
		SELECT
			COUNT(*) FILTER (
				WHERE status IN ('retrying', 'throttled') AND next_attempt_at <= NOW()
			),
			MIN(CASE
				WHEN status IN ('retrying', 'throttled') AND next_attempt_at <= NOW() THEN next_attempt_at
				WHEN status = 'processing' AND processing_deadline <= NOW() THEN processing_deadline
			END),
			COUNT(*) FILTER (
				WHERE status = 'processing' AND processing_deadline <= NOW()
			),
			COUNT(*) FILTER (
				WHERE status = 'processing' AND processing_deadline > NOW()
			)
		FROM deliveries
		WHERE status IN ('retrying', 'throttled', 'processing')
	`

func (r *EventRepository) GetRetryBacklogStats(ctx context.Context) (repository.RetryBacklogStats, error) {

	var stats repository.RetryBacklogStats
	if err := r.pool.QueryRow(ctx, retryBacklogStatsQuery).Scan(
		&stats.DueCount,
		&stats.OldestDueAt,
		&stats.ExpiredProcessingCount,
		&stats.LeasedCount,
	); err != nil {
		return repository.RetryBacklogStats{}, fmt.Errorf("get retry backlog stats: %w", err)
	}
	return stats, nil
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

// PersistNewOutcomes atomically creates Kafka-originated event records and
// their delivery attempts. Duplicate event IDs keep the existing event row,
// while attempts from HTTP calls that actually occurred remain auditable.
func (r *EventRepository) PersistNewOutcomes(ctx context.Context, outcomes []repository.EventOutcome) error {
	return r.persistNewOutcomes(ctx, outcomes)
}

func (r *EventRepository) persistNewOutcomes(ctx context.Context, outcomes []repository.EventOutcome) error {
	if len(outcomes) == 0 {
		return nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin outcome transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	batch := &pgx.Batch{}
	commands := 0
	for _, outcome := range outcomes {
		if outcome.Event == nil {
			return errors.New("persist outcome: event is nil")
		}

		batch.Queue(`
			INSERT INTO events (id, type, source, data, status, attempts, max_attempts, next_attempt_at, last_error, created_at, updated_at, delivered_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (id) DO NOTHING
		`, outcome.Event.ID, outcome.Event.Type, outcome.Event.Source, outcome.Event.Data,
			outcome.Event.Status, outcome.Event.Attempts, outcome.Event.MaxAttempts,
			outcome.Event.NextAttemptAt, outcome.Event.LastError, outcome.Event.CreatedAt,
			outcome.Event.UpdatedAt, outcome.Event.DeliveredAt)
		commands++

		for _, attempt := range outcome.Attempts {
			batch.Queue(`
				INSERT INTO delivery_attempts (event_id, attempt_number, status_code, response_body, error, duration_ms)
				VALUES ($1, $2, $3, $4, $5, $6)
			`, attempt.EventID, attempt.AttemptNumber, attempt.StatusCode,
				attempt.ResponseBody, attempt.Error, attempt.DurationMs)
			commands++
		}
	}

	results := tx.SendBatch(ctx, batch)
	for range commands {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return fmt.Errorf("persist outcome batch: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("close outcome batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit outcome transaction: %w", err)
	}
	return nil
}

// PersistClaimedOutcomes atomically persists retry outcomes only while each event still
// owns the exact lease that produced the outcome. The deadline comparison fences stale
// work even if a later claim uses the same worker instance ID.
func (r *EventRepository) PersistClaimedOutcomes(ctx context.Context, outcomes []repository.EventOutcome) error {
	if len(outcomes) == 0 {
		return nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin claimed outcome transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, outcome := range outcomes {
		if outcome.Event == nil || outcome.Event.ProcessingOwner == nil || outcome.Event.ProcessingDeadline == nil {
			return fmt.Errorf("persist claimed outcome: %w", repository.ErrClaimLost)
		}

		tag, err := tx.Exec(ctx, `
			UPDATE events
			SET status = $2, attempts = $3, next_attempt_at = $4,
			    last_error = $5, updated_at = $6, delivered_at = $7,
			    processing_owner = NULL, processing_deadline = NULL
			WHERE id = $1 AND status = 'processing'
			  AND processing_owner = $8 AND processing_deadline = $9
		`, outcome.Event.ID, outcome.Event.Status, outcome.Event.Attempts,
			outcome.Event.NextAttemptAt, outcome.Event.LastError,
			outcome.Event.UpdatedAt, outcome.Event.DeliveredAt,
			*outcome.Event.ProcessingOwner, *outcome.Event.ProcessingDeadline)
		if err != nil {
			return fmt.Errorf("update claimed event %s: %w", outcome.Event.ID, err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("event %s: %w", outcome.Event.ID, repository.ErrClaimLost)
		}

		for _, attempt := range outcome.Attempts {
			_, err := tx.Exec(ctx, `
				INSERT INTO delivery_attempts (event_id, attempt_number, status_code, response_body, error, duration_ms)
				VALUES ($1, $2, $3, $4, $5, $6)
			`, attempt.EventID, attempt.AttemptNumber, attempt.StatusCode,
				attempt.ResponseBody, attempt.Error, attempt.DurationMs)
			if err != nil {
				return fmt.Errorf("insert claimed event attempt %s: %w", outcome.Event.ID, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit claimed outcome transaction: %w", err)
	}
	return nil
}

func (r *EventRepository) GetAttemptsByEventID(ctx context.Context, eventID string) ([]*domain.DeliveryAttempt, error) {
	const query = `
		SELECT id, event_id, delivery_id, subscription_id, attempt_number, status_code, response_body, error, duration_ms, created_at
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
			&attempt.DeliveryID,
			&attempt.SubscriptionID,
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
