package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/repository"
)

type EventRepository struct {
	pool *pgxpool.Pool
}

type deliveryQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func NewEventRepository(pool *pgxpool.Pool) *EventRepository {
	return &EventRepository{pool: pool}
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
		return nil, domain.ErrNotFound
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
				subscription_url, subscription_secret, max_delivery_rate, status, attempts, max_attempts, generation, next_attempt_at,
				last_error, processing_owner, processing_deadline, created_at,
				updated_at, delivered_at
			)
			VALUES (
				$1, $2, $3, $4, $5, $6,
				$7, $8, $9, $10, $11, $12, $13, $14,
				$15, $16, $17, $18, $19, $20
			)
			ON CONFLICT (event_id, subscription_id) DO NOTHING
		`, delivery.ID, delivery.EventID, delivery.SubscriptionID, delivery.EventType,
			delivery.Source, delivery.Data, delivery.SubscriptionURL, delivery.SubscriptionSecret,
			delivery.MaxDeliveryRate,
			delivery.Status, delivery.Attempts, delivery.MaxAttempts, delivery.Generation, delivery.NextAttemptAt,
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

func (r *EventRepository) ReplayFailedDelivery(ctx context.Context, id string, scheduledAt time.Time) (*domain.Delivery, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin replay transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	delivery, err := scanDelivery(tx.QueryRow(ctx, `
		SELECT id, event_id, subscription_id, event_type, source, data,
		       subscription_url, subscription_secret, max_delivery_rate, status, attempts, max_attempts, generation,
		       next_attempt_at, last_error, processing_owner, processing_deadline,
		       created_at, updated_at, delivered_at
		FROM deliveries
		WHERE id = $1
		FOR UPDATE
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock delivery for replay %s: %w", id, err)
	}
	if err := delivery.ScheduleReplay(scheduledAt); err != nil {
		return nil, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE deliveries
		SET generation = $2, status = $3, attempts = $4, next_attempt_at = $5,
		    last_error = $6, processing_owner = $7, processing_deadline = $8,
		    delivered_at = $9, updated_at = $10
		WHERE id = $1 AND status = 'failed'
	`, id, delivery.Generation, delivery.Status, delivery.Attempts, delivery.NextAttemptAt,
		delivery.LastError, delivery.ProcessingOwner, delivery.ProcessingDeadline,
		delivery.DeliveredAt, delivery.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("schedule replay %s: %w", id, err)
	}
	if tag.RowsAffected() != 1 {
		return nil, domain.ErrReplayNotEligible
	}

	if err := updateEventProjection(ctx, tx, delivery.EventID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit replay transaction: %w", err)
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
		          delivery.max_delivery_rate, delivery.status,
			          delivery.attempts, delivery.max_attempts, delivery.generation,
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
		          delivery.max_delivery_rate, delivery.status,
			          delivery.attempts, delivery.max_attempts, delivery.generation,
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
					event_id, delivery_id, subscription_id, attempt_number, generation,
					status_code, response_body, error, duration_ms
			)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			`, attempt.EventID, attempt.DeliveryID, attempt.SubscriptionID,
			attempt.AttemptNumber, attempt.Generation, attempt.StatusCode, attempt.ResponseBody,
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
		       subscription_url, subscription_secret, max_delivery_rate, status, attempts, max_attempts, generation, next_attempt_at,
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
		&delivery.MaxDeliveryRate,
		&delivery.Status,
		&delivery.Attempts,
		&delivery.MaxAttempts,
		&delivery.Generation,
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
		&delivery.MaxDeliveryRate,
		&delivery.Status,
		&delivery.Attempts,
		&delivery.MaxAttempts,
		&delivery.Generation,
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
	if delivery.Generation <= 0 {
		delivery.Generation = 1
	}
	if attempt.EventID == "" {
		attempt.EventID = delivery.EventID
	}
	if attempt.DeliveryID == "" {
		attempt.DeliveryID = delivery.ID
	}
	if attempt.SubscriptionID == "" {
		attempt.SubscriptionID = delivery.SubscriptionID
	}
	if attempt.Generation <= 0 {
		attempt.Generation = delivery.Generation
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
		return fmt.Errorf("event projection %s: %w", eventID, domain.ErrNotFound)
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

func (r *EventRepository) GetAttemptsByEventID(ctx context.Context, eventID string) ([]*domain.DeliveryAttempt, error) {
	const query = `
			SELECT id, event_id, delivery_id, subscription_id, attempt_number, generation, status_code, response_body, error, duration_ms, created_at
		FROM delivery_attempts
		WHERE event_id = $1
			ORDER BY generation, attempt_number, id
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
			&attempt.Generation,
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
