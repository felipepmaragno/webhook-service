package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/repository"
)

func makeEvent(id string) *domain.Event {
	return &domain.Event{
		ID:          id,
		Type:        "order.created",
		Source:      "billing",
		Data:        json.RawMessage(`{"amount":99}`),
		Status:      domain.EventStatusPending,
		Attempts:    0,
		MaxAttempts: 5,
		CreatedAt:   time.Now().UTC().Truncate(time.Millisecond),
		UpdatedAt:   time.Now().UTC().Truncate(time.Millisecond),
	}
}

func TestEventRepository_Create(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)

	t.Run("happy path", func(t *testing.T) {
		evt := makeEvent("evt-create-1")
		if err := repo.Create(ctx, evt); err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		got, err := repo.GetByID(ctx, evt.ID)
		if err != nil {
			t.Fatalf("GetByID failed: %v", err)
		}
		if got.ID != evt.ID || got.Type != evt.Type || got.Source != evt.Source {
			t.Errorf("got %+v, want id=%s type=%s source=%s", got, evt.ID, evt.Type, evt.Source)
		}
	})

	t.Run("duplicate id is silently ignored (ON CONFLICT DO NOTHING)", func(t *testing.T) {
		evt := makeEvent("evt-create-dup")
		if err := repo.Create(ctx, evt); err != nil {
			t.Fatalf("first Create failed: %v", err)
		}
		evt.Type = "changed.type"
		if err := repo.Create(ctx, evt); err != nil {
			t.Fatalf("second Create (duplicate) failed: %v", err)
		}
		// Original must be unchanged
		got, _ := repo.GetByID(ctx, evt.ID)
		if got.Type != "order.created" {
			t.Errorf("expected original type, got %s", got.Type)
		}
	})
}

func TestEventRepository_CreateBatch(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)

	t.Run("empty batch is no-op", func(t *testing.T) {
		if err := repo.CreateBatch(ctx, nil); err != nil {
			t.Fatalf("CreateBatch(nil) failed: %v", err)
		}
		if err := repo.CreateBatch(ctx, []*domain.Event{}); err != nil {
			t.Fatalf("CreateBatch([]) failed: %v", err)
		}
	})

	t.Run("multiple events", func(t *testing.T) {
		events := []*domain.Event{
			makeEvent("evt-batch-1"),
			makeEvent("evt-batch-2"),
			makeEvent("evt-batch-3"),
		}
		if err := repo.CreateBatch(ctx, events); err != nil {
			t.Fatalf("CreateBatch failed: %v", err)
		}
		for _, e := range events {
			if _, err := repo.GetByID(ctx, e.ID); err != nil {
				t.Errorf("event %s not found after CreateBatch: %v", e.ID, err)
			}
		}
	})
}

func TestEventRepository_GetByID(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)

	t.Run("not found returns ErrNotFound", func(t *testing.T) {
		_, err := repo.GetByID(ctx, "nonexistent")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("found returns correct event", func(t *testing.T) {
		evt := makeEvent("evt-getbyid-1")
		_ = repo.Create(ctx, evt)

		got, err := repo.GetByID(ctx, evt.ID)
		if err != nil {
			t.Fatalf("GetByID failed: %v", err)
		}
		if got.Status != domain.EventStatusPending {
			t.Errorf("expected status pending, got %s", got.Status)
		}
		// PostgreSQL normalizes JSON formatting, so compare parsed content not raw bytes
		var gotData, wantData map[string]interface{}
		if err := json.Unmarshal(got.Data, &gotData); err != nil {
			t.Fatalf("failed to parse returned data: %v", err)
		}
		if err := json.Unmarshal(evt.Data, &wantData); err != nil {
			t.Fatalf("failed to parse original data: %v", err)
		}
		if gotData["amount"] != wantData["amount"] {
			t.Errorf("expected data %v, got %v", wantData, gotData)
		}
	})
}

func TestEventRepository_GetPendingEvents(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)

	t.Run("returns pending events and marks them processing", func(t *testing.T) {
		_ = repo.Create(ctx, makeEvent("evt-pending-1"))
		_ = repo.Create(ctx, makeEvent("evt-pending-2"))

		events, err := repo.GetPendingEvents(ctx, 10)
		if err != nil {
			t.Fatalf("GetPendingEvents failed: %v", err)
		}
		if len(events) < 2 {
			t.Fatalf("expected at least 2 events, got %d", len(events))
		}
		// Status must be 'processing' after the UPDATE...RETURNING
		for _, e := range events {
			if e.Status != domain.EventStatusProcessing {
				t.Errorf("expected processing, got %s for event %s", e.Status, e.ID)
			}
		}
	})

	t.Run("respects limit", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			evt := makeEvent("evt-limit-pending-" + string(rune('a'+i)))
			evt.Status = domain.EventStatusPending
			_ = repo.Create(ctx, evt)
		}
		events, err := repo.GetPendingEvents(ctx, 2)
		if err != nil {
			t.Fatalf("GetPendingEvents failed: %v", err)
		}
		if len(events) > 2 {
			t.Errorf("expected at most 2 events, got %d", len(events))
		}
	})

	t.Run("skips events with future next_attempt_at", func(t *testing.T) {
		future := time.Now().Add(1 * time.Hour)
		evt := makeEvent("evt-future-retry")
		evt.Status = domain.EventStatusRetrying
		evt.NextAttemptAt = &future
		_ = repo.Create(ctx, evt)

		events, err := repo.GetPendingEvents(ctx, 100)
		if err != nil {
			t.Fatalf("GetPendingEvents failed: %v", err)
		}
		for _, e := range events {
			if e.ID == evt.ID {
				t.Error("event with future next_attempt_at should not be returned")
			}
		}
	})
}

func TestEventRepository_UpdateStatus(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)

	t.Run("marks event as delivered", func(t *testing.T) {
		evt := makeEvent("evt-update-delivered")
		_ = repo.Create(ctx, evt)

		deliveredAt := time.Now().UTC().Truncate(time.Millisecond)
		evt.MarkAsDelivered(deliveredAt)
		if err := repo.UpdateStatus(ctx, evt); err != nil {
			t.Fatalf("UpdateStatus failed: %v", err)
		}

		got, _ := repo.GetByID(ctx, evt.ID)
		if got.Status != domain.EventStatusDelivered {
			t.Errorf("expected delivered, got %s", got.Status)
		}
		if got.DeliveredAt == nil {
			t.Error("expected DeliveredAt to be set")
		}
	})

	t.Run("marks event as retrying with last error", func(t *testing.T) {
		evt := makeEvent("evt-update-retry")
		_ = repo.Create(ctx, evt)

		next := time.Now().Add(30 * time.Second)
		evt.MarkAsRetrying(next, "connection refused")
		if err := repo.UpdateStatus(ctx, evt); err != nil {
			t.Fatalf("UpdateStatus failed: %v", err)
		}

		got, _ := repo.GetByID(ctx, evt.ID)
		if got.Status != domain.EventStatusRetrying {
			t.Errorf("expected retrying, got %s", got.Status)
		}
		if got.LastError == nil || *got.LastError != "connection refused" {
			t.Errorf("expected last_error='connection refused', got %v", got.LastError)
		}
		if got.Attempts != 1 {
			t.Errorf("expected attempts=1, got %d", got.Attempts)
		}
	})
}

func TestEventRepository_UpdateStatusBatch(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)

	t.Run("empty batch is no-op", func(t *testing.T) {
		if err := repo.UpdateStatusBatch(ctx, nil); err != nil {
			t.Fatalf("UpdateStatusBatch(nil) failed: %v", err)
		}
	})

	t.Run("updates multiple events", func(t *testing.T) {
		events := []*domain.Event{
			makeEvent("evt-batch-upd-1"),
			makeEvent("evt-batch-upd-2"),
		}
		_ = repo.CreateBatch(ctx, events)

		deliveredAt := time.Now().UTC()
		for _, e := range events {
			e.MarkAsDelivered(deliveredAt)
		}
		if err := repo.UpdateStatusBatch(ctx, events); err != nil {
			t.Fatalf("UpdateStatusBatch failed: %v", err)
		}

		for _, e := range events {
			got, _ := repo.GetByID(ctx, e.ID)
			if got.Status != domain.EventStatusDelivered {
				t.Errorf("event %s: expected delivered, got %s", e.ID, got.Status)
			}
		}
	})
}

func TestEventRepository_RecordAttempt(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)

	evt := makeEvent("evt-attempt-1")
	_ = repo.Create(ctx, evt)

	statusCode := 500
	errMsg := "internal server error"
	attempt := &domain.DeliveryAttempt{
		EventID:       evt.ID,
		AttemptNumber: 1,
		StatusCode:    &statusCode,
		Error:         &errMsg,
		DurationMs:    123,
	}

	if err := repo.RecordAttempt(ctx, attempt); err != nil {
		t.Fatalf("RecordAttempt failed: %v", err)
	}
	if attempt.ID == 0 {
		t.Error("expected attempt.ID to be set after insert")
	}

	attempts, err := repo.GetAttemptsByEventID(ctx, evt.ID)
	if err != nil {
		t.Fatalf("GetAttemptsByEventID failed: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(attempts))
	}
	if *attempts[0].StatusCode != statusCode {
		t.Errorf("expected status_code=%d, got %d", statusCode, *attempts[0].StatusCode)
	}
}

func TestEventRepository_RecordAttemptBatch(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)

	evt := makeEvent("evt-attempt-batch-1")
	_ = repo.Create(ctx, evt)

	t.Run("empty batch is no-op", func(t *testing.T) {
		if err := repo.RecordAttemptBatch(ctx, nil); err != nil {
			t.Fatalf("RecordAttemptBatch(nil) failed: %v", err)
		}
	})

	t.Run("records multiple attempts", func(t *testing.T) {
		sc1, sc2 := 500, 503
		attempts := []*domain.DeliveryAttempt{
			{EventID: evt.ID, AttemptNumber: 1, StatusCode: &sc1, DurationMs: 100},
			{EventID: evt.ID, AttemptNumber: 2, StatusCode: &sc2, DurationMs: 200},
		}
		if err := repo.RecordAttemptBatch(ctx, attempts); err != nil {
			t.Fatalf("RecordAttemptBatch failed: %v", err)
		}
		got, _ := repo.GetAttemptsByEventID(ctx, evt.ID)
		if len(got) != 2 {
			t.Errorf("expected 2 attempts, got %d", len(got))
		}
	})
}

func TestEventRepository_PersistNewOutcomes_RollsBackOnAttemptFailure(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)
	event := makeEvent("evt-atomic-create")
	event.Status = domain.EventStatusDelivered

	err := repo.PersistNewOutcomes(ctx, []repository.EventOutcome{{
		Event: event,
		Attempts: []*domain.DeliveryAttempt{{
			EventID:       "missing-parent",
			AttemptNumber: 1,
			DurationMs:    10,
		}},
	}})
	if err == nil {
		t.Fatal("expected foreign-key failure")
	}
	if _, err := repo.GetByID(ctx, event.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("event insert should have rolled back, got %v", err)
	}
}

func TestEventRepository_PersistUpdatedOutcomes_RollsBackOnAttemptFailure(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)
	event := makeEvent("evt-atomic-update")
	if err := repo.Create(ctx, event); err != nil {
		t.Fatalf("create event: %v", err)
	}

	deliveredAt := time.Now().UTC()
	event.MarkAsDelivered(deliveredAt)
	err := repo.PersistUpdatedOutcomes(ctx, []repository.EventOutcome{{
		Event: event,
		Attempts: []*domain.DeliveryAttempt{{
			EventID:       "missing-parent",
			AttemptNumber: 1,
			DurationMs:    10,
		}},
	}})
	if err == nil {
		t.Fatal("expected foreign-key failure")
	}
	got, err := repo.GetByID(ctx, event.ID)
	if err != nil {
		t.Fatalf("get event: %v", err)
	}
	if got.Status != domain.EventStatusPending {
		t.Fatalf("event update should have rolled back, got status %s", got.Status)
	}
}

func TestEventRepository_PersistNewOutcomes_DuplicateEventKeepsOneRowAndRecordsAttempts(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)
	event := makeEvent("evt-duplicate-outcome")
	event.Status = domain.EventStatusDelivered
	statusCode := http.StatusOK

	for range 2 {
		err := repo.PersistNewOutcomes(ctx, []repository.EventOutcome{{
			Event: event,
			Attempts: []*domain.DeliveryAttempt{{
				EventID:       event.ID,
				AttemptNumber: 1,
				StatusCode:    &statusCode,
				DurationMs:    10,
			}},
		}})
		if err != nil {
			t.Fatalf("persist duplicate outcome: %v", err)
		}
	}

	var eventCount int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM events WHERE id = $1", event.ID).Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("expected one event row, got %d", eventCount)
	}
	attempts, err := repo.GetAttemptsByEventID(ctx, event.ID)
	if err != nil {
		t.Fatalf("get attempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("expected both performed attempts to be recorded, got %d", len(attempts))
	}
}

func TestEventRepository_GetAttemptsByEventID(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)

	t.Run("no attempts returns empty slice", func(t *testing.T) {
		evt := makeEvent("evt-noattempts")
		_ = repo.Create(ctx, evt)

		attempts, err := repo.GetAttemptsByEventID(ctx, evt.ID)
		if err != nil {
			t.Fatalf("GetAttemptsByEventID failed: %v", err)
		}
		if len(attempts) != 0 {
			t.Errorf("expected 0 attempts, got %d", len(attempts))
		}
	})

	t.Run("returns ordered by attempt_number", func(t *testing.T) {
		evt := makeEvent("evt-ordered-attempts")
		_ = repo.Create(ctx, evt)

		sc := 500
		for _, n := range []int{3, 1, 2} {
			_ = repo.RecordAttempt(ctx, &domain.DeliveryAttempt{
				EventID:       evt.ID,
				AttemptNumber: n,
				StatusCode:    &sc,
				DurationMs:    10,
			})
		}

		attempts, err := repo.GetAttemptsByEventID(ctx, evt.ID)
		if err != nil {
			t.Fatalf("GetAttemptsByEventID failed: %v", err)
		}
		if len(attempts) != 3 {
			t.Fatalf("expected 3 attempts, got %d", len(attempts))
		}
		for i, a := range attempts {
			if a.AttemptNumber != i+1 {
				t.Errorf("expected attempt_number=%d at index %d, got %d", i+1, i, a.AttemptNumber)
			}
		}
	})
}

func TestEventRepository_Shutdown(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()

	t.Run("shutdown without batcher is no-op", func(t *testing.T) {
		repo := NewEventRepository(pool)
		if err := repo.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown without batcher failed: %v", err)
		}
	})

	t.Run("shutdown with batcher flushes pending", func(t *testing.T) {
		repo := NewEventRepository(pool).WithBatcher(DefaultBatcherConfig())
		evt := makeEvent("evt-shutdown-flush")
		_ = repo.Create(ctx, evt)
		if err := repo.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown failed: %v", err)
		}
		if _, err := repo.GetByID(ctx, evt.ID); err != nil {
			t.Errorf("event not found after shutdown flush: %v", err)
		}
	})
}
