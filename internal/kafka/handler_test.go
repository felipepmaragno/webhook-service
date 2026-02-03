package kafka

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/resilience"
	"github.com/felipemaragno/dispatch/internal/retry"
)

func TestIsPermanentFailure(t *testing.T) {
	tests := []struct {
		statusCode int
		expected   bool
		reason     string
	}{
		{400, true, "Bad Request"},
		{401, true, "Unauthorized"},
		{403, true, "Forbidden"},
		{404, true, "Not Found"},
		{405, true, "Method Not Allowed"},
		{406, true, "Not Acceptable"},
		{410, true, "Gone"},
		{411, true, "Length Required"},
		{413, true, "Payload Too Large"},
		{414, true, "URI Too Long"},
		{415, true, "Unsupported Media Type"},
		{422, true, "Unprocessable Entity"},
		{426, true, "Upgrade Required"},
		{431, true, "Request Header Fields Too Large"},
		// Not permanent failures
		{200, false, "OK"},
		{201, false, "Created"},
		{408, false, "Request Timeout - retryable"},
		{429, false, "Too Many Requests - retryable"},
		{500, false, "Internal Server Error - retryable"},
		{502, false, "Bad Gateway - retryable"},
		{503, false, "Service Unavailable - retryable"},
		{504, false, "Gateway Timeout - retryable"},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			result := isPermanentFailure(tt.statusCode)
			if result != tt.expected {
				t.Errorf("isPermanentFailure(%d) = %v, want %v (%s)",
					tt.statusCode, result, tt.expected, tt.reason)
			}
		})
	}
}

func TestIsRetryableFailure(t *testing.T) {
	tests := []struct {
		statusCode int
		expected   bool
		reason     string
	}{
		{408, true, "Request Timeout"},
		{429, true, "Too Many Requests"},
		{500, true, "Internal Server Error"},
		{502, true, "Bad Gateway"},
		{503, true, "Service Unavailable"},
		{504, true, "Gateway Timeout"},
		// Not retryable
		{200, false, "OK - success"},
		{201, false, "Created - success"},
		{400, false, "Bad Request - permanent"},
		{401, false, "Unauthorized - permanent"},
		{403, false, "Forbidden - permanent"},
		{404, false, "Not Found - permanent"},
		{410, false, "Gone - permanent"},
		{422, false, "Unprocessable Entity - permanent"},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			result := isRetryableFailure(tt.statusCode)
			if result != tt.expected {
				t.Errorf("isRetryableFailure(%d) = %v, want %v (%s)",
					tt.statusCode, result, tt.expected, tt.reason)
			}
		})
	}
}

// =============================================================================
// Mock implementations for testing
// =============================================================================

type mockEventRepo struct {
	events                map[string]*domain.Event
	attempts              []*domain.DeliveryAttempt
	createBatchErr        error
	recordAttemptBatchErr error
}

func newMockEventRepo() *mockEventRepo {
	return &mockEventRepo{
		events:   make(map[string]*domain.Event),
		attempts: make([]*domain.DeliveryAttempt, 0),
	}
}

func (m *mockEventRepo) Create(ctx context.Context, event *domain.Event) error {
	m.events[event.ID] = event
	return nil
}

func (m *mockEventRepo) CreateBatch(ctx context.Context, events []*domain.Event) error {
	if m.createBatchErr != nil {
		return m.createBatchErr
	}
	for _, e := range events {
		m.events[e.ID] = e
	}
	return nil
}

func (m *mockEventRepo) GetByID(ctx context.Context, id string) (*domain.Event, error) {
	if e, ok := m.events[id]; ok {
		return e, nil
	}
	return nil, nil
}

func (m *mockEventRepo) GetPendingEvents(ctx context.Context, limit int) ([]*domain.Event, error) {
	return nil, nil
}

func (m *mockEventRepo) UpdateStatus(ctx context.Context, event *domain.Event) error {
	m.events[event.ID] = event
	return nil
}

func (m *mockEventRepo) UpdateStatusBatch(ctx context.Context, events []*domain.Event) error {
	for _, e := range events {
		m.events[e.ID] = e
	}
	return nil
}

func (m *mockEventRepo) RecordAttempt(ctx context.Context, attempt *domain.DeliveryAttempt) error {
	m.attempts = append(m.attempts, attempt)
	return nil
}

func (m *mockEventRepo) RecordAttemptBatch(ctx context.Context, attempts []*domain.DeliveryAttempt) error {
	if m.recordAttemptBatchErr != nil {
		return m.recordAttemptBatchErr
	}
	m.attempts = append(m.attempts, attempts...)
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
	subs               map[string][]*domain.Subscription
	getByEventTypesErr error
}

func newMockSubRepo() *mockSubRepo {
	return &mockSubRepo{
		subs: make(map[string][]*domain.Subscription),
	}
}

func (m *mockSubRepo) Create(ctx context.Context, sub *domain.Subscription) error {
	return nil
}

func (m *mockSubRepo) GetByID(ctx context.Context, id string) (*domain.Subscription, error) {
	return nil, nil
}

func (m *mockSubRepo) GetActive(ctx context.Context) ([]*domain.Subscription, error) {
	return nil, nil
}

func (m *mockSubRepo) GetByEventType(ctx context.Context, eventType string) ([]*domain.Subscription, error) {
	return m.subs[eventType], nil
}

func (m *mockSubRepo) GetByEventTypes(ctx context.Context, eventTypes []string) (map[string][]*domain.Subscription, error) {
	if m.getByEventTypesErr != nil {
		return nil, m.getByEventTypesErr
	}
	result := make(map[string][]*domain.Subscription)
	for _, et := range eventTypes {
		if subs, ok := m.subs[et]; ok {
			result[et] = subs
		}
	}
	return result, nil
}

func (m *mockSubRepo) Delete(ctx context.Context, id string) error {
	return nil
}

type mockRateLimiter struct {
	allowed bool
	err     error
}

func (m *mockRateLimiter) Allow(ctx context.Context, subscriptionID string) (bool, error) {
	return m.allowed, m.err
}

type mockCircuitBreaker struct {
	mu        sync.Mutex
	allowed   bool
	allowErr  error
	successes int
	failures  int
}

func (m *mockCircuitBreaker) Allow(ctx context.Context, subscriptionID string) (bool, error) {
	return m.allowed, m.allowErr
}

func (m *mockCircuitBreaker) RecordSuccess(ctx context.Context, subscriptionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.successes++
	return nil
}

func (m *mockCircuitBreaker) RecordFailure(ctx context.Context, subscriptionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures++
	return nil
}

func (m *mockCircuitBreaker) State(ctx context.Context, subscriptionID string) (resilience.CircuitState, error) {
	return resilience.CircuitStateClosed, nil
}

// =============================================================================
// Test helper functions
// =============================================================================

func newTestLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newTestHandler(t *testing.T, eventRepo *mockEventRepo, subRepo *mockSubRepo, rateLimiter *mockRateLimiter, circuitBreaker *mockCircuitBreaker) *DeliveryHandler {
	t.Helper()
	return &DeliveryHandler{
		config:         DefaultHandlerConfig(),
		eventRepo:      eventRepo,
		subRepo:        subRepo,
		httpClient:     &http.Client{Timeout: 5 * time.Second},
		retryPolicy:    retry.DefaultPolicy(),
		rateLimiter:    rateLimiter,
		circuitBreaker: circuitBreaker,
		logger:         newTestLogger(t),
	}
}

func newTestEvent(id, eventType string) *EventMessage {
	return &EventMessage{
		ID:          id,
		Type:        eventType,
		Source:      "test",
		Data:        json.RawMessage(`{"test": true}`),
		MaxAttempts: 5,
		Attempt:     0,
	}
}

// =============================================================================
// ProcessBatch Tests
// =============================================================================

func TestProcessBatch_EmptyBatch(t *testing.T) {
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	handler := newTestHandler(t, eventRepo, subRepo, nil, nil)

	successes, retries, failures := handler.ProcessBatch(context.Background(), nil)

	if len(successes) != 0 || len(retries) != 0 || len(failures) != 0 {
		t.Errorf("expected all empty slices for empty batch, got successes=%d, retries=%d, failures=%d",
			len(successes), len(retries), len(failures))
	}
}

func TestProcessBatch_SubscriptionLoadError(t *testing.T) {
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	subRepo.getByEventTypesErr = context.DeadlineExceeded

	handler := newTestHandler(t, eventRepo, subRepo, nil, nil)

	events := []*EventMessage{newTestEvent("evt-1", "order.created")}
	successes, retries, failures := handler.ProcessBatch(context.Background(), events)

	if len(successes) != 0 {
		t.Errorf("expected 0 successes, got %d", len(successes))
	}
	if len(retries) != 1 {
		t.Errorf("expected 1 retry (all events), got %d", len(retries))
	}
	if len(failures) != 0 {
		t.Errorf("expected 0 failures, got %d", len(failures))
	}
}

func TestProcessBatch_NoSubscriptions_MarksAsSuccess(t *testing.T) {
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	// No subscriptions registered

	handler := newTestHandler(t, eventRepo, subRepo, nil, nil)

	events := []*EventMessage{newTestEvent("evt-1", "order.created")}
	successes, retries, failures := handler.ProcessBatch(context.Background(), events)

	if len(successes) != 1 {
		t.Errorf("expected 1 success (no subscriptions = nothing to do), got %d", len(successes))
	}
	if len(retries) != 0 {
		t.Errorf("expected 0 retries, got %d", len(retries))
	}
	if len(failures) != 0 {
		t.Errorf("expected 0 failures, got %d", len(failures))
	}

	// Verify event was persisted as delivered
	if e, ok := eventRepo.events["evt-1"]; !ok {
		t.Error("expected event to be persisted")
	} else if e.Status != domain.EventStatusDelivered {
		t.Errorf("expected status delivered, got %s", e.Status)
	}
}

func TestProcessBatch_SuccessfulDelivery(t *testing.T) {
	// Start test HTTP server that returns 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "ok"}`))
	}))
	t.Cleanup(server.Close)

	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	subRepo.subs["order.created"] = []*domain.Subscription{
		{ID: "sub-1", URL: server.URL, EventTypes: []string{"order.created"}, RateLimit: 100, Active: true},
	}

	rateLimiter := &mockRateLimiter{allowed: true}
	circuitBreaker := &mockCircuitBreaker{allowed: true}

	handler := newTestHandler(t, eventRepo, subRepo, rateLimiter, circuitBreaker)

	events := []*EventMessage{newTestEvent("evt-1", "order.created")}
	successes, retries, failures := handler.ProcessBatch(context.Background(), events)

	if len(successes) != 1 {
		t.Errorf("expected 1 success, got %d", len(successes))
	}
	if len(retries) != 0 {
		t.Errorf("expected 0 retries, got %d", len(retries))
	}
	if len(failures) != 0 {
		t.Errorf("expected 0 failures, got %d", len(failures))
	}

	// Verify event was persisted as delivered
	if e, ok := eventRepo.events["evt-1"]; !ok {
		t.Error("expected event to be persisted")
	} else {
		if e.Status != domain.EventStatusDelivered {
			t.Errorf("expected status delivered, got %s", e.Status)
		}
		if e.DeliveredAt == nil {
			t.Error("expected DeliveredAt to be set")
		}
	}

	// Verify attempt was recorded
	if len(eventRepo.attempts) != 1 {
		t.Errorf("expected 1 attempt recorded, got %d", len(eventRepo.attempts))
	}

	// Verify circuit breaker recorded success
	if circuitBreaker.successes != 1 {
		t.Errorf("expected 1 circuit breaker success, got %d", circuitBreaker.successes)
	}
}

func TestProcessBatch_RetryableFailure(t *testing.T) {
	// Start test HTTP server that returns 503
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error": "service unavailable"}`))
	}))
	t.Cleanup(server.Close)

	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	subRepo.subs["order.created"] = []*domain.Subscription{
		{ID: "sub-1", URL: server.URL, EventTypes: []string{"order.created"}, RateLimit: 100, Active: true},
	}

	rateLimiter := &mockRateLimiter{allowed: true}
	circuitBreaker := &mockCircuitBreaker{allowed: true}

	handler := newTestHandler(t, eventRepo, subRepo, rateLimiter, circuitBreaker)

	events := []*EventMessage{newTestEvent("evt-1", "order.created")}
	successes, retries, failures := handler.ProcessBatch(context.Background(), events)

	if len(successes) != 0 {
		t.Errorf("expected 0 successes, got %d", len(successes))
	}
	if len(retries) != 1 {
		t.Errorf("expected 1 retry, got %d", len(retries))
	}
	if len(failures) != 0 {
		t.Errorf("expected 0 failures, got %d", len(failures))
	}

	// Verify event was persisted as retrying
	if e, ok := eventRepo.events["evt-1"]; !ok {
		t.Error("expected event to be persisted")
	} else {
		if e.Status != domain.EventStatusRetrying {
			t.Errorf("expected status retrying, got %s", e.Status)
		}
		if e.NextAttemptAt == nil {
			t.Error("expected NextAttemptAt to be set")
		}
		if e.LastError == nil {
			t.Error("expected LastError to be set")
		}
	}

	// Verify circuit breaker recorded failure
	if circuitBreaker.failures != 1 {
		t.Errorf("expected 1 circuit breaker failure, got %d", circuitBreaker.failures)
	}
}

func TestProcessBatch_PermanentFailure(t *testing.T) {
	// Start test HTTP server that returns 400
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": "bad request"}`))
	}))
	t.Cleanup(server.Close)

	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	subRepo.subs["order.created"] = []*domain.Subscription{
		{ID: "sub-1", URL: server.URL, EventTypes: []string{"order.created"}, RateLimit: 100, Active: true},
	}

	rateLimiter := &mockRateLimiter{allowed: true}
	circuitBreaker := &mockCircuitBreaker{allowed: true}

	handler := newTestHandler(t, eventRepo, subRepo, rateLimiter, circuitBreaker)

	events := []*EventMessage{newTestEvent("evt-1", "order.created")}
	successes, retries, failures := handler.ProcessBatch(context.Background(), events)

	if len(successes) != 0 {
		t.Errorf("expected 0 successes, got %d", len(successes))
	}
	if len(retries) != 0 {
		t.Errorf("expected 0 retries (permanent failure), got %d", len(retries))
	}
	if len(failures) != 1 {
		t.Errorf("expected 1 failure, got %d", len(failures))
	}

	// Verify event was persisted as failed
	if e, ok := eventRepo.events["evt-1"]; !ok {
		t.Error("expected event to be persisted")
	} else {
		if e.Status != domain.EventStatusFailed {
			t.Errorf("expected status failed, got %s", e.Status)
		}
	}
}

func TestProcessBatch_CircuitBreakerOpen(t *testing.T) {
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	subRepo.subs["order.created"] = []*domain.Subscription{
		{ID: "sub-1", URL: "http://example.com", EventTypes: []string{"order.created"}, RateLimit: 100, Active: true},
	}

	rateLimiter := &mockRateLimiter{allowed: true}
	circuitBreaker := &mockCircuitBreaker{allowed: false} // Circuit breaker is OPEN

	handler := newTestHandler(t, eventRepo, subRepo, rateLimiter, circuitBreaker)

	events := []*EventMessage{newTestEvent("evt-1", "order.created")}
	successes, retries, failures := handler.ProcessBatch(context.Background(), events)

	if len(successes) != 0 {
		t.Errorf("expected 0 successes, got %d", len(successes))
	}
	if len(retries) != 1 {
		t.Errorf("expected 1 retry (circuit open), got %d", len(retries))
	}
	if len(failures) != 0 {
		t.Errorf("expected 0 failures, got %d", len(failures))
	}

	// Verify event was persisted as retrying with circuit breaker error
	if e, ok := eventRepo.events["evt-1"]; !ok {
		t.Error("expected event to be persisted")
	} else {
		if e.Status != domain.EventStatusRetrying {
			t.Errorf("expected status retrying, got %s", e.Status)
		}
		if e.LastError == nil || *e.LastError != ErrCircuitOpen.Error() {
			t.Errorf("expected LastError to be circuit breaker open, got %v", e.LastError)
		}
	}
}

func TestProcessBatch_RateLimited(t *testing.T) {
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	subRepo.subs["order.created"] = []*domain.Subscription{
		{ID: "sub-1", URL: "http://example.com", EventTypes: []string{"order.created"}, RateLimit: 100, Active: true},
	}

	rateLimiter := &mockRateLimiter{allowed: false} // Rate limited
	circuitBreaker := &mockCircuitBreaker{allowed: true}

	handler := newTestHandler(t, eventRepo, subRepo, rateLimiter, circuitBreaker)

	events := []*EventMessage{newTestEvent("evt-1", "order.created")}
	successes, retries, failures := handler.ProcessBatch(context.Background(), events)

	if len(successes) != 0 {
		t.Errorf("expected 0 successes, got %d", len(successes))
	}
	if len(retries) != 1 {
		t.Errorf("expected 1 retry (rate limited), got %d", len(retries))
	}
	if len(failures) != 0 {
		t.Errorf("expected 0 failures, got %d", len(failures))
	}

	// Verify event was persisted as retrying with rate limit error
	if e, ok := eventRepo.events["evt-1"]; !ok {
		t.Error("expected event to be persisted")
	} else {
		if e.LastError == nil || *e.LastError != ErrRateLimited.Error() {
			t.Errorf("expected LastError to be rate limited, got %v", e.LastError)
		}
	}
}

func TestProcessBatch_ContextCancelled(t *testing.T) {
	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	subRepo.subs["order.created"] = []*domain.Subscription{
		{ID: "sub-1", URL: "http://example.com", EventTypes: []string{"order.created"}, RateLimit: 100, Active: true},
	}

	handler := newTestHandler(t, eventRepo, subRepo, nil, nil)

	// Cancel context before processing
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	events := []*EventMessage{newTestEvent("evt-1", "order.created")}
	successes, retries, failures := handler.ProcessBatch(ctx, events)

	if len(successes) != 0 {
		t.Errorf("expected 0 successes, got %d", len(successes))
	}
	if len(retries) != 1 {
		t.Errorf("expected 1 retry (context cancelled), got %d", len(retries))
	}
	if len(failures) != 0 {
		t.Errorf("expected 0 failures, got %d", len(failures))
	}
}

func TestProcessBatch_MultipleBatchEvents(t *testing.T) {
	// Server returns 200 for all requests
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	subRepo.subs["order.created"] = []*domain.Subscription{
		{ID: "sub-1", URL: server.URL, EventTypes: []string{"order.created"}, RateLimit: 100, Active: true},
	}
	subRepo.subs["order.updated"] = []*domain.Subscription{
		{ID: "sub-2", URL: server.URL, EventTypes: []string{"order.updated"}, RateLimit: 100, Active: true},
	}

	rateLimiter := &mockRateLimiter{allowed: true}
	circuitBreaker := &mockCircuitBreaker{allowed: true}

	handler := newTestHandler(t, eventRepo, subRepo, rateLimiter, circuitBreaker)

	events := []*EventMessage{
		newTestEvent("evt-1", "order.created"),
		newTestEvent("evt-2", "order.created"),
		newTestEvent("evt-3", "order.updated"),
	}
	successes, retries, failures := handler.ProcessBatch(context.Background(), events)

	if len(successes) != 3 {
		t.Errorf("expected 3 successes, got %d", len(successes))
	}
	if len(retries) != 0 {
		t.Errorf("expected 0 retries, got %d", len(retries))
	}
	if len(failures) != 0 {
		t.Errorf("expected 0 failures, got %d", len(failures))
	}

	// Verify all events were persisted
	if len(eventRepo.events) != 3 {
		t.Errorf("expected 3 events persisted, got %d", len(eventRepo.events))
	}
}

func TestProcessBatch_MaxRetriesExhausted(t *testing.T) {
	// Server returns 503 (retryable)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	eventRepo := newMockEventRepo()
	subRepo := newMockSubRepo()
	subRepo.subs["order.created"] = []*domain.Subscription{
		{ID: "sub-1", URL: server.URL, EventTypes: []string{"order.created"}, RateLimit: 100, Active: true},
	}

	rateLimiter := &mockRateLimiter{allowed: true}
	circuitBreaker := &mockCircuitBreaker{allowed: true}

	handler := newTestHandler(t, eventRepo, subRepo, rateLimiter, circuitBreaker)

	// Event already at max attempts
	event := newTestEvent("evt-1", "order.created")
	event.Attempt = 4 // Already tried 4 times, max is 5
	event.MaxAttempts = 5

	events := []*EventMessage{event}
	successes, retries, failures := handler.ProcessBatch(context.Background(), events)

	if len(successes) != 0 {
		t.Errorf("expected 0 successes, got %d", len(successes))
	}
	if len(retries) != 0 {
		t.Errorf("expected 0 retries (max exhausted), got %d", len(retries))
	}
	if len(failures) != 1 {
		t.Errorf("expected 1 failure, got %d", len(failures))
	}

	// Verify event was persisted as failed
	if e, ok := eventRepo.events["evt-1"]; !ok {
		t.Error("expected event to be persisted")
	} else {
		if e.Status != domain.EventStatusFailed {
			t.Errorf("expected status failed, got %s", e.Status)
		}
	}
}

// =============================================================================
// deliverWebhook Tests
// =============================================================================

func TestDeliverWebhook_Success(t *testing.T) {
	var receivedHeaders http.Header
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		receivedBody, _ = json.Marshal(map[string]interface{}{})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"received": true}`))
	}))
	t.Cleanup(server.Close)

	handler := newTestHandler(t, newMockEventRepo(), newMockSubRepo(), nil, nil)

	sub := &domain.Subscription{ID: "sub-1", URL: server.URL}
	event := newTestEvent("evt-1", "order.created")

	statusCode, respBody, err := handler.deliverWebhook(context.Background(), sub, event)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if statusCode == nil || *statusCode != 200 {
		t.Errorf("expected status 200, got %v", statusCode)
	}
	if respBody == "" {
		t.Error("expected response body")
	}

	// Verify headers were set
	if receivedHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", receivedHeaders.Get("Content-Type"))
	}
	if receivedHeaders.Get("X-Event-ID") != "evt-1" {
		t.Errorf("expected X-Event-ID evt-1, got %s", receivedHeaders.Get("X-Event-ID"))
	}
	if receivedHeaders.Get("X-Event-Type") != "order.created" {
		t.Errorf("expected X-Event-Type order.created, got %s", receivedHeaders.Get("X-Event-Type"))
	}

	_ = receivedBody
}

func TestDeliverWebhook_WithSecret(t *testing.T) {
	var receivedSignature string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSignature = r.Header.Get("X-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	handler := newTestHandler(t, newMockEventRepo(), newMockSubRepo(), nil, nil)

	secret := "my-secret"
	sub := &domain.Subscription{ID: "sub-1", URL: server.URL, Secret: &secret}
	event := newTestEvent("evt-1", "order.created")

	_, _, err := handler.deliverWebhook(context.Background(), sub, event)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if receivedSignature == "" {
		t.Error("expected X-Signature header to be set")
	}
}

func TestDeliverWebhook_Non2xxStatus(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{"400 Bad Request", 400},
		{"401 Unauthorized", 401},
		{"403 Forbidden", 403},
		{"404 Not Found", 404},
		{"500 Internal Server Error", 500},
		{"502 Bad Gateway", 502},
		{"503 Service Unavailable", 503},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(`{"error": "test error"}`))
			}))
			t.Cleanup(server.Close)

			handler := newTestHandler(t, newMockEventRepo(), newMockSubRepo(), nil, nil)

			sub := &domain.Subscription{ID: "sub-1", URL: server.URL}
			event := newTestEvent("evt-1", "order.created")

			statusCode, _, err := handler.deliverWebhook(context.Background(), sub, event)

			if err == nil {
				t.Error("expected error for non-2xx status")
			}
			if statusCode == nil || *statusCode != tt.statusCode {
				t.Errorf("expected status %d, got %v", tt.statusCode, statusCode)
			}
		})
	}
}

func TestDeliverWebhook_NetworkError(t *testing.T) {
	handler := newTestHandler(t, newMockEventRepo(), newMockSubRepo(), nil, nil)

	sub := &domain.Subscription{ID: "sub-1", URL: "http://localhost:99999"} // Invalid port
	event := newTestEvent("evt-1", "order.created")

	statusCode, _, err := handler.deliverWebhook(context.Background(), sub, event)

	if err == nil {
		t.Error("expected error for network failure")
	}
	if statusCode != nil {
		t.Errorf("expected nil status code for network error, got %v", statusCode)
	}
}

func TestDeliverWebhook_ContextTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // Slow response
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	handler := newTestHandler(t, newMockEventRepo(), newMockSubRepo(), nil, nil)
	handler.httpClient.Timeout = 100 * time.Millisecond // Short timeout

	sub := &domain.Subscription{ID: "sub-1", URL: server.URL}
	event := newTestEvent("evt-1", "order.created")

	_, _, err := handler.deliverWebhook(context.Background(), sub, event)

	if err == nil {
		t.Error("expected timeout error")
	}
}
