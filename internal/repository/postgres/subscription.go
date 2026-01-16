package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felipemaragno/dispatch/internal/domain"
)

type SubscriptionRepository struct {
	pool *pgxpool.Pool
}

func NewSubscriptionRepository(pool *pgxpool.Pool) *SubscriptionRepository {
	return &SubscriptionRepository{pool: pool}
}

func (r *SubscriptionRepository) Create(ctx context.Context, sub *domain.Subscription) error {
	const query = `
		INSERT INTO subscriptions (id, url, event_types, secret, rate_limit, created_at, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`

	_, err := r.pool.Exec(ctx, query,
		sub.ID,
		sub.URL,
		sub.EventTypes,
		sub.Secret,
		sub.RateLimit,
		sub.CreatedAt,
		sub.Active,
	)
	return err
}

func (r *SubscriptionRepository) GetByID(ctx context.Context, id string) (*domain.Subscription, error) {
	const query = `
		SELECT id, url, event_types, secret, rate_limit, created_at, active
		FROM subscriptions
		WHERE id = $1
	`

	var sub domain.Subscription
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&sub.ID,
		&sub.URL,
		&sub.EventTypes,
		&sub.Secret,
		&sub.RateLimit,
		&sub.CreatedAt,
		&sub.Active,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

func (r *SubscriptionRepository) GetActive(ctx context.Context) ([]*domain.Subscription, error) {
	const query = `
		SELECT id, url, event_types, secret, rate_limit, created_at, active
		FROM subscriptions
		WHERE active = TRUE
		ORDER BY created_at
	`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*domain.Subscription
	for rows.Next() {
		var sub domain.Subscription
		err := rows.Scan(
			&sub.ID,
			&sub.URL,
			&sub.EventTypes,
			&sub.Secret,
			&sub.RateLimit,
			&sub.CreatedAt,
			&sub.Active,
		)
		if err != nil {
			return nil, err
		}
		subs = append(subs, &sub)
	}

	return subs, rows.Err()
}

func (r *SubscriptionRepository) GetByEventType(ctx context.Context, eventType string) ([]*domain.Subscription, error) {
	const query = `
		SELECT id, url, event_types, secret, rate_limit, created_at, active
		FROM subscriptions
		WHERE active = TRUE
		ORDER BY created_at
	`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*domain.Subscription
	for rows.Next() {
		var sub domain.Subscription
		err := rows.Scan(
			&sub.ID,
			&sub.URL,
			&sub.EventTypes,
			&sub.Secret,
			&sub.RateLimit,
			&sub.CreatedAt,
			&sub.Active,
		)
		if err != nil {
			return nil, err
		}
		if sub.MatchesEventType(eventType) {
			subs = append(subs, &sub)
		}
	}

	return subs, rows.Err()
}

func (r *SubscriptionRepository) GetByEventTypes(ctx context.Context, eventTypes []string) (map[string][]*domain.Subscription, error) {
	if len(eventTypes) == 0 {
		return make(map[string][]*domain.Subscription), nil
	}

	const query = `
		SELECT id, url, event_types, secret, rate_limit, created_at, active
		FROM subscriptions
		WHERE active = TRUE
	`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Build result map
	result := make(map[string][]*domain.Subscription)
	for _, et := range eventTypes {
		result[et] = nil
	}

	var allSubs []*domain.Subscription
	for rows.Next() {
		var sub domain.Subscription
		err := rows.Scan(
			&sub.ID,
			&sub.URL,
			&sub.EventTypes,
			&sub.Secret,
			&sub.RateLimit,
			&sub.CreatedAt,
			&sub.Active,
		)
		if err != nil {
			return nil, err
		}
		allSubs = append(allSubs, &sub)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Match subscriptions to event types
	for _, sub := range allSubs {
		for _, et := range eventTypes {
			if sub.MatchesEventType(et) {
				result[et] = append(result[et], sub)
			}
		}
	}

	return result, nil
}

func (r *SubscriptionRepository) Delete(ctx context.Context, id string) error {
	const query = `
		UPDATE subscriptions
		SET active = FALSE
		WHERE id = $1
	`

	result, err := r.pool.Exec(ctx, query, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
