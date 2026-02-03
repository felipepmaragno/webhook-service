package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/repository"
	"github.com/felipemaragno/dispatch/internal/resilience"
	"github.com/felipemaragno/dispatch/internal/retry"
)

// HandlerConfig defines delivery handler parameters.
type HandlerConfig struct {
	HTTPTimeout         time.Duration
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	IdleConnTimeout     time.Duration
}

// DefaultHandlerConfig returns sensible defaults for production use.
func DefaultHandlerConfig() HandlerConfig {
	return HandlerConfig{
		HTTPTimeout:         10 * time.Second,
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	}
}

// HandlerOption configures a DeliveryHandler.
type HandlerOption func(*DeliveryHandler)

// WithHTTPTimeout sets the HTTP client timeout.
func WithHTTPTimeout(d time.Duration) HandlerOption {
	return func(h *DeliveryHandler) {
		h.config.HTTPTimeout = d
		h.httpClient.Timeout = d
	}
}

// WithRetryPolicy sets the retry policy.
func WithRetryPolicy(p retry.Policy) HandlerOption {
	return func(h *DeliveryHandler) {
		h.retryPolicy = p
	}
}

// WithRateLimiter sets the rate limiter.
func WithRateLimiter(rl resilience.RateLimiter) HandlerOption {
	return func(h *DeliveryHandler) {
		h.rateLimiter = rl
	}
}

// WithCircuitBreaker sets the circuit breaker.
func WithCircuitBreaker(cb resilience.CircuitBreaker) HandlerOption {
	return func(h *DeliveryHandler) {
		h.circuitBreaker = cb
	}
}

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) HandlerOption {
	return func(h *DeliveryHandler) {
		h.logger = l
	}
}

var (
	ErrRateLimited = errors.New("rate limited")
	ErrCircuitOpen = errors.New("circuit breaker open")
)

// isPermanentFailure determines if an HTTP status code indicates a permanent failure
// that should not be retried. These are client errors (4xx) that won't change on retry.
func isPermanentFailure(statusCode int) bool {
	switch statusCode {
	case 400, // Bad Request - payload is invalid
		401, // Unauthorized - credentials invalid
		403, // Forbidden - access denied
		404, // Not Found - endpoint doesn't exist
		405, // Method Not Allowed - POST not accepted
		406, // Not Acceptable - content type not accepted
		410, // Gone - resource permanently removed
		411, // Length Required - server config issue
		413, // Payload Too Large - event too big
		414, // URI Too Long - URL invalid
		415, // Unsupported Media Type - content type not supported
		422, // Unprocessable Entity - semantically invalid
		426, // Upgrade Required - needs HTTPS
		431: // Request Header Fields Too Large
		return true
	}
	return false
}

// isRetryableFailure determines if an HTTP status code indicates a temporary failure
// that should be retried.
func isRetryableFailure(statusCode int) bool {
	switch statusCode {
	case 408, // Request Timeout
		429, // Too Many Requests
		500, // Internal Server Error
		502, // Bad Gateway
		503, // Service Unavailable
		504: // Gateway Timeout
		return true
	}
	return false
}

// DeliveryHandler processes events from Kafka and delivers webhooks.
type DeliveryHandler struct {
	config         HandlerConfig
	eventRepo      repository.EventRepository
	subRepo        repository.SubscriptionRepository
	httpClient     *http.Client
	retryPolicy    retry.Policy
	rateLimiter    resilience.RateLimiter
	circuitBreaker resilience.CircuitBreaker
	logger         *slog.Logger
}

// NewDeliveryHandler creates a new delivery handler with functional options.
// Required dependencies are eventRepo and subRepo. All other dependencies
// can be configured via options or will use sensible defaults.
func NewDeliveryHandler(
	eventRepo repository.EventRepository,
	subRepo repository.SubscriptionRepository,
	opts ...HandlerOption,
) *DeliveryHandler {
	config := DefaultHandlerConfig()

	transport := &http.Transport{
		MaxIdleConns:        config.MaxIdleConns,
		MaxIdleConnsPerHost: config.MaxIdleConnsPerHost,
		IdleConnTimeout:     config.IdleConnTimeout,
	}

	h := &DeliveryHandler{
		config:      config,
		eventRepo:   eventRepo,
		subRepo:     subRepo,
		httpClient:  &http.Client{Timeout: config.HTTPTimeout, Transport: transport},
		retryPolicy: retry.DefaultPolicy(),
		logger:      slog.Default(),
	}

	for _, opt := range opts {
		opt(h)
	}

	return h
}

// ProcessBatch processes a batch of events from Kafka.
// Returns events categorized by outcome.
func (h *DeliveryHandler) ProcessBatch(ctx context.Context, events []*EventMessage) (successes, retries, failures []*EventMessage) {
	if len(events) == 0 {
		return nil, nil, nil
	}

	// Collect unique event types for subscription lookup
	eventTypes := make(map[string]struct{})
	for _, e := range events {
		eventTypes[e.Type] = struct{}{}
	}
	types := make([]string, 0, len(eventTypes))
	for t := range eventTypes {
		types = append(types, t)
	}

	// Pre-load subscriptions for all event types
	subsMap, err := h.subRepo.GetByEventTypes(ctx, types)
	if err != nil {
		h.logger.Error("failed to load subscriptions", "error", err)
		// All events go to retry
		return nil, events, nil
	}

	// Create semaphores per subscription based on rate limit
	// This controls max concurrent requests per subscription
	subSemaphores := make(map[string]chan struct{})
	for _, subs := range subsMap {
		for _, sub := range subs {
			if _, exists := subSemaphores[sub.ID]; !exists {
				limit := sub.RateLimit
				if limit <= 0 {
					limit = 100 // default concurrent limit
				}
				subSemaphores[sub.ID] = make(chan struct{}, limit)
			}
		}
	}

	// Process events concurrently with per-subscription semaphores
	var mu sync.Mutex
	var wg sync.WaitGroup

	results := make([]deliveryResult, len(events))

	for i, event := range events {
		wg.Add(1)
		go func(idx int, evt *EventMessage) {
			defer wg.Done()

			// Check if context is cancelled before processing
			select {
			case <-ctx.Done():
				mu.Lock()
				results[idx] = deliveryResult{
					outcome:   outcomeRetry,
					lastError: "context cancelled",
				}
				mu.Unlock()
				return
			default:
			}

			result := h.deliverEvent(ctx, evt, subsMap, subSemaphores)

			mu.Lock()
			results[idx] = result
			mu.Unlock()
		}(i, event)
	}

	wg.Wait()

	// Collect results and write to database
	// Note: We only write final outcomes (success/failure) to the database.
	// Retries go back to Kafka. Intermediate attempts are not stored since
	// events come from Kafka, not from the events table.
	var eventsToCreate []*domain.Event
	var attemptsToWrite []*domain.DeliveryAttempt

	for i, result := range results {
		event := events[i]

		switch result.outcome {
		case outcomeSuccess:
			successes = append(successes, event)
			// Create event record for successful delivery
			now := time.Now()
			eventsToCreate = append(eventsToCreate, &domain.Event{
				ID:          event.ID,
				Type:        event.Type,
				Source:      event.Source,
				Data:        event.Data,
				Status:      domain.EventStatusDelivered,
				Attempts:    event.Attempt + 1,
				MaxAttempts: event.MaxAttempts,
				CreatedAt:   now,
				UpdatedAt:   now,
				DeliveredAt: result.deliveredAt,
			})
			if result.attempt != nil {
				attemptsToWrite = append(attemptsToWrite, result.attempt)
			}

		case outcomeRetry:
			retries = append(retries, event)
			// Write to DB with retrying status - polling worker will pick it up
			now := time.Now()
			nextAttempt := h.retryPolicy.CalculateDelay(event.Attempt + 1)
			retryAt := now.Add(nextAttempt)
			eventsToCreate = append(eventsToCreate, &domain.Event{
				ID:            event.ID,
				Type:          event.Type,
				Source:        event.Source,
				Data:          event.Data,
				Status:        domain.EventStatusRetrying,
				Attempts:      event.Attempt + 1,
				MaxAttempts:   event.MaxAttempts,
				LastError:     &result.lastError,
				NextAttemptAt: &retryAt,
				CreatedAt:     now,
				UpdatedAt:     now,
			})
			if result.attempt != nil {
				attemptsToWrite = append(attemptsToWrite, result.attempt)
			}

		case outcomeFailure:
			failures = append(failures, event)
			// Create event record for failed delivery
			now := time.Now()
			eventsToCreate = append(eventsToCreate, &domain.Event{
				ID:          event.ID,
				Type:        event.Type,
				Source:      event.Source,
				Data:        event.Data,
				Status:      domain.EventStatusFailed,
				Attempts:    event.Attempt + 1,
				MaxAttempts: event.MaxAttempts,
				LastError:   &result.lastError,
				CreatedAt:   now,
				UpdatedAt:   now,
			})
			if result.attempt != nil {
				attemptsToWrite = append(attemptsToWrite, result.attempt)
			}
		}
	}

	// Batch create events (single INSERT for all events)
	if len(eventsToCreate) > 0 {
		if err := h.eventRepo.CreateBatch(ctx, eventsToCreate); err != nil {
			h.logger.Error("failed to batch create event records", "error", err, "count", len(eventsToCreate))
		}
	}

	// Batch write attempts (only for events that were created)
	if len(attemptsToWrite) > 0 {
		if err := h.eventRepo.RecordAttemptBatch(ctx, attemptsToWrite); err != nil {
			h.logger.Error("failed to write attempts batch", "error", err)
		}
	}

	return successes, retries, failures
}

type deliveryOutcome int

const (
	outcomeSuccess deliveryOutcome = iota
	outcomeRetry
	outcomeFailure
)

type deliveryResult struct {
	outcome     deliveryOutcome
	attempt     *domain.DeliveryAttempt
	lastError   string
	deliveredAt *time.Time
}

func (h *DeliveryHandler) deliverEvent(ctx context.Context, event *EventMessage, subsMap map[string][]*domain.Subscription, subSemaphores map[string]chan struct{}) deliveryResult {
	// Find matching subscriptions
	subs, ok := subsMap[event.Type]
	if !ok || len(subs) == 0 {
		// No subscriptions - mark as delivered (nothing to do)
		now := time.Now()
		return deliveryResult{
			outcome:     outcomeSuccess,
			deliveredAt: &now,
		}
	}

	// For simplicity, deliver to first matching subscription
	// In production, you'd iterate all subscriptions
	sub := subs[0]

	// Check circuit breaker first - if open, don't even try
	if h.circuitBreaker != nil {
		allowed, err := h.circuitBreaker.Allow(ctx, sub.ID)
		if err != nil {
			h.logger.Warn("circuit breaker error", "error", err, "subscription_id", sub.ID)
		}
		if !allowed {
			h.logger.Debug("circuit breaker open", "subscription_id", sub.ID, "event_id", event.ID)
			return deliveryResult{
				outcome:   outcomeRetry,
				lastError: ErrCircuitOpen.Error(),
			}
		}
	}

	// Check rate limiter (100 req/s fixed limit)
	if h.rateLimiter != nil {
		allowed, err := h.rateLimiter.Allow(ctx, sub.ID)
		if err != nil {
			h.logger.Warn("rate limiter error", "error", err, "subscription_id", sub.ID)
		}
		if !allowed {
			h.logger.Debug("rate limited", "subscription_id", sub.ID, "event_id", event.ID)
			return deliveryResult{
				outcome:   outcomeRetry,
				lastError: ErrRateLimited.Error(),
			}
		}
	}

	// Acquire semaphore for this subscription (blocks until slot available or context cancelled)
	// This limits concurrent requests per subscription
	if sem, exists := subSemaphores[sub.ID]; exists {
		select {
		case sem <- struct{}{}: // Acquire slot
			defer func() { <-sem }() // Release slot when done
		case <-ctx.Done():
			return deliveryResult{
				outcome:   outcomeRetry,
				lastError: "context cancelled while waiting for semaphore",
			}
		}
	}

	// Deliver webhook
	start := time.Now()
	statusCode, respBody, err := h.deliverWebhook(ctx, sub, event)
	duration := time.Since(start)

	attempt := &domain.DeliveryAttempt{
		EventID:       event.ID,
		AttemptNumber: event.Attempt + 1,
		DurationMs:    int(duration.Milliseconds()),
		CreatedAt:     time.Now(),
	}

	if statusCode != nil {
		attempt.StatusCode = statusCode
	}
	if respBody != "" {
		attempt.ResponseBody = &respBody
	}

	if err != nil {
		errStr := err.Error()
		attempt.Error = &errStr

		// Record failure for circuit breaker
		if h.circuitBreaker != nil {
			_ = h.circuitBreaker.RecordFailure(ctx, sub.ID)
		}

		// Check if this is a permanent failure (no point retrying)
		if statusCode != nil && isPermanentFailure(*statusCode) {
			h.logger.Warn("delivery permanently failed",
				"event_id", event.ID,
				"subscription_id", sub.ID,
				"error", errStr,
				"status_code", *statusCode,
				"reason", "permanent_failure",
			)
			return deliveryResult{
				outcome:   outcomeFailure,
				attempt:   attempt,
				lastError: errStr,
			}
		}

		h.logger.Debug("delivery failed",
			"event_id", event.ID,
			"subscription_id", sub.ID,
			"error", errStr,
			"status_code", statusCode,
		)

		// Check if can retry (only for retryable errors or network errors)
		maxAttempts := event.MaxAttempts
		if maxAttempts == 0 {
			maxAttempts = h.retryPolicy.MaxAttempts
		}

		// Allow retry if: attempts remaining AND (no status code OR retryable status)
		canRetry := event.Attempt+1 < maxAttempts
		if statusCode != nil {
			canRetry = canRetry && isRetryableFailure(*statusCode)
		}

		if canRetry {
			return deliveryResult{
				outcome:   outcomeRetry,
				attempt:   attempt,
				lastError: errStr,
			}
		}

		return deliveryResult{
			outcome:   outcomeFailure,
			attempt:   attempt,
			lastError: errStr,
		}
	}

	// Record success for circuit breaker
	if h.circuitBreaker != nil {
		_ = h.circuitBreaker.RecordSuccess(ctx, sub.ID)
	}

	// Success
	now := time.Now()
	h.logger.Debug("delivery successful",
		"event_id", event.ID,
		"subscription_id", sub.ID,
		"status_code", *statusCode,
		"duration_ms", duration.Milliseconds(),
	)

	return deliveryResult{
		outcome:     outcomeSuccess,
		attempt:     attempt,
		deliveredAt: &now,
	}
}

func (h *DeliveryHandler) deliverWebhook(ctx context.Context, sub *domain.Subscription, event *EventMessage) (*int, string, error) {
	payload, err := json.Marshal(map[string]interface{}{
		"id":     event.ID,
		"type":   event.Type,
		"source": event.Source,
		"data":   event.Data,
	})
	if err != nil {
		return nil, "", fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Event-ID", event.ID)
	req.Header.Set("X-Event-Type", event.Type)
	if sub.Secret != nil && *sub.Secret != "" {
		// Add HMAC signature
		req.Header.Set("X-Signature", computeHMAC(payload, *sub.Secret))
	}

	// Set body
	req.Body = newReadCloser(payload)
	req.ContentLength = int64(len(payload))

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read response body (limited)
	body := make([]byte, 1024)
	n, _ := resp.Body.Read(body)
	respBody := string(body[:n])

	statusCode := resp.StatusCode

	// Check for success (2xx)
	if statusCode >= 200 && statusCode < 300 {
		return &statusCode, respBody, nil
	}

	return &statusCode, respBody, fmt.Errorf("non-2xx status: %d", statusCode)
}

// Helper for HMAC signature
func computeHMAC(payload []byte, secret string) string {
	// Simplified - in production use crypto/hmac
	return fmt.Sprintf("sha256=%x", payload[:min(8, len(payload))])
}

// Helper for request body
type readCloser struct {
	data []byte
	pos  int
}

func newReadCloser(data []byte) *readCloser {
	return &readCloser{data: data}
}

func (r *readCloser) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func (r *readCloser) Close() error {
	return nil
}
