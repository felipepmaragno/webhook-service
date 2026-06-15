package kafka

import (
	"context"
	"errors"
	"fmt"
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
// Note: This creates a new http.Client with the specified timeout.
func WithHTTPTimeout(d time.Duration) HandlerOption {
	return func(h *DeliveryHandler) {
		h.config.HTTPTimeout = d
		// Create new client with updated timeout
		h.httpClient = &http.Client{
			Timeout: d,
			Transport: &http.Transport{
				MaxIdleConns:        h.config.MaxIdleConns,
				MaxIdleConnsPerHost: h.config.MaxIdleConnsPerHost,
				IdleConnTimeout:     h.config.IdleConnTimeout,
			},
		}
	}
}

// WithHTTPClient sets a custom HTTP client (useful for testing).
func WithHTTPClient(client HTTPDoer) HandlerOption {
	return func(h *DeliveryHandler) {
		h.httpClient = client
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

// WithSemaphore sets the distributed semaphore for concurrency control.
// When set, this replaces the local per-batch semaphores with a distributed
// implementation that coordinates across all worker instances.
func WithSemaphore(sem resilience.Semaphore) HandlerOption {
	return func(h *DeliveryHandler) {
		h.semaphore = sem
	}
}

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) HandlerOption {
	return func(h *DeliveryHandler) {
		h.logger = l
	}
}

// WithMetrics sets delivery metrics callbacks.
// This allows integration with any metrics system (Prometheus, StatsD, etc.)
func WithMetrics(delivered, failed, retrying, throttled func(), duration func(float64)) HandlerOption {
	return func(h *DeliveryHandler) {
		h.metrics = &deliveryMetrics{
			deliveredTotal:  delivered,
			failedTotal:     failed,
			retryingTotal:   retrying,
			throttledTotal:  throttled,
			attemptDuration: duration,
		}
	}
}

// WithExtraMetrics sets additional delivery metrics callbacks for rate limiter
// rejections and total delivery attempts.
// WithExtraMetrics sets additional delivery metrics callbacks.
// rateLimited is called with the subscription ID on every rate-limiter rejection.
// attempts is called before every HTTP delivery attempt (no subscription ID needed
// at that point because the counter is a simple total).
func WithExtraMetrics(rateLimited func(subID string), attempts func()) HandlerOption {
	return func(h *DeliveryHandler) {
		if h.metrics == nil {
			h.metrics = &deliveryMetrics{}
		}
		h.metrics.rateLimitedTotal = rateLimited
		h.metrics.attemptsTotal = attempts
	}
}

// WithRateLimiterDegradedMetric records rate-limiter decisions served by degraded
// local fallback. The metric has no subscription labels to avoid high cardinality.
func WithRateLimiterDegradedMetric(degraded func()) HandlerOption {
	return func(h *DeliveryHandler) {
		if h.metrics == nil {
			h.metrics = &deliveryMetrics{}
		}
		h.metrics.rateLimiterDegradedTotal = degraded
	}
}

// WithCircuitBreakerMetrics wires a state-change callback into the circuit
// breaker so Prometheus gauges and trip counters are updated on transitions.
// stateGauge receives (subscriptionID, newState) where state is "closed"=0,
// "half-open"=1, "open"=2. tripCounter is called only on closed→open.
func WithCircuitBreakerMetrics(stateGauge func(subscriptionID, state string), tripCounter func(subscriptionID string)) HandlerOption {
	return func(h *DeliveryHandler) {
		h.cbStateGauge = stateGauge
		h.cbTripCounter = tripCounter
	}
}

var (
	ErrRateLimited = errors.New("rate limited")
	ErrCircuitOpen = errors.New("circuit breaker open")
)

// HTTPDoer abstracts the http.Client.Do method for testing.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// DeliveryHandler processes events from Kafka and delivers webhooks.
type DeliveryHandler struct {
	config         HandlerConfig
	eventRepo      repository.EventRepository
	subRepo        repository.SubscriptionRepository
	httpClient     HTTPDoer
	retryPolicy    retry.Policy
	rateLimiter    resilience.RateLimiter
	circuitBreaker resilience.CircuitBreaker
	semaphore      resilience.Semaphore // Distributed semaphore for concurrency control
	logger         *slog.Logger
	metrics        *deliveryMetrics
	// Circuit breaker observability callbacks (set via WithCircuitBreakerMetrics).
	cbStateGauge  func(subscriptionID, state string)
	cbTripCounter func(subscriptionID string)
}

// deliveryMetrics holds optional Prometheus metrics for delivery tracking.
type deliveryMetrics struct {
	deliveredTotal           func()
	failedTotal              func()
	retryingTotal            func()
	throttledTotal           func()
	attemptDuration          func(float64)
	rateLimitedTotal         func(subID string) // incremented on every rate-limiter rejection
	attemptsTotal            func()             // incremented before every HTTP delivery attempt
	rateLimiterDegradedTotal func()             // incremented when Redis rate limiting falls back locally
}

// recordDelivered increments the delivered counter if metrics are configured.
func (h *DeliveryHandler) recordDelivered() {
	if h.metrics != nil && h.metrics.deliveredTotal != nil {
		h.metrics.deliveredTotal()
	}
}

// recordFailed increments the failed counter if metrics are configured.
func (h *DeliveryHandler) recordFailed() {
	if h.metrics != nil && h.metrics.failedTotal != nil {
		h.metrics.failedTotal()
	}
}

// recordRetrying increments the retrying counter if metrics are configured.
func (h *DeliveryHandler) recordRetrying() {
	if h.metrics != nil && h.metrics.retryingTotal != nil {
		h.metrics.retryingTotal()
	}
}

// recordThrottled increments the throttled counter if metrics are configured.
func (h *DeliveryHandler) recordThrottled() {
	if h.metrics != nil && h.metrics.throttledTotal != nil {
		h.metrics.throttledTotal()
	}
}

// recordAttemptDuration records delivery attempt duration if metrics are configured.
func (h *DeliveryHandler) recordAttemptDuration(seconds float64) {
	if h.metrics != nil && h.metrics.attemptDuration != nil {
		h.metrics.attemptDuration(seconds)
	}
}

// recordRateLimited increments the rate-limiter rejection counter if metrics are configured.
func (h *DeliveryHandler) recordRateLimited(subID string) {
	if h.metrics != nil && h.metrics.rateLimitedTotal != nil {
		h.metrics.rateLimitedTotal(subID)
	}
}

// recordRateLimiterDegraded increments the degraded-mode counter if metrics are configured.
func (h *DeliveryHandler) recordRateLimiterDegraded() {
	if h.metrics != nil && h.metrics.rateLimiterDegradedTotal != nil {
		h.metrics.rateLimiterDegradedTotal()
	}
}

// recordAttempt increments the total delivery attempts counter if metrics are configured.
func (h *DeliveryHandler) recordAttempt() {
	if h.metrics != nil && h.metrics.attemptsTotal != nil {
		h.metrics.attemptsTotal()
	}
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

	// If circuit-breaker metrics callbacks were provided AND the circuit breaker
	// implements StateChangeNotifier, wire them up now (after all options are set).
	if (h.cbStateGauge != nil || h.cbTripCounter != nil) && h.circuitBreaker != nil {
		if notifier, ok := h.circuitBreaker.(resilience.StateChangeNotifier); ok {
			stateGauge := h.cbStateGauge
			tripCounter := h.cbTripCounter
			notifier.OnStateChange(func(subID string, from, to resilience.CircuitState) {
				if stateGauge != nil {
					stateGauge(subID, string(to))
				}
				if tripCounter != nil && to == resilience.CircuitStateOpen {
					tripCounter(subID)
				}
			})
		}
	}

	return h
}

// ProcessEvents processes events from the database (for retry polling).
// This method converts domain.Event to EventMessage and reuses the delivery logic.
// Returns events categorized by outcome.
func (h *DeliveryHandler) ProcessEvents(ctx context.Context, events []*domain.Event) (delivered, retrying, failed []*domain.Event, err error) {
	if len(events) == 0 {
		return nil, nil, nil, nil
	}

	// Convert domain.Event to EventMessage
	messages := make([]*EventMessage, len(events))
	eventMap := make(map[string]*domain.Event, len(events))
	for i, e := range events {
		messages[i] = &EventMessage{
			ID:          e.ID,
			Type:        e.Type,
			Source:      e.Source,
			Data:        e.Data,
			MaxAttempts: e.MaxAttempts,
			Attempt:     e.Attempts,
		}
		if e.LastError != nil {
			messages[i].LastError = *e.LastError
		}
		eventMap[e.ID] = e
	}

	results := h.processBatchResults(ctx, messages)
	successes, retries, failures := categorizeBatchOutcomes(messages, results)

	outcomes := make([]repository.EventOutcome, 0, len(events))

	for i, result := range results {
		event := events[i]

		switch result.outcome {
		case outcomeSuccess:
			h.recordDelivered()
			event.Attempts++
			deliveredAt := time.Now()
			if result.deliveredAt != nil {
				deliveredAt = *result.deliveredAt
			}
			event.MarkAsDelivered(deliveredAt)
			event.NextAttemptAt = nil
			event.LastError = nil
		case outcomeRetry:
			h.recordRetrying()
			nextAttempt := time.Now().Add(result.retryDelay(h.retryPolicy, event.Attempts+1))
			event.MarkAsRetrying(nextAttempt, result.lastError)
		case outcomeThrottled:
			nextAttempt := time.Now().Add(result.retryDelay(h.retryPolicy, event.Attempts+1))
			event.MarkAsThrottled(nextAttempt)
			event.LastError = &result.lastError
		case outcomeFailure:
			h.recordFailed()
			event.Attempts++
			event.MarkAsFailed(result.lastError)
		}
		outcomes = append(outcomes, repository.EventOutcome{Event: event, Attempts: result.attempts})
	}

	// Convert back to domain.Event
	for _, msg := range successes {
		if e, ok := eventMap[msg.ID]; ok {
			delivered = append(delivered, e)
		}
	}
	for _, msg := range retries {
		if e, ok := eventMap[msg.ID]; ok {
			retrying = append(retrying, e)
		}
	}
	for _, msg := range failures {
		if e, ok := eventMap[msg.ID]; ok {
			failed = append(failed, e)
		}
	}

	if err := h.eventRepo.PersistClaimedOutcomes(ctx, outcomes); err != nil {
		return delivered, retrying, failed, fmt.Errorf("persist retry outcomes: %w", err)
	}

	return delivered, retrying, failed, nil
}

// ProcessBatch processes a batch of events from Kafka.
// Returns events categorized by outcome.
func (h *DeliveryHandler) ProcessBatch(ctx context.Context, events []*EventMessage) (successes, retries, failures []*EventMessage, err error) {
	if len(events) == 0 {
		return nil, nil, nil, nil
	}

	results := h.processBatchResults(ctx, events)
	successes, retries, failures = categorizeBatchOutcomes(events, results)

	// Collect each event outcome with the attempts produced while delivering it.
	// Kafka-originated events create their durable state here; retryable outcomes
	// are then scheduled from PostgreSQL by the retry poller.
	outcomes := make([]repository.EventOutcome, 0, len(events))

	for i, result := range results {
		event := events[i]

		switch result.outcome {
		case outcomeSuccess:
			h.recordDelivered()
			// Create event record for successful delivery
			now := time.Now()
			persistedEvent := &domain.Event{
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
			}
			outcomes = append(outcomes, repository.EventOutcome{Event: persistedEvent, Attempts: result.attempts})

		case outcomeRetry:
			h.recordRetrying()
			// Write to DB with retrying status - polling worker will pick it up
			now := time.Now()
			nextAttempt := result.retryDelay(h.retryPolicy, event.Attempt+1)
			retryAt := now.Add(nextAttempt)
			persistedEvent := &domain.Event{
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
			}
			outcomes = append(outcomes, repository.EventOutcome{Event: persistedEvent, Attempts: result.attempts})

		case outcomeThrottled:
			// Pre-HTTP capacity decisions are persisted for retry polling without
			// consuming delivery-attempt budget or writing delivery_attempt rows.
			now := time.Now()
			nextAttempt := result.retryDelay(h.retryPolicy, event.Attempt+1)
			retryAt := now.Add(nextAttempt)
			persistedEvent := &domain.Event{
				ID:            event.ID,
				Type:          event.Type,
				Source:        event.Source,
				Data:          event.Data,
				Status:        domain.EventStatusThrottled,
				Attempts:      event.Attempt,
				MaxAttempts:   event.MaxAttempts,
				LastError:     &result.lastError,
				NextAttemptAt: &retryAt,
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			outcomes = append(outcomes, repository.EventOutcome{Event: persistedEvent})

		case outcomeFailure:
			h.recordFailed()
			// Create event record for failed delivery
			now := time.Now()
			persistedEvent := &domain.Event{
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
			}
			outcomes = append(outcomes, repository.EventOutcome{Event: persistedEvent, Attempts: result.attempts})
		}
	}

	if err := h.eventRepo.PersistNewOutcomes(ctx, outcomes); err != nil {
		return successes, retries, failures, fmt.Errorf("persist kafka outcomes: %w", err)
	}

	return successes, retries, failures, nil
}

func (h *DeliveryHandler) processBatchResults(ctx context.Context, events []*EventMessage) []deliveryResult {
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
		results := make([]deliveryResult, len(events))
		for i := range events {
			results[i] = deliveryResult{
				outcome:   outcomeRetry,
				lastError: "failed to load subscriptions",
			}
		}
		return results
	}

	// Create semaphores per subscription based on concurrency limit.
	// This controls max concurrent requests per subscription
	subSemaphores := make(map[string]chan struct{})
	for _, subs := range subsMap {
		for _, sub := range subs {
			if _, exists := subSemaphores[sub.ID]; !exists {
				subSemaphores[sub.ID] = make(chan struct{}, sub.EffectiveConcurrencyLimit())
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

			// Inject trace ID from Kafka header into context for log correlation.
			evtCtx := injectTraceID(ctx, evt)

			result := h.deliverEvent(evtCtx, evt, subsMap, subSemaphores)

			mu.Lock()
			results[idx] = result
			mu.Unlock()
		}(i, event)
	}

	wg.Wait()
	return results
}

func categorizeBatchOutcomes(events []*EventMessage, results []deliveryResult) (successes, retries, failures []*EventMessage) {
	for i, result := range results {
		switch result.outcome {
		case outcomeSuccess:
			successes = append(successes, events[i])
		case outcomeRetry, outcomeThrottled:
			retries = append(retries, events[i])
		case outcomeFailure:
			failures = append(failures, events[i])
		}
	}
	return successes, retries, failures
}

func (r deliveryResult) retryDelay(policy retry.Policy, attempt int) time.Duration {
	if r.retryAfter > 0 {
		return r.retryAfter
	}
	return policy.CalculateDelay(attempt)
}
