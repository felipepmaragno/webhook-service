package postgres

import (
	"context"
	"testing"

	"github.com/felipemaragno/dispatch/internal/domain"
)

func TestEventRepository_GetAttemptsByEventIDOrdersByGeneration(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewEventRepository(pool)
	subRepo := NewSubscriptionRepository(pool)
	event := makeEvent("evt-attempt-order")
	sub := makeSub("sub-attempt-order", []string{"order.created"})
	if err := subRepo.Create(ctx, sub); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	deliveries, err := repo.InitializeEventDeliveries(ctx, event, []*domain.Subscription{sub})
	if err != nil {
		t.Fatalf("initialize delivery: %v", err)
	}
	delivery := deliveries[0]

	for _, item := range []struct {
		generation int
		attempt    int
	}{
		{generation: 2, attempt: 1},
		{generation: 1, attempt: 2},
		{generation: 1, attempt: 1},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO delivery_attempts (
				event_id, delivery_id, subscription_id, attempt_number, generation, duration_ms
			) VALUES ($1, $2, $3, $4, $5, 10)
		`, event.ID, delivery.ID, sub.ID, item.attempt, item.generation); err != nil {
			t.Fatalf("insert attempt: %v", err)
		}
	}

	attempts, err := repo.GetAttemptsByEventID(ctx, event.ID)
	if err != nil {
		t.Fatalf("GetAttemptsByEventID: %v", err)
	}
	if len(attempts) != 3 {
		t.Fatalf("attempt count = %d, want 3", len(attempts))
	}
	want := [][2]int{{1, 1}, {1, 2}, {2, 1}}
	for i, attempt := range attempts {
		if attempt.Generation != want[i][0] || attempt.AttemptNumber != want[i][1] {
			t.Fatalf("attempt[%d] = generation %d attempt %d, want %v", i, attempt.Generation, attempt.AttemptNumber, want[i])
		}
	}
}
