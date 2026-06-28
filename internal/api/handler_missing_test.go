package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/kafka"
)

// errorPublisher is a publisher that always returns an error.
type errorPublisher struct{}

func (e *errorPublisher) Publish(_ context.Context, _ kafka.EventMessage) error {
	return errors.New("broker unavailable")
}
func (e *errorPublisher) Close() error { return nil }

// errorSubRepo is a subscription repo that always fails on Create.
type errorSubRepo struct{ *mockSubRepo }

func (e *errorSubRepo) Create(_ context.Context, _ *domain.Subscription) error {
	return errors.New("db error")
}

type updateSecretErrorSubRepo struct{ *mockSubRepo }

func (e *updateSecretErrorSubRepo) UpdateSecret(_ context.Context, _, _ string) error {
	return errors.New("db error")
}

// errorEventRepo wraps mockEventRepo but fails on GetAttemptsByEventID.
type errorEventRepo struct{ *mockEventRepo }

func (e *errorEventRepo) GetAttemptsByEventID(_ context.Context, _ string) ([]*domain.DeliveryAttempt, error) {
	return nil, errors.New("db error")
}

type getEventErrorRepo struct{ *mockEventRepo }

func (e *getEventErrorRepo) GetByID(_ context.Context, _ string) (*domain.Event, error) {
	return nil, errors.New("db error")
}

type replayErrorRepo struct{ *mockEventRepo }

func (e *replayErrorRepo) ReplayFailedDelivery(_ context.Context, _ string, _ time.Time) (*domain.Delivery, error) {
	return nil, errors.New("db error")
}

func newTestHandler(t *testing.T) (*Handler, *mockEventRepo, *mockSubRepo) {
	t.Helper()
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewHandler(newMockPublisher(), eventRepo, subRepo, logger), eventRepo, subRepo
}

// ---------- CreateEvent ----------

func TestHandler_CreateEvent_InvalidBody(t *testing.T) {
	h, _, _ := newTestHandler(t)
	router := newTestRouter(h)

	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewBufferString("not-json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandler_CreateEvent_PublisherError(t *testing.T) {
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	h := NewHandler(&errorPublisher{}, eventRepo, subRepo, logger)
	router := newTestRouter(h)

	body := `{"id":"e1","type":"t","source":"s","data":{}}`
	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

// ---------- GetEvent ----------

func TestHandler_GetEvent_InternalError(t *testing.T) {
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	h := NewHandler(newMockPublisher(), &getEventErrorRepo{newMockEventRepo()}, subRepo, logger)
	router := newTestRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/events/e1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

// ---------- GetEventAttempts ----------

func TestHandler_GetEventAttempts_HappyPath(t *testing.T) {
	h, eventRepo, _ := newTestHandler(t)
	router := newTestRouter(h)

	sc := 200
	eventRepo.attempts["evt-a"] = []*domain.DeliveryAttempt{
		{ID: 1, EventID: "evt-a", AttemptNumber: 1, StatusCode: &sc, DurationMs: 42, CreatedAt: time.Now()},
	}

	req := httptest.NewRequest(http.MethodGet, "/events/evt-a/attempts", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var attempts []*domain.DeliveryAttempt
	if err := json.NewDecoder(rec.Body).Decode(&attempts); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(attempts) != 1 {
		t.Errorf("expected 1 attempt, got %d", len(attempts))
	}
	if *attempts[0].StatusCode != 200 {
		t.Errorf("expected status_code 200, got %d", *attempts[0].StatusCode)
	}
}

func TestHandler_GetEventAttempts_InternalError(t *testing.T) {
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	h := NewHandler(newMockPublisher(), &errorEventRepo{newMockEventRepo()}, subRepo, logger)
	router := newTestRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/events/e1/attempts", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandler_GetEventAttempts_Empty(t *testing.T) {
	h, _, _ := newTestHandler(t)
	router := newTestRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/events/no-attempts/attempts", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestHandler_GetEventAttempts_MultipleAttempts(t *testing.T) {
	h, eventRepo, _ := newTestHandler(t)
	router := newTestRouter(h)

	sc1, sc2, sc3 := 500, 503, 200
	eventRepo.attempts["evt-multi"] = []*domain.DeliveryAttempt{
		{ID: 1, EventID: "evt-multi", AttemptNumber: 1, StatusCode: &sc1, DurationMs: 10},
		{ID: 2, EventID: "evt-multi", AttemptNumber: 2, StatusCode: &sc2, DurationMs: 20},
		{ID: 3, EventID: "evt-multi", AttemptNumber: 3, StatusCode: &sc3, DurationMs: 30},
	}

	req := httptest.NewRequest(http.MethodGet, "/events/evt-multi/attempts", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var attempts []*domain.DeliveryAttempt
	_ = json.NewDecoder(rec.Body).Decode(&attempts)
	if len(attempts) != 3 {
		t.Errorf("expected 3 attempts, got %d", len(attempts))
	}
}

// ---------- GetSubscriptions ----------

func TestHandler_GetSubscriptions_Empty(t *testing.T) {
	h, _, _ := newTestHandler(t)
	router := newTestRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/subscriptions", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var subs []*domain.Subscription
	_ = json.NewDecoder(rec.Body).Decode(&subs)
	if len(subs) != 0 {
		t.Errorf("expected 0 subscriptions, got %d", len(subs))
	}
}

func TestHandler_GetSubscriptions_WithItems(t *testing.T) {
	h, _, subRepo := newTestHandler(t)
	router := newTestRouter(h)

	for i := 0; i < 3; i++ {
		subRepo.subs[fmt.Sprintf("sub-%d", i)] = &domain.Subscription{
			ID:     fmt.Sprintf("sub-%d", i),
			URL:    "https://example.com",
			Active: true,
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/subscriptions", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var subs []*domain.Subscription
	_ = json.NewDecoder(rec.Body).Decode(&subs)
	if len(subs) != 3 {
		t.Errorf("expected 3 subscriptions, got %d", len(subs))
	}
}

// ---------- DeleteSubscription ----------

func TestHandler_DeleteSubscription_NotFound(t *testing.T) {
	h, _, _ := newTestHandler(t)
	router := newTestRouter(h)

	req := httptest.NewRequest(http.MethodDelete, "/subscriptions/nonexistent", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// ---------- CreateSubscription ----------

func TestHandler_CreateSubscription_MissingFields(t *testing.T) {
	h, _, _ := newTestHandler(t)
	router := newTestRouter(h)

	cases := []struct {
		name string
		body string
	}{
		{"missing url", `{"id":"s1","event_types":["*"]}`},
		{"missing id", `{"url":"https://example.com","event_types":["*"]}`},
		{"missing event_types", `{"id":"s1","url":"https://example.com"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/subscriptions", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: expected 400, got %d", tc.name, rec.Code)
			}
		})
	}
}

func TestHandler_CreateSubscription_RepositoryError(t *testing.T) {
	eventRepo := newMockEventRepo()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	h := NewHandler(newMockPublisher(), eventRepo, &errorSubRepo{newMockSubRepo()}, logger)
	router := newTestRouter(h)

	body := `{"id":"sub-1","url":"https://example.com/hook","event_types":["order.created"]}`
	req := httptest.NewRequest(http.MethodPost, "/subscriptions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandler_CreateSubscription_InvalidBody(t *testing.T) {
	h, _, _ := newTestHandler(t)
	router := newTestRouter(h)

	req := httptest.NewRequest(http.MethodPost, "/subscriptions", bytes.NewBufferString("not-json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandler_CreateSubscription_InvalidSecret(t *testing.T) {
	tests := []string{
		`{"id":"sub-1","url":"https://example.com","event_types":["*"],"secret":""}`,
		`{"id":"sub-1","url":"https://example.com","event_types":["*"],"secret":"` + strings.Repeat("x", maxSubscriptionSecretBytes+1) + `"}`,
	}
	for _, body := range tests {
		h, _, subRepo := newTestHandler(t)
		req := httptest.NewRequest(http.MethodPost, "/subscriptions", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()

		newTestRouter(h).ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if len(subRepo.subs) != 0 {
			t.Fatal("invalid secret reached repository")
		}
	}
}

func TestHandler_CreateSubscription_DefaultMaxDeliveryRate(t *testing.T) {
	h, _, subRepo := newTestHandler(t)
	router := newTestRouter(h)

	// MaxDeliveryRate not set — should default to 100
	body := `{"id":"sub-default-rl","url":"https://example.com","event_types":["*"]}`
	req := httptest.NewRequest(http.MethodPost, "/subscriptions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", rec.Code)
	}
	if subRepo.subs["sub-default-rl"].MaxDeliveryRate != 100 {
		t.Errorf("expected default max_delivery_rate=100, got %d", subRepo.subs["sub-default-rl"].MaxDeliveryRate)
	}
}

func TestHandler_RotateSubscriptionSecret_RepositoryError(t *testing.T) {
	eventRepo := newMockEventRepo()
	subRepo := &updateSecretErrorSubRepo{newMockSubRepo()}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	h := NewHandler(newMockPublisher(), eventRepo, subRepo, logger)
	req := httptest.NewRequest(http.MethodPut, "/subscriptions/sub-1/secret", bytes.NewBufferString(`{"secret":"new-secret"}`))
	rec := httptest.NewRecorder()

	newTestRouter(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandler_ReplayDelivery_RepositoryError(t *testing.T) {
	subRepo := newMockSubRepo()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	h := NewHandler(newMockPublisher(), &replayErrorRepo{newMockEventRepo()}, subRepo, logger)
	rec := httptest.NewRecorder()

	newTestRouter(h).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/deliveries/delivery-1/replay", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
