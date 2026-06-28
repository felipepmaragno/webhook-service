package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/kafka"
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

func (m *mockEventRepo) GetByID(ctx context.Context, id string) (*domain.Event, error) {
	if e, ok := m.events[id]; ok {
		return e, nil
	}
	return nil, domain.ErrNotFound
}

func (m *mockEventRepo) GetDeliveriesByEventID(ctx context.Context, eventID string) ([]*domain.Delivery, error) {
	return m.deliveries[eventID], nil
}

func (m *mockEventRepo) GetAttemptsByEventID(ctx context.Context, eventID string) ([]*domain.DeliveryAttempt, error) {
	return m.attempts[eventID], nil
}

func (m *mockEventRepo) ReplayFailedDelivery(ctx context.Context, id string, scheduledAt time.Time) (*domain.Delivery, error) {
	for _, deliveries := range m.deliveries {
		for _, delivery := range deliveries {
			if delivery.ID != id {
				continue
			}
			if delivery.Status != domain.DeliveryStatusFailed {
				return nil, domain.ErrReplayNotEligible
			}
			if delivery.Generation <= 0 {
				delivery.Generation = 1
			}
			delivery.Generation++
			delivery.Status = domain.DeliveryStatusRetrying
			delivery.Attempts = 0
			delivery.NextAttemptAt = &scheduledAt
			delivery.LastError = nil
			delivery.DeliveredAt = nil
			delivery.ProcessingOwner = nil
			delivery.ProcessingDeadline = nil
			return delivery, nil
		}
	}
	return nil, domain.ErrNotFound
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

func (m *mockSubRepo) GetActive(ctx context.Context) ([]*domain.Subscription, error) {
	var result []*domain.Subscription
	for _, s := range m.subs {
		if s.Active {
			result = append(result, s)
		}
	}
	return result, nil
}

func (m *mockSubRepo) Delete(ctx context.Context, id string) error {
	if s, ok := m.subs[id]; ok {
		s.Active = false
		return nil
	}
	return domain.ErrNotFound
}

func (m *mockSubRepo) UpdateSecret(ctx context.Context, id, secret string) error {
	if s, ok := m.subs[id]; ok && s.Active {
		s.Secret = &secret
		return nil
	}
	return domain.ErrNotFound
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

	secret := "must-not-leak"
	eventRepo.deliveries["evt_get"] = []*domain.Delivery{
		{
			ID:                 "evt_get:sub_1",
			EventID:            "evt_get",
			SubscriptionID:     "sub_1",
			SubscriptionURL:    "https://example.com",
			SubscriptionSecret: &secret,
			Status:             domain.DeliveryStatusPending,
			MaxAttempts:        5,
			CreatedAt:          time.Now(),
			UpdatedAt:          time.Now(),
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
	if strings.Contains(rec.Body.String(), secret) || strings.Contains(rec.Body.String(), "subscription_secret") {
		t.Fatalf("delivery response exposed secret: %s", rec.Body.String())
	}
	if resp[0].ID != "evt_get:sub_1" {
		t.Errorf("expected delivery id evt_get:sub_1, got %q", resp[0].ID)
	}
}

func TestHandler_GetEventDeliveries_EmptyForEventWithoutDestinations(t *testing.T) {
	publisher := newMockPublisher()
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	handler := NewHandler(publisher, eventRepo, subRepo, logger)
	router := newTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/events/no-destinations/deliveries", nil)
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
		t.Fatalf("expected no deliveries for event without destinations, got %d", len(resp))
	}
}

func TestHandler_ReplayDelivery(t *testing.T) {
	h, _, _ := newTestHandler(t)
	eventRepo := h.eventRepo.(*mockEventRepo)
	delivery := &domain.Delivery{
		ID:         "evt-replay:sub-1",
		EventID:    "evt-replay",
		Status:     domain.DeliveryStatusFailed,
		Attempts:   5,
		Generation: 1,
	}
	eventRepo.deliveries[delivery.EventID] = []*domain.Delivery{delivery}
	rec := httptest.NewRecorder()

	newTestRouter(h).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/deliveries/evt-replay:sub-1/replay", nil))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var response ReplayDeliveryResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.ID != delivery.ID || response.Status != domain.DeliveryStatusRetrying || response.Generation != 2 {
		t.Fatalf("unexpected replay response: %+v", response)
	}
	if delivery.Attempts != 0 || delivery.NextAttemptAt == nil {
		t.Fatalf("delivery not scheduled for replay: %+v", delivery)
	}
}

func TestHandler_ReplayDelivery_NotFound(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rec := httptest.NewRecorder()

	newTestRouter(h).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/deliveries/missing/replay", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandler_ReplayDelivery_Conflict(t *testing.T) {
	h, _, _ := newTestHandler(t)
	eventRepo := h.eventRepo.(*mockEventRepo)
	eventRepo.deliveries["evt-active"] = []*domain.Delivery{{
		ID: "evt-active:sub-1", EventID: "evt-active", Status: domain.DeliveryStatusDelivered, Generation: 1,
	}}
	rec := httptest.NewRecorder()

	newTestRouter(h).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/deliveries/evt-active:sub-1/replay", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
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

	if subRepo.subs["sub_test"].MaxDeliveryRate != 100 {
		t.Errorf("expected default max_delivery_rate 100, got %d", subRepo.subs["sub_test"].MaxDeliveryRate)
	}
}

func TestHandler_CreateSubscription_DoesNotReturnSecret(t *testing.T) {
	h, _, _ := newTestHandler(t)
	router := newTestRouter(h)
	body := `{"id":"sub-secret","url":"https://example.com/webhook","event_types":["*"],"secret":"do-not-return"}`
	req := httptest.NewRequest(http.MethodPost, "/subscriptions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if strings.Contains(rec.Body.String(), "do-not-return") || strings.Contains(rec.Body.String(), `"secret"`) {
		t.Fatalf("response exposed secret: %s", rec.Body.String())
	}
}

func TestHandler_GetSubscriptions_DoesNotReturnSecrets(t *testing.T) {
	h, _, subRepo := newTestHandler(t)
	secret := "list-secret"
	subRepo.subs["sub-secret"] = &domain.Subscription{ID: "sub-secret", URL: "https://example.com", Secret: &secret, Active: true}
	rec := httptest.NewRecorder()

	newTestRouter(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/subscriptions", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if strings.Contains(rec.Body.String(), secret) || strings.Contains(rec.Body.String(), `"secret"`) {
		t.Fatalf("response exposed secret: %s", rec.Body.String())
	}
}

func TestHandler_RotateSubscriptionSecret(t *testing.T) {
	h, _, subRepo := newTestHandler(t)
	oldSecret := "old-secret"
	subRepo.subs["sub-rotate"] = &domain.Subscription{ID: "sub-rotate", Secret: &oldSecret, Active: true}
	req := httptest.NewRequest(http.MethodPut, "/subscriptions/sub-rotate/secret", bytes.NewBufferString(`{"secret":"new-secret"}`))
	rec := httptest.NewRecorder()

	newTestRouter(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := *subRepo.subs["sub-rotate"].Secret; got != "new-secret" {
		t.Fatalf("stored secret = %q, want new-secret", got)
	}
	if strings.Contains(rec.Body.String(), "new-secret") || strings.Contains(rec.Body.String(), `"secret"`) {
		t.Fatalf("response exposed secret: %s", rec.Body.String())
	}
}

func TestHandler_RotateSubscriptionSecret_NotFound(t *testing.T) {
	h, _, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPut, "/subscriptions/missing/secret", bytes.NewBufferString(`{"secret":"new-secret"}`))
	rec := httptest.NewRecorder()

	newTestRouter(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandler_RotateSubscriptionSecret_InvalidInput(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{"secret"`},
		{name: "empty", body: `{"secret":""}`},
		{name: "too large", body: `{"secret":"` + strings.Repeat("x", maxSubscriptionSecretBytes+1) + `"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, subRepo := newTestHandler(t)
			oldSecret := "old-secret"
			subRepo.subs["sub-rotate"] = &domain.Subscription{ID: "sub-rotate", Secret: &oldSecret, Active: true}
			req := httptest.NewRequest(http.MethodPut, "/subscriptions/sub-rotate/secret", bytes.NewBufferString(tt.body))
			rec := httptest.NewRecorder()

			newTestRouter(h).ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if got := *subRepo.subs["sub-rotate"].Secret; got != oldSecret {
				t.Fatalf("stored secret changed to %q", got)
			}
		})
	}
}

func TestHandler_CreateSubscription_CustomMaxDeliveryRate(t *testing.T) {
	publisher := newMockPublisher()
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	handler := NewHandler(publisher, eventRepo, subRepo, logger)
	router := newTestRouter(handler)

	body := `{"id":"sub_policy","url":"https://example.com/webhook","event_types":["order.*"],"max_delivery_rate":25}`
	req := httptest.NewRequest(http.MethodPost, "/subscriptions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}
	sub := subRepo.subs["sub_policy"]
	if sub.MaxDeliveryRate != 25 {
		t.Errorf("expected max_delivery_rate 25, got %d", sub.MaxDeliveryRate)
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

func newTestRouter(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Route("/events", func(r chi.Router) {
		r.Post("/", h.CreateEvent)
		r.Get("/{id}", h.GetEvent)
		r.Get("/{id}/attempts", h.GetEventAttempts)
		r.Get("/{id}/deliveries", h.GetEventDeliveries)
	})
	r.Post("/deliveries/{id}/replay", h.ReplayDelivery)
	r.Route("/subscriptions", func(r chi.Router) {
		r.Post("/", h.CreateSubscription)
		r.Get("/", h.GetSubscriptions)
		r.Put("/{id}/secret", h.RotateSubscriptionSecret)
		r.Delete("/{id}", h.DeleteSubscription)
	})
	return r
}
