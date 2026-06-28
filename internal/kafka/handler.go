package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/observability"
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
	LeaseDuration       time.Duration
	InstanceID          string
}

// DefaultHandlerConfig returns sensible defaults for production use.
func DefaultHandlerConfig() HandlerConfig {
	return HandlerConfig{
		HTTPTimeout:         10 * time.Second,
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		LeaseDuration:       30 * time.Second,
		InstanceID:          "worker-1",
	}
}

// HandlerOption configures a DeliveryHandler.
type HandlerOption func(*DeliveryHandler)

type timeSource interface {
	Now() time.Time
}

type realTimeSource struct{}

func (realTimeSource) Now() time.Time {
	return time.Now()
}

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

// WithClaimIdentity sets the owner and lease duration used when claiming
// delivery rows initialized from Kafka messages.
func WithClaimIdentity(instanceID string, leaseDuration time.Duration) HandlerOption {
	return func(h *DeliveryHandler) {
		if instanceID != "" {
			h.config.InstanceID = instanceID
		}
		if leaseDuration > 0 {
			h.config.LeaseDuration = leaseDuration
		}
	}
}

// WithRateLimiter sets the rate limiter.
func WithRateLimiter(rl resilience.RateLimiter) HandlerOption {
	return func(h *DeliveryHandler) {
		h.rateLimiter = rl
	}
}

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) HandlerOption {
	return func(h *DeliveryHandler) {
		h.logger = l
	}
}

// DeliveryObserver receives delivery lifecycle observations.
type DeliveryObserver interface {
	Delivered()
	Failed()
	Retrying()
	Throttled()
	AttemptStarted()
	AttemptDuration(seconds float64)
	RateLimited(subscriptionID string)
}

type noopDeliveryObserver struct{}

func (noopDeliveryObserver) Delivered()              {}
func (noopDeliveryObserver) Failed()                 {}
func (noopDeliveryObserver) Retrying()               {}
func (noopDeliveryObserver) Throttled()              {}
func (noopDeliveryObserver) AttemptStarted()         {}
func (noopDeliveryObserver) AttemptDuration(float64) {}
func (noopDeliveryObserver) RateLimited(string)      {}

// WithDeliveryObserver sets a typed delivery lifecycle observer.
func WithDeliveryObserver(observer DeliveryObserver) HandlerOption {
	return func(h *DeliveryHandler) {
		if observer != nil {
			h.observer = observer
		}
	}
}

var (
	ErrRateLimited = errors.New("rate limited")
)

// HTTPDoer abstracts the http.Client.Do method for testing.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

type SubscriptionRepository interface {
	GetByEventTypes(ctx context.Context, eventTypes []string) (map[string][]*domain.Subscription, error)
}

// DeliveryHandler processes events from Kafka and delivers webhooks.
type DeliveryHandler struct {
	config      HandlerConfig
	eventRepo   repository.DeliveryRuntimeRepository
	subRepo     SubscriptionRepository
	httpClient  HTTPDoer
	retryPolicy retry.Policy
	rateLimiter resilience.RateLimiter
	logger      *slog.Logger
	observer    DeliveryObserver
	timeSource  timeSource
}

// recordDelivered increments the delivered counter if metrics are configured.
func (h *DeliveryHandler) recordDelivered() {
	h.observer.Delivered()
}

// recordFailed increments the failed counter if metrics are configured.
func (h *DeliveryHandler) recordFailed() {
	h.observer.Failed()
}

// recordRetrying increments the retrying counter if metrics are configured.
func (h *DeliveryHandler) recordRetrying() {
	h.observer.Retrying()
}

// recordThrottled increments the throttled counter if metrics are configured.
func (h *DeliveryHandler) recordThrottled() {
	h.observer.Throttled()
}

// recordAttemptDuration records delivery attempt duration if metrics are configured.
func (h *DeliveryHandler) recordAttemptDuration(seconds float64) {
	h.observer.AttemptDuration(seconds)
}

// recordRateLimited increments the rate-limiter rejection counter if metrics are configured.
func (h *DeliveryHandler) recordRateLimited(subID string) {
	h.observer.RateLimited(subID)
}

// recordAttempt increments the total delivery attempts counter if metrics are configured.
func (h *DeliveryHandler) recordAttempt() {
	h.observer.AttemptStarted()
}

// NewDeliveryHandler creates a new delivery handler with functional options.
// Required dependencies are eventRepo and subRepo. All other dependencies
// can be configured via options or will use sensible defaults.
func NewDeliveryHandler(
	eventRepo repository.DeliveryRuntimeRepository,
	subRepo SubscriptionRepository,
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
		observer:    noopDeliveryObserver{},
		timeSource:  realTimeSource{},
	}

	for _, opt := range opts {
		opt(h)
	}

	return h
}

// ProcessBatch processes a batch of events from Kafka.
// Returns events categorized by outcome.
func (h *DeliveryHandler) ProcessBatch(ctx context.Context, events []*EventMessage) (successes, retries, failures []*EventMessage, err error) {
	if len(events) == 0 {
		return nil, nil, nil, nil
	}

	eventsByID := make(map[string]*EventMessage, len(events))
	traceByEventID := make(map[string]string, len(events))
	eventIDs := make([]string, 0, len(events))
	totalDeliveries := 0
	for _, msg := range events {
		if msg.MaxAttempts == 0 {
			msg.MaxAttempts = h.retryPolicy.MaxAttempts
		}
		eventsByID[msg.ID] = msg
		if msg.TraceID != "" {
			traceByEventID[msg.ID] = msg.TraceID
		}
		eventIDs = append(eventIDs, msg.ID)
	}

	subsMap, err := h.subscriptionsForMessages(ctx, events)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load subscriptions for kafka events: %w", err)
	}

	for _, msg := range events {
		event := eventFromMessage(msg)
		deliveries, err := h.eventRepo.InitializeEventDeliveries(ctx, event, subsMap[msg.Type])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("initialize deliveries for event %s: %w", msg.ID, err)
		}
		totalDeliveries += len(deliveries)
	}

	if totalDeliveries > 0 {
		claimed, err := h.eventRepo.ClaimEventDeliveries(ctx, eventIDs, h.config.InstanceID, h.config.LeaseDuration, totalDeliveries)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("claim kafka deliveries: %w", err)
		}
		deliveries := make([]*domain.Delivery, 0, len(claimed))
		for _, claim := range claimed {
			deliveries = append(deliveries, claim.Delivery)
		}
		if _, _, _, err := h.processDeliveries(ctx, deliveries, traceByEventID); err != nil {
			return nil, nil, nil, fmt.Errorf("process kafka deliveries: %w", err)
		}
	}

	successes, retries, failures, err = h.categorizeMessagesFromDeliveries(ctx, events, eventsByID)
	if err != nil {
		return nil, nil, nil, err
	}
	return successes, retries, failures, nil
}

// ProcessDeliveries processes claimed delivery rows and persists each outcome
// with owner/deadline fencing. The retry poller uses this path directly.
func (h *DeliveryHandler) ProcessDeliveries(ctx context.Context, deliveries []*domain.Delivery) (delivered, retrying, failed []*domain.Delivery, err error) {
	return h.processDeliveries(ctx, deliveries, nil)
}

func (h *DeliveryHandler) processDeliveries(ctx context.Context, deliveries []*domain.Delivery, traceByEventID map[string]string) (delivered, retrying, failed []*domain.Delivery, err error) {
	if len(deliveries) == 0 {
		return nil, nil, nil, nil
	}

	for _, delivery := range deliveries {
		if delivery == nil {
			continue
		}
		deliveryCtx := ctx
		if traceID := traceByEventID[delivery.EventID]; traceID != "" {
			deliveryCtx = observability.ContextWithTraceID(ctx, traceID)
		}
		result := h.deliverDelivery(deliveryCtx, delivery, nil)
		h.applyDeliveryResult(delivery, result)

		switch delivery.Status {
		case domain.DeliveryStatusDelivered:
			delivered = append(delivered, delivery)
		case domain.DeliveryStatusRetrying, domain.DeliveryStatusThrottled:
			retrying = append(retrying, delivery)
		case domain.DeliveryStatusFailed:
			failed = append(failed, delivery)
		}

		if err := h.eventRepo.PersistClaimedDeliveryOutcome(ctx, delivery, result.attempts); err != nil {
			return delivered, retrying, failed, fmt.Errorf("persist delivery outcome %s: %w", delivery.ID, err)
		}
	}
	return delivered, retrying, failed, nil
}

func (h *DeliveryHandler) subscriptionsForMessages(ctx context.Context, events []*EventMessage) (map[string][]*domain.Subscription, error) {
	eventTypes := make(map[string]struct{})
	for _, e := range events {
		eventTypes[e.Type] = struct{}{}
	}
	types := make([]string, 0, len(eventTypes))
	for t := range eventTypes {
		types = append(types, t)
	}
	return h.subRepo.GetByEventTypes(ctx, types)
}

func eventFromMessage(msg *EventMessage) *domain.Event {
	now := time.Now()
	return &domain.Event{
		ID:          msg.ID,
		Type:        msg.Type,
		Source:      msg.Source,
		Data:        msg.Data,
		Status:      domain.EventStatusPending,
		Attempts:    msg.Attempt,
		MaxAttempts: msg.MaxAttempts,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func (h *DeliveryHandler) categorizeMessagesFromDeliveries(ctx context.Context, events []*EventMessage, eventsByID map[string]*EventMessage) (successes, retries, failures []*EventMessage, err error) {
	for _, msg := range events {
		deliveries, err := h.eventRepo.GetDeliveriesByEventID(ctx, msg.ID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("load deliveries for event %s: %w", msg.ID, err)
		}
		projection := domain.ProjectEventFromDeliveries(deliveries)
		event := eventsByID[msg.ID]
		switch projection.Status {
		case domain.EventStatusDelivered:
			successes = append(successes, event)
		case domain.EventStatusRetrying, domain.EventStatusThrottled, domain.EventStatusPending, domain.EventStatusProcessing:
			retries = append(retries, event)
		case domain.EventStatusFailed:
			failures = append(failures, event)
		}
	}
	return successes, retries, failures, nil
}

func (h *DeliveryHandler) applyDeliveryResult(delivery *domain.Delivery, result deliveryResult) {
	switch result.outcome {
	case outcomeSuccess:
		h.recordDelivered()
		if len(result.attempts) > 0 {
			delivery.Attempts++
		}
		deliveredAt := time.Now()
		if result.deliveredAt != nil {
			deliveredAt = *result.deliveredAt
		}
		delivery.MarkAsDelivered(deliveredAt)
	case outcomeRetry:
		h.recordRetrying()
		nextAttempt := time.Now().Add(result.retryDelay(h.retryPolicy, delivery.Attempts+1))
		delivery.MarkAsRetrying(nextAttempt, result.lastError)
	case outcomeThrottled:
		nextAttempt := time.Now().Add(result.retryDelay(h.retryPolicy, delivery.Attempts+1))
		delivery.MarkAsThrottled(nextAttempt, result.lastError)
	case outcomeFailure:
		h.recordFailed()
		if len(result.attempts) > 0 {
			delivery.Attempts++
		}
		delivery.MarkAsFailed(result.lastError)
	}
}

func (r deliveryResult) retryDelay(policy retry.Policy, attempt int) time.Duration {
	if r.retryAfter > 0 {
		return r.retryAfter
	}
	return policy.CalculateDelay(attempt)
}
