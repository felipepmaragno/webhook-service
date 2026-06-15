package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
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

func TestEventRepository_ClaimRetryEvents(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)
	due := func(id string) *domain.Event {
		event := makeEvent(id)
		now := time.Now().Add(-time.Minute)
		event.Status = domain.EventStatusRetrying
		event.NextAttemptAt = &now
		return event
	}

	t.Run("claims due retries with owner and deadline", func(t *testing.T) {
		_ = repo.Create(ctx, due("evt-claim-1"))
		_ = repo.Create(ctx, due("evt-claim-2"))

		claims, err := repo.ClaimRetryEvents(ctx, "worker-a", time.Minute, 10)
		if err != nil {
			t.Fatalf("ClaimRetryEvents failed: %v", err)
		}
		if len(claims) != 2 {
			t.Fatalf("expected 2 claims, got %d", len(claims))
		}
		for _, claim := range claims {
			e := claim.Event
			if e.Status != domain.EventStatusProcessing {
				t.Errorf("expected processing, got %s for event %s", e.Status, e.ID)
			}
			if e.ProcessingOwner == nil || *e.ProcessingOwner != "worker-a" {
				t.Errorf("expected worker-a owner for event %s", e.ID)
			}
			if e.ProcessingDeadline == nil || !e.ProcessingDeadline.After(time.Now()) {
				t.Errorf("expected future deadline for event %s", e.ID)
			}
			if claim.Reclaimed {
				t.Errorf("newly due event %s should not be marked reclaimed", e.ID)
			}
		}
	})

	t.Run("respects limit", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			_ = repo.Create(ctx, due("evt-limit-retry-"+string(rune('a'+i))))
		}
		claims, err := repo.ClaimRetryEvents(ctx, "worker-limit", time.Minute, 2)
		if err != nil {
			t.Fatalf("ClaimRetryEvents failed: %v", err)
		}
		if len(claims) != 2 {
			t.Errorf("expected 2 events, got %d", len(claims))
		}
	})

	t.Run("skips pending and future retry events", func(t *testing.T) {
		pending := makeEvent("evt-pending-owned-by-kafka")
		_ = repo.Create(ctx, pending)
		future := time.Now().Add(1 * time.Hour)
		evt := makeEvent("evt-future-retry")
		evt.Status = domain.EventStatusRetrying
		evt.NextAttemptAt = &future
		_ = repo.Create(ctx, evt)

		claims, err := repo.ClaimRetryEvents(ctx, "worker-skip", time.Minute, 100)
		if err != nil {
			t.Fatalf("ClaimRetryEvents failed: %v", err)
		}
		for _, claim := range claims {
			if claim.Event.ID == evt.ID || claim.Event.ID == pending.ID {
				t.Errorf("ineligible event %s should not be claimed", claim.Event.ID)
			}
		}
	})
}

func TestEventRepository_GetRetryBacklogStats(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)
	now := time.Now().UTC()

	insert := func(id string, status domain.EventStatus, nextAttempt, deadline *time.Time) {
		t.Helper()
		event := makeEvent(id)
		event.Status = status
		event.NextAttemptAt = nextAttempt
		if err := repo.Create(ctx, event); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if deadline != nil {
			if _, err := pool.Exec(ctx, `
				UPDATE events
				SET processing_owner = 'worker-a', processing_deadline = $2
				WHERE id = $1
			`, id, *deadline); err != nil {
				t.Fatalf("set deadline for %s: %v", id, err)
			}
		}
	}

	oldestDue := now.Add(-2 * time.Minute)
	recentDue := now.Add(-30 * time.Second)
	future := now.Add(time.Hour)
	expired := now.Add(-time.Minute)
	leased := now.Add(time.Minute)
	insert("evt-stats-oldest", domain.EventStatusRetrying, &oldestDue, nil)
	insert("evt-stats-recent", domain.EventStatusThrottled, &recentDue, nil)
	insert("evt-stats-future", domain.EventStatusRetrying, &future, nil)
	insert("evt-stats-expired", domain.EventStatusProcessing, nil, &expired)
	insert("evt-stats-leased", domain.EventStatusProcessing, nil, &leased)
	insert("evt-stats-delivered", domain.EventStatusDelivered, nil, nil)

	stats, err := repo.GetRetryBacklogStats(ctx)
	if err != nil {
		t.Fatalf("GetRetryBacklogStats: %v", err)
	}
	if stats.DueCount != 2 || stats.ExpiredProcessingCount != 1 || stats.LeasedCount != 1 {
		t.Fatalf("unexpected backlog stats: %+v", stats)
	}
	if stats.OldestDueAt == nil || stats.OldestDueAt.After(oldestDue.Add(time.Second)) {
		t.Fatalf("oldest due time = %v, want around %v", stats.OldestDueAt, oldestDue)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin explain transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("disable sequential scan: %v", err)
	}
	rows, err := tx.Query(ctx, "EXPLAIN "+retryBacklogStatsQuery)
	if err != nil {
		t.Fatalf("explain backlog query: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan explain plan: %v", err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read explain plan: %v", err)
	}
	if !strings.Contains(plan.String(), "idx_events_retry_claimable") {
		t.Fatalf("backlog query did not use retry claim index:\n%s", plan.String())
	}
}

func TestEventRepository_ClaimRetryEvents_LeaseRecoveryAndFencing(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)
	event := makeEvent("evt-lease-recovery")
	due := time.Now().Add(-time.Minute)
	event.Status = domain.EventStatusRetrying
	event.NextAttemptAt = &due
	if err := repo.Create(ctx, event); err != nil {
		t.Fatalf("create event: %v", err)
	}

	first, err := repo.ClaimRetryEvents(ctx, "worker-a", time.Minute, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: claims=%d err=%v", len(first), err)
	}
	beforeExpiry, err := repo.ClaimRetryEvents(ctx, "worker-b", time.Minute, 1)
	if err != nil {
		t.Fatalf("claim before expiry: %v", err)
	}
	if len(beforeExpiry) != 0 {
		t.Fatal("active lease should not be claimable by another worker")
	}

	if _, err := pool.Exec(ctx, `UPDATE events SET processing_deadline = NOW() - INTERVAL '1 second' WHERE id = $1`, event.ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	second, err := repo.ClaimRetryEvents(ctx, "worker-a", time.Minute, 1)
	if err != nil || len(second) != 1 {
		t.Fatalf("reclaim expired event: claims=%d err=%v", len(second), err)
	}
	if !second[0].Reclaimed {
		t.Fatal("expired processing event should be marked reclaimed")
	}

	firstEvent := first[0].Event
	firstEvent.MarkAsDelivered(time.Now())
	err = repo.PersistClaimedOutcomes(ctx, []repository.EventOutcome{{Event: firstEvent}})
	if !errors.Is(err, repository.ErrClaimLost) {
		t.Fatalf("expected stale owner rejection, got %v", err)
	}

	secondEvent := second[0].Event
	secondEvent.MarkAsDelivered(time.Now())
	if err := repo.PersistClaimedOutcomes(ctx, []repository.EventOutcome{{Event: secondEvent}}); err != nil {
		t.Fatalf("persist current owner outcome: %v", err)
	}
	got, err := repo.GetByID(ctx, event.ID)
	if err != nil {
		t.Fatalf("get event: %v", err)
	}
	if got.Status != domain.EventStatusDelivered || got.ProcessingOwner != nil || got.ProcessingDeadline != nil {
		t.Fatalf("expected delivered event with cleared lease, got %+v", got)
	}
}

func TestEventRepository_ClaimRetryEvents_ConcurrentWorkersAreExclusive(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)
	event := makeEvent("evt-exclusive-claim")
	due := time.Now().Add(-time.Minute)
	event.Status = domain.EventStatusRetrying
	event.NextAttemptAt = &due
	if err := repo.Create(ctx, event); err != nil {
		t.Fatalf("create event: %v", err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	counts := make(chan int, 2)
	errs := make(chan error, 2)
	for _, owner := range []string{"worker-a", "worker-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			<-start
			claims, err := repo.ClaimRetryEvents(ctx, owner, time.Minute, 1)
			counts <- len(claims)
			errs <- err
		}(owner)
	}
	close(start)
	wg.Wait()
	close(counts)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent claim: %v", err)
		}
	}
	total := 0
	for count := range counts {
		total += count
	}
	if total != 1 {
		t.Fatalf("expected exactly one worker to claim event, total claims=%d", total)
	}
}

func TestEventRepository_PersistClaimedOutcomes_ClearsLeaseForEveryOutcome(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)
	statuses := []domain.EventStatus{
		domain.EventStatusDelivered,
		domain.EventStatusRetrying,
		domain.EventStatusThrottled,
		domain.EventStatusFailed,
	}

	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			event := makeEvent("evt-clear-lease-" + string(status))
			due := time.Now().Add(-time.Minute)
			event.Status = domain.EventStatusRetrying
			event.NextAttemptAt = &due
			if err := repo.Create(ctx, event); err != nil {
				t.Fatalf("create event: %v", err)
			}
			claims, err := repo.ClaimRetryEvents(ctx, "worker-clear", time.Minute, 1)
			if err != nil || len(claims) != 1 {
				t.Fatalf("claim event: claims=%d err=%v", len(claims), err)
			}
			claimed := claims[0].Event
			claimed.Status = status
			claimed.UpdatedAt = time.Now()
			if status == domain.EventStatusRetrying || status == domain.EventStatusThrottled {
				next := time.Now().Add(time.Hour)
				claimed.NextAttemptAt = &next
			}
			if err := repo.PersistClaimedOutcomes(ctx, []repository.EventOutcome{{Event: claimed}}); err != nil {
				t.Fatalf("persist outcome: %v", err)
			}

			got, err := repo.GetByID(ctx, event.ID)
			if err != nil {
				t.Fatalf("get event: %v", err)
			}
			if got.Status != status || got.ProcessingOwner != nil || got.ProcessingDeadline != nil {
				t.Fatalf("expected status %s with cleared lease, got %+v", status, got)
			}
		})
	}
}

func TestRetryLeaseMigrationDownRemovesLeaseColumns(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	sql, err := os.ReadFile("../../../migrations/003_add_retry_claim_lease.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("apply down migration: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_name = 'events' AND column_name IN ('processing_owner', 'processing_deadline')
	`).Scan(&count); err != nil {
		t.Fatalf("inspect lease columns: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected lease columns removed, found %d", count)
	}
}

func TestRetryLeaseMigrationMakesLegacyProcessingRowsRecoverable(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	downSQL, err := os.ReadFile("../../../migrations/003_add_retry_claim_lease.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(downSQL)); err != nil {
		t.Fatalf("apply down migration: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (id, type, source, data, status)
		VALUES ('evt-legacy-processing', 'legacy.event', 'migration-test', '{}', 'processing')
	`); err != nil {
		t.Fatalf("insert legacy processing event: %v", err)
	}

	upSQL, err := os.ReadFile("../../../migrations/003_add_retry_claim_lease.up.sql")
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(upSQL)); err != nil {
		t.Fatalf("reapply up migration: %v", err)
	}

	repo := NewEventRepository(pool)
	claims, err := repo.ClaimRetryEvents(ctx, "worker-after-migration", time.Minute, 1)
	if err != nil {
		t.Fatalf("claim migrated event: %v", err)
	}
	if len(claims) != 1 || claims[0].Event.ID != "evt-legacy-processing" || !claims[0].Reclaimed {
		t.Fatalf("expected migrated processing event to be reclaimable, got %+v", claims)
	}
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

func TestEventRepository_PersistClaimedOutcomes_RollsBackOnAttemptFailure(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)
	event := makeEvent("evt-atomic-update")
	event.Status = domain.EventStatusRetrying
	due := time.Now().Add(-time.Minute)
	event.NextAttemptAt = &due
	if err := repo.Create(ctx, event); err != nil {
		t.Fatalf("create event: %v", err)
	}
	claims, err := repo.ClaimRetryEvents(ctx, "worker-rollback", time.Minute, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim event: claims=%d err=%v", len(claims), err)
	}
	event = claims[0].Event

	deliveredAt := time.Now().UTC()
	event.MarkAsDelivered(deliveredAt)
	err = repo.PersistClaimedOutcomes(ctx, []repository.EventOutcome{{
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
	if got.Status != domain.EventStatusProcessing {
		t.Fatalf("event update should have rolled back, got status %s", got.Status)
	}
	if got.ProcessingOwner == nil || *got.ProcessingOwner != "worker-rollback" {
		t.Fatal("claim metadata should remain after rollback")
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
