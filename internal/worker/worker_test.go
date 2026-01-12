package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/felipemaragno/dispatch/internal/clock"
	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/retry"
)

type mockEventRepo struct {
	events        []*domain.Event
	pendingEvents []*domain.Event
	attempts      []*domain.DeliveryAttempt
	createCalled  int
	updateCalled  int
	lastUpdated   *domain.Event
	getPendingErr error
}

func (m *mockEventRepo) Create(ctx context.Context, event *domain.Event) error {
	m.createCalled++
	m.events = append(m.events, event)
	return nil
}

func (m *mockEventRepo) GetByID(ctx context.Context, id string) (*domain.Event, error) {
	for _, e := range m.events {
		if e.ID == id {
			return e, nil
		}
	}
	return nil, nil
}

func (m *mockEventRepo) GetPendingEvents(ctx context.Context, limit int) ([]*domain.Event, error) {
	if m.getPendingErr != nil {
		return nil, m.getPendingErr
	}
	if len(m.pendingEvents) == 0 {
		return nil, nil
	}
	result := m.pendingEvents
	m.pendingEvents = nil
	return result, nil
}

func (m *mockEventRepo) UpdateStatus(ctx context.Context, event *domain.Event) error {
	m.updateCalled++
	m.lastUpdated = event
	return nil
}

func (m *mockEventRepo) RecordAttempt(ctx context.Context, attempt *domain.DeliveryAttempt) error {
	m.attempts = append(m.attempts, attempt)
	return nil
}

func (m *mockEventRepo) GetAttemptsByEventID(ctx context.Context, eventID string) ([]*domain.DeliveryAttempt, error) {
	var result []*domain.DeliveryAttempt
	for _, a := range m.attempts {
		if a.EventID == eventID {
			result = append(result, a)
		}
	}
	return result, nil
}

type mockSubRepo struct {
	subs []*domain.Subscription
}

func (m *mockSubRepo) Create(ctx context.Context, sub *domain.Subscription) error {
	m.subs = append(m.subs, sub)
	return nil
}

func (m *mockSubRepo) GetByID(ctx context.Context, id string) (*domain.Subscription, error) {
	for _, s := range m.subs {
		if s.ID == id {
			return s, nil
		}
	}
	return nil, nil
}

func (m *mockSubRepo) GetActive(ctx context.Context) ([]*domain.Subscription, error) {
	return m.subs, nil
}

func (m *mockSubRepo) GetByEventType(ctx context.Context, eventType string) ([]*domain.Subscription, error) {
	var result []*domain.Subscription
	for _, s := range m.subs {
		if s.MatchesEventType(eventType) {
			result = append(result, s)
		}
	}
	return result, nil
}

func (m *mockSubRepo) Delete(ctx context.Context, id string) error {
	return nil
}

func TestWorker_DeliverSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Dispatch-Event-ID") == "" {
			t.Error("missing X-Dispatch-Event-ID header")
		}
		if r.Header.Get("X-Dispatch-Event-Type") == "" {
			t.Error("missing X-Dispatch-Event-Type header")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	eventRepo := &mockEventRepo{
		pendingEvents: []*domain.Event{
			{
				ID:          "evt_123",
				Type:        "order.created",
				Source:      "test",
				Data:        json.RawMessage(`{"test": true}`),
				Status:      domain.EventStatusPending,
				Attempts:    0,
				MaxAttempts: 5,
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			},
		},
	}

	subRepo := &mockSubRepo{
		subs: []*domain.Subscription{
			{
				ID:         "sub_123",
				URL:        server.URL,
				EventTypes: []string{"order.*"},
				Active:     true,
			},
		},
	}

	mockClock := &clock.MockClock{NowTime: time.Now()}

	pool := NewPool(
		Config{
			Workers:      1,
			PollInterval: 10 * time.Millisecond,
			BatchSize:    10,
			Timeout:      5 * time.Second,
		},
		eventRepo,
		subRepo,
		server.Client(),
		mockClock,
		retry.DefaultPolicy(),
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	pool.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	pool.Stop()

	if eventRepo.updateCalled == 0 {
		t.Error("expected UpdateStatus to be called")
	}

	if eventRepo.lastUpdated == nil {
		t.Fatal("expected lastUpdated to be set")
	}

	if eventRepo.lastUpdated.Status != domain.EventStatusDelivered {
		t.Errorf("expected status %v, got %v", domain.EventStatusDelivered, eventRepo.lastUpdated.Status)
	}

	if len(eventRepo.attempts) == 0 {
		t.Error("expected at least one delivery attempt to be recorded")
	}
}

func TestWorker_DeliverFailure_SchedulesRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	eventRepo := &mockEventRepo{
		pendingEvents: []*domain.Event{
			{
				ID:          "evt_456",
				Type:        "order.created",
				Source:      "test",
				Data:        json.RawMessage(`{}`),
				Status:      domain.EventStatusPending,
				Attempts:    0,
				MaxAttempts: 5,
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			},
		},
	}

	subRepo := &mockSubRepo{
		subs: []*domain.Subscription{
			{
				ID:         "sub_123",
				URL:        server.URL,
				EventTypes: []string{"*"},
				Active:     true,
			},
		},
	}

	mockClock := &clock.MockClock{NowTime: time.Now()}

	pool := NewPool(
		Config{
			Workers:      1,
			PollInterval: 10 * time.Millisecond,
			BatchSize:    10,
			Timeout:      5 * time.Second,
		},
		eventRepo,
		subRepo,
		server.Client(),
		mockClock,
		retry.DefaultPolicy(),
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	pool.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	pool.Stop()

	if eventRepo.lastUpdated == nil {
		t.Fatal("expected lastUpdated to be set")
	}

	if eventRepo.lastUpdated.Status != domain.EventStatusRetrying {
		t.Errorf("expected status %v, got %v", domain.EventStatusRetrying, eventRepo.lastUpdated.Status)
	}

	if eventRepo.lastUpdated.Attempts != 1 {
		t.Errorf("expected attempts to be 1, got %d", eventRepo.lastUpdated.Attempts)
	}

	if eventRepo.lastUpdated.NextAttemptAt == nil {
		t.Error("expected NextAttemptAt to be set")
	}
}

func TestWorker_DeliverFailure_MaxRetries_MarksFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	// Attempts=5 means all 5 attempts exhausted, CanRetry() returns false
	eventRepo := &mockEventRepo{
		pendingEvents: []*domain.Event{
			{
				ID:          "evt_789",
				Type:        "order.created",
				Source:      "test",
				Data:        json.RawMessage(`{}`),
				Status:      domain.EventStatusRetrying,
				Attempts:    5,
				MaxAttempts: 5,
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			},
		},
	}

	subRepo := &mockSubRepo{
		subs: []*domain.Subscription{
			{
				ID:         "sub_123",
				URL:        server.URL,
				EventTypes: []string{"*"},
				Active:     true,
			},
		},
	}

	mockClock := &clock.MockClock{NowTime: time.Now()}

	pool := NewPool(
		Config{
			Workers:      1,
			PollInterval: 10 * time.Millisecond,
			BatchSize:    10,
			Timeout:      5 * time.Second,
		},
		eventRepo,
		subRepo,
		server.Client(),
		mockClock,
		retry.DefaultPolicy(),
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	pool.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	pool.Stop()

	if eventRepo.lastUpdated == nil {
		t.Fatal("expected lastUpdated to be set")
	}

	if eventRepo.lastUpdated.Status != domain.EventStatusFailed {
		t.Errorf("expected status %v, got %v", domain.EventStatusFailed, eventRepo.lastUpdated.Status)
	}

	if eventRepo.lastUpdated.Attempts != 6 {
		t.Errorf("expected attempts to be 6, got %d", eventRepo.lastUpdated.Attempts)
	}
}

func TestWorker_NoSubscriptions_MarksDelivered(t *testing.T) {
	eventRepo := &mockEventRepo{
		pendingEvents: []*domain.Event{
			{
				ID:          "evt_no_subs",
				Type:        "unknown.event",
				Source:      "test",
				Data:        json.RawMessage(`{}`),
				Status:      domain.EventStatusPending,
				Attempts:    0,
				MaxAttempts: 5,
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			},
		},
	}

	subRepo := &mockSubRepo{
		subs: []*domain.Subscription{},
	}

	mockClock := &clock.MockClock{NowTime: time.Now()}

	pool := NewPool(
		Config{
			Workers:      1,
			PollInterval: 10 * time.Millisecond,
			BatchSize:    10,
			Timeout:      5 * time.Second,
		},
		eventRepo,
		subRepo,
		&http.Client{},
		mockClock,
		retry.DefaultPolicy(),
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	pool.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	pool.Stop()

	if eventRepo.lastUpdated == nil {
		t.Fatal("expected lastUpdated to be set")
	}

	if eventRepo.lastUpdated.Status != domain.EventStatusDelivered {
		t.Errorf("expected status %v, got %v", domain.EventStatusDelivered, eventRepo.lastUpdated.Status)
	}
}

func TestWorker_SignatureHeader(t *testing.T) {
	var receivedSignature string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSignature = r.Header.Get("X-Dispatch-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	secret := "my-secret-key"
	eventRepo := &mockEventRepo{
		pendingEvents: []*domain.Event{
			{
				ID:          "evt_sig",
				Type:        "order.created",
				Source:      "test",
				Data:        json.RawMessage(`{"test": true}`),
				Status:      domain.EventStatusPending,
				Attempts:    0,
				MaxAttempts: 5,
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			},
		},
	}

	subRepo := &mockSubRepo{
		subs: []*domain.Subscription{
			{
				ID:         "sub_sig",
				URL:        server.URL,
				EventTypes: []string{"*"},
				Secret:     &secret,
				Active:     true,
			},
		},
	}

	mockClock := &clock.MockClock{NowTime: time.Now()}

	pool := NewPool(
		Config{
			Workers:      1,
			PollInterval: 10 * time.Millisecond,
			BatchSize:    10,
			Timeout:      5 * time.Second,
		},
		eventRepo,
		subRepo,
		server.Client(),
		mockClock,
		retry.DefaultPolicy(),
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	pool.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	pool.Stop()

	if receivedSignature == "" {
		t.Error("expected X-Dispatch-Signature header to be set")
	}

	if len(receivedSignature) < 10 || receivedSignature[:7] != "sha256=" {
		t.Errorf("expected signature to start with 'sha256=', got %s", receivedSignature)
	}
}
