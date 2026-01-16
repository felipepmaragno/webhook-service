package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/kafka"
	"github.com/felipemaragno/dispatch/internal/repository/postgres"
)

// mockPublisher implements EventPublisher for testing
type mockPublisher struct {
	events []kafka.EventMessage
}

func newMockPublisher() *mockPublisher {
	return &mockPublisher{events: make([]kafka.EventMessage, 0)}
}

func (m *mockPublisher) Publish(ctx context.Context, event kafka.EventMessage) error {
	m.events = append(m.events, event)
	return nil
}

func (m *mockPublisher) Close() error {
	return nil
}

type mockEventRepo struct {
	events   map[string]*domain.Event
	attempts map[string][]*domain.DeliveryAttempt
}

func newMockEventRepo() *mockEventRepo {
	return &mockEventRepo{
		events:   make(map[string]*domain.Event),
		attempts: make(map[string][]*domain.DeliveryAttempt),
	}
}

func (m *mockEventRepo) Create(ctx context.Context, event *domain.Event) error {
	m.events[event.ID] = event
	return nil
}

func (m *mockEventRepo) GetByID(ctx context.Context, id string) (*domain.Event, error) {
	if e, ok := m.events[id]; ok {
		return e, nil
	}
	return nil, postgres.ErrNotFound
}

func (m *mockEventRepo) GetPendingEvents(ctx context.Context, limit int) ([]*domain.Event, error) {
	return nil, nil
}

func (m *mockEventRepo) UpdateStatus(ctx context.Context, event *domain.Event) error {
	m.events[event.ID] = event
	return nil
}

func (m *mockEventRepo) RecordAttempt(ctx context.Context, attempt *domain.DeliveryAttempt) error {
	m.attempts[attempt.EventID] = append(m.attempts[attempt.EventID], attempt)
	return nil
}

func (m *mockEventRepo) GetAttemptsByEventID(ctx context.Context, eventID string) ([]*domain.DeliveryAttempt, error) {
	return m.attempts[eventID], nil
}

func (m *mockEventRepo) RecordAttemptBatch(ctx context.Context, attempts []*domain.DeliveryAttempt) error {
	for _, a := range attempts {
		m.attempts[a.EventID] = append(m.attempts[a.EventID], a)
	}
	return nil
}

func (m *mockEventRepo) UpdateStatusBatch(ctx context.Context, events []*domain.Event) error {
	for _, e := range events {
		m.events[e.ID] = e
	}
	return nil
}

func (m *mockEventRepo) Shutdown(ctx context.Context) error {
	return nil
}

type mockSubRepo struct {
	subs map[string]*domain.Subscription
}

func newMockSubRepo() *mockSubRepo {
	return &mockSubRepo{
		subs: make(map[string]*domain.Subscription),
	}
}

func (m *mockSubRepo) Create(ctx context.Context, sub *domain.Subscription) error {
	m.subs[sub.ID] = sub
	return nil
}

func (m *mockSubRepo) GetByID(ctx context.Context, id string) (*domain.Subscription, error) {
	if s, ok := m.subs[id]; ok {
		return s, nil
	}
	return nil, postgres.ErrNotFound
}

func (m *mockSubRepo) GetActive(ctx context.Context) ([]*domain.Subscription, error) {
	var result []*domain.Subscription
	for _, s := range m.subs {
		if s.Active {
			result = append(result, s)
		}
	}
	return result, nil
}

func (m *mockSubRepo) GetByEventType(ctx context.Context, eventType string) ([]*domain.Subscription, error) {
	return m.GetActive(ctx)
}

func (m *mockSubRepo) Delete(ctx context.Context, id string) error {
	if s, ok := m.subs[id]; ok {
		s.Active = false
		return nil
	}
	return postgres.ErrNotFound
}

func (m *mockSubRepo) GetByEventTypes(ctx context.Context, eventTypes []string) (map[string][]*domain.Subscription, error) {
	result := make(map[string][]*domain.Subscription)
	for _, et := range eventTypes {
		for _, s := range m.subs {
			if s.Active {
				result[et] = append(result[et], s)
			}
		}
	}
	return result, nil
}

func TestHandler_CreateEvent(t *testing.T) {
	publisher := newMockPublisher()
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	handler := NewHandler(publisher, eventRepo, subRepo, logger)
	router := newTestRouter(handler)

	body := `{"id": "evt_test", "type": "order.created", "source": "test", "data": {"foo": "bar"}}`
	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("expected status %d, got %d", http.StatusAccepted, rec.Code)
	}

	var resp CreateEventResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.ID != "evt_test" {
		t.Errorf("expected id 'evt_test', got %q", resp.ID)
	}

	if resp.Status != "pending" {
		t.Errorf("expected status 'pending', got %q", resp.Status)
	}

	// Event should be published to Kafka, not stored in DB
	if len(publisher.events) != 1 {
		t.Errorf("expected 1 event published, got %d", len(publisher.events))
	}
	if publisher.events[0].ID != "evt_test" {
		t.Errorf("expected event id 'evt_test', got %q", publisher.events[0].ID)
	}
}

func TestHandler_CreateEvent_MissingFields(t *testing.T) {
	publisher := newMockPublisher()
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	handler := NewHandler(publisher, eventRepo, subRepo, logger)
	router := newTestRouter(handler)

	body := `{"id": "evt_test"}`
	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestHandler_GetEvent(t *testing.T) {
	publisher := newMockPublisher()
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	handler := NewHandler(publisher, eventRepo, subRepo, logger)
	router := newTestRouter(handler)

	event := &domain.Event{
		ID:        "evt_get",
		Type:      "order.created",
		Source:    "test",
		Data:      json.RawMessage(`{}`),
		Status:    domain.EventStatusPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	eventRepo.events["evt_get"] = event

	req := httptest.NewRequest(http.MethodGet, "/events/evt_get", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var resp domain.Event
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.ID != "evt_get" {
		t.Errorf("expected id 'evt_get', got %q", resp.ID)
	}
}

func TestHandler_GetEvent_NotFound(t *testing.T) {
	publisher := newMockPublisher()
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	handler := NewHandler(publisher, eventRepo, subRepo, logger)
	router := newTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/events/nonexistent", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestHandler_CreateSubscription(t *testing.T) {
	publisher := newMockPublisher()
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	handler := NewHandler(publisher, eventRepo, subRepo, logger)
	router := newTestRouter(handler)

	body := `{"id": "sub_test", "url": "https://example.com/webhook", "event_types": ["order.*"]}`
	req := httptest.NewRequest(http.MethodPost, "/subscriptions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}

	if _, ok := subRepo.subs["sub_test"]; !ok {
		t.Error("subscription not stored in repository")
	}

	if subRepo.subs["sub_test"].RateLimit != 100 {
		t.Errorf("expected default rate_limit 100, got %d", subRepo.subs["sub_test"].RateLimit)
	}
}

func TestHandler_DeleteSubscription(t *testing.T) {
	publisher := newMockPublisher()
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	handler := NewHandler(publisher, eventRepo, subRepo, logger)
	router := newTestRouter(handler)

	subRepo.subs["sub_del"] = &domain.Subscription{
		ID:     "sub_del",
		URL:    "https://example.com",
		Active: true,
	}

	req := httptest.NewRequest(http.MethodDelete, "/subscriptions/sub_del", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}

	if subRepo.subs["sub_del"].Active {
		t.Error("expected subscription to be deactivated")
	}
}

func TestHandler_Health(t *testing.T) {
	publisher := newMockPublisher()
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	handler := NewHandler(publisher, eventRepo, subRepo, logger)
	router := newTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
}

func newTestRouter(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/health", h.Health)
	r.Route("/events", func(r chi.Router) {
		r.Post("/", h.CreateEvent)
		r.Get("/{id}", h.GetEvent)
		r.Get("/{id}/attempts", h.GetEventAttempts)
	})
	r.Route("/subscriptions", func(r chi.Router) {
		r.Post("/", h.CreateSubscription)
		r.Get("/", h.GetSubscriptions)
		r.Delete("/{id}", h.DeleteSubscription)
	})
	return r
}
