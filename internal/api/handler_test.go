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
	"github.com/felipemaragno/dispatch/internal/repository"
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
	events     map[string]*domain.Event
	attempts   map[string][]*domain.DeliveryAttempt
	deliveries map[string][]*domain.Delivery
}

func newMockEventRepo() *mockEventRepo {
	return &mockEventRepo{
		events:     make(map[string]*domain.Event),
		attempts:   make(map[string][]*domain.DeliveryAttempt),
		deliveries: make(map[string][]*domain.Delivery),
	}
}

func (m *mockEventRepo) Create(ctx context.Context, event *domain.Event) error {
	m.events[event.ID] = event
	return nil
}

func (m *mockEventRepo) CreateBatch(ctx context.Context, events []*domain.Event) error {
	for _, e := range events {
		m.events[e.ID] = e
	}
	return nil
}

func (m *mockEventRepo) GetByID(ctx context.Context, id string) (*domain.Event, error) {
	if e, ok := m.events[id]; ok {
		return e, nil
	}
	return nil, postgres.ErrNotFound
}

func (m *mockEventRepo) InitializeEventDeliveries(ctx context.Context, event *domain.Event, subscriptions []*domain.Subscription) ([]*domain.Delivery, error) {
	deliveries := make([]*domain.Delivery, 0, len(subscriptions))
	for _, sub := range subscriptions {
		delivery := domain.NewDelivery(event, sub)
		deliveries = append(deliveries, delivery)
	}
	m.deliveries[event.ID] = deliveries
	return deliveries, nil
}

func (m *mockEventRepo) GetDeliveriesByEventID(ctx context.Context, eventID string) ([]*domain.Delivery, error) {
	return m.deliveries[eventID], nil
}

func (m *mockEventRepo) GetDeliveryByID(ctx context.Context, id string) (*domain.Delivery, error) {
	for _, deliveries := range m.deliveries {
		for _, delivery := range deliveries {
			if delivery.ID == id {
				return delivery, nil
			}
		}
	}
	return nil, postgres.ErrNotFound
}

func (m *mockEventRepo) ClaimDeliveries(ctx context.Context, owner string, leaseDuration time.Duration, limit int) ([]repository.ClaimedDelivery, error) {
	return nil, nil
}

func (m *mockEventRepo) ClaimEventDeliveries(ctx context.Context, eventIDs []string, owner string, leaseDuration time.Duration, limit int) ([]repository.ClaimedDelivery, error) {
	return nil, nil
}

func (m *mockEventRepo) PersistDeliveryOutcome(ctx context.Context, delivery *domain.Delivery, attempts []*domain.DeliveryAttempt) error {
	m.deliveries[delivery.EventID] = append(m.deliveries[delivery.EventID], delivery)
	m.attempts[delivery.EventID] = append(m.attempts[delivery.EventID], attempts...)
	return nil
}

func (m *mockEventRepo) PersistClaimedDeliveryOutcome(ctx context.Context, delivery *domain.Delivery, attempts []*domain.DeliveryAttempt) error {
	return m.PersistDeliveryOutcome(ctx, delivery, attempts)
}

func (m *mockEventRepo) ClaimRetryEvents(ctx context.Context, owner string, leaseDuration time.Duration, limit int) ([]repository.ClaimedEvent, error) {
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

func (m *mockEventRepo) PersistNewOutcomes(ctx context.Context, outcomes []repository.EventOutcome) error {
	return nil
}

func (m *mockEventRepo) PersistClaimedOutcomes(ctx context.Context, outcomes []repository.EventOutcome) error {
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

func TestHandler_GetEventDeliveries(t *testing.T) {
	publisher := newMockPublisher()
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	handler := NewHandler(publisher, eventRepo, subRepo, logger)
	router := newTestRouter(handler)

	eventRepo.deliveries["evt_get"] = []*domain.Delivery{
		{
			ID:              "evt_get:sub_1",
			EventID:         "evt_get",
			SubscriptionID:  "sub_1",
			SubscriptionURL: "https://example.com",
			Status:          domain.DeliveryStatusPending,
			MaxAttempts:     5,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		},
		{
			ID:              "evt_get:sub_2",
			EventID:         "evt_get",
			SubscriptionID:  "sub_2",
			SubscriptionURL: "https://example.org",
			Status:          domain.DeliveryStatusRetrying,
			MaxAttempts:     5,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/events/evt_get/deliveries", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var resp []*domain.Delivery
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp) != 2 {
		t.Fatalf("expected 2 deliveries, got %d", len(resp))
	}
	if resp[0].ID != "evt_get:sub_1" {
		t.Errorf("expected delivery id evt_get:sub_1, got %q", resp[0].ID)
	}
}

func TestHandler_GetEventDeliveries_EmptyForLegacyEvent(t *testing.T) {
	publisher := newMockPublisher()
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	handler := NewHandler(publisher, eventRepo, subRepo, logger)
	router := newTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/events/legacy/deliveries", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var resp []*domain.Delivery
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp) != 0 {
		t.Fatalf("expected no deliveries for legacy event, got %d", len(resp))
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
	if subRepo.subs["sub_test"].BurstSize != 10 {
		t.Errorf("expected default burst_size 10, got %d", subRepo.subs["sub_test"].BurstSize)
	}
	if subRepo.subs["sub_test"].ConcurrencyLimit != 100 {
		t.Errorf("expected default concurrency_limit 100, got %d", subRepo.subs["sub_test"].ConcurrencyLimit)
	}
}

func TestHandler_CreateSubscription_CustomRateControls(t *testing.T) {
	publisher := newMockPublisher()
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	handler := NewHandler(publisher, eventRepo, subRepo, logger)
	router := newTestRouter(handler)

	body := `{"id":"sub_policy","url":"https://example.com/webhook","event_types":["order.*"],"rate_limit":25,"burst_size":8,"concurrency_limit":4}`
	req := httptest.NewRequest(http.MethodPost, "/subscriptions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}
	sub := subRepo.subs["sub_policy"]
	if sub.RateLimit != 25 {
		t.Errorf("expected rate_limit 25, got %d", sub.RateLimit)
	}
	if sub.BurstSize != 8 {
		t.Errorf("expected burst_size 8, got %d", sub.BurstSize)
	}
	if sub.ConcurrencyLimit != 4 {
		t.Errorf("expected concurrency_limit 4, got %d", sub.ConcurrencyLimit)
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
		r.Get("/{id}/deliveries", h.GetEventDeliveries)
	})
	r.Route("/subscriptions", func(r chi.Router) {
		r.Post("/", h.CreateSubscription)
		r.Get("/", h.GetSubscriptions)
		r.Delete("/{id}", h.DeleteSubscription)
	})
	return r
}
