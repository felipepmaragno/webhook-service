package kafka

import (
	"context"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/observability"
)

type deliveryOutcome int

const (
	outcomeSuccess deliveryOutcome = iota
	outcomeThrottled
	outcomeRetry
	outcomeFailure
)

type deliveryResult struct {
	outcome     deliveryOutcome
	attempts    []*domain.DeliveryAttempt
	lastError   string
	deliveredAt *time.Time
	retryAfter  time.Duration
}

// deliverEvent delivers an event to ALL matching subscriptions (fan-out).
// The aggregated outcome uses "worst wins": failure > retry > throttled > success.
// All per-subscription delivery attempts are collected.
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

	// Deliver to every matching subscription and collect results
	var (
		worstOutcome = outcomeSuccess
		lastError    string
		attempts     []*domain.DeliveryAttempt
		deliveredAt  *time.Time
		retryAfter   time.Duration
	)

	for _, sub := range subs {
		subResult := h.deliverToSubscription(ctx, event, sub, subSemaphores)

		if subResult.attempt != nil {
			attempts = append(attempts, subResult.attempt)
		}

		// Aggregate: worst outcome wins (failure > retry > success)
		if subResult.outcome > worstOutcome {
			worstOutcome = subResult.outcome
			lastError = subResult.lastError
			retryAfter = subResult.retryAfter
		} else if subResult.outcome == worstOutcome && subResult.lastError != "" {
			lastError = subResult.lastError
			if retryAfter == 0 {
				retryAfter = subResult.retryAfter
			}
		}

		if subResult.deliveredAt != nil && deliveredAt == nil {
			deliveredAt = subResult.deliveredAt
		}
	}

	return deliveryResult{
		outcome:     worstOutcome,
		attempts:    attempts,
		lastError:   lastError,
		deliveredAt: deliveredAt,
		retryAfter:  retryAfter,
	}
}

func (h *DeliveryHandler) deliverDelivery(ctx context.Context, delivery *domain.Delivery, subSemaphores map[string]chan struct{}) deliveryResult {
	event := &EventMessage{
		ID:          delivery.EventID,
		Type:        delivery.EventType,
		Source:      delivery.Source,
		Data:        delivery.Data,
		MaxAttempts: delivery.MaxAttempts,
		Attempt:     delivery.Attempts,
	}
	sub := subscriptionFromDelivery(delivery)
	result := h.deliverToSubscription(ctx, event, sub, subSemaphores)
	if result.attempt == nil {
		return deliveryResult{
			outcome:    result.outcome,
			lastError:  result.lastError,
			retryAfter: result.retryAfter,
		}
	}
	deliveryID := delivery.ID
	subscriptionID := delivery.SubscriptionID
	result.attempt.DeliveryID = &deliveryID
	result.attempt.SubscriptionID = &subscriptionID
	return deliveryResult{
		outcome:     result.outcome,
		attempts:    []*domain.DeliveryAttempt{result.attempt},
		lastError:   result.lastError,
		deliveredAt: result.deliveredAt,
		retryAfter:  result.retryAfter,
	}
}

func subscriptionFromDelivery(delivery *domain.Delivery) *domain.Subscription {
	return &domain.Subscription{
		ID:               delivery.SubscriptionID,
		URL:              delivery.SubscriptionURL,
		Secret:           delivery.SubscriptionSecret,
		EventTypes:       []string{delivery.EventType},
		RateLimit:        delivery.RateLimit,
		BurstSize:        delivery.BurstSize,
		ConcurrencyLimit: delivery.ConcurrencyLimit,
		Active:           true,
	}
}

func effectiveDeliveryConcurrency(delivery *domain.Delivery) int {
	if delivery.ConcurrencyLimit > 0 {
		return delivery.ConcurrencyLimit
	}
	return 100
}

// subDeliveryResult holds the outcome of delivering to a single subscription.
type subDeliveryResult struct {
	outcome     deliveryOutcome
	attempt     *domain.DeliveryAttempt
	lastError   string
	deliveredAt *time.Time
	retryAfter  time.Duration
}

// deliverToSubscription handles delivery to a single subscription, including
// circuit breaker, rate limiter, semaphore checks, and the actual HTTP call.
func (h *DeliveryHandler) deliverToSubscription(ctx context.Context, event *EventMessage, sub *domain.Subscription, subSemaphores map[string]chan struct{}) subDeliveryResult {
	// Check circuit breaker first - if open, don't even try
	if h.circuitBreaker != nil {
		allowed, err := h.circuitBreaker.Allow(ctx, sub.ID)
		if err != nil {
			h.logger.Warn("circuit breaker error", "error", err, "subscription_id", sub.ID)
		}
		if !allowed {
			h.logger.Debug("circuit breaker open", "subscription_id", sub.ID, "event_id", event.ID)
			h.recordThrottled()
			return subDeliveryResult{
				outcome:   outcomeThrottled,
				lastError: ErrCircuitOpen.Error(),
			}
		}
	}

	// Check rate limiter before starting an HTTP attempt.
	if h.rateLimiter != nil {
		decision, err := h.rateLimiter.Allow(ctx, sub.ID, sub.EffectiveRatePolicy())
		if err != nil {
			h.logger.Warn("rate limiter error", "error", err, "subscription_id", sub.ID)
		}
		if decision.Degraded {
			h.recordRateLimiterDegraded()
		}
		if !decision.Allowed {
			h.logger.Debug("rate limited", "subscription_id", sub.ID, "event_id", event.ID)
			h.recordThrottled()
			h.recordRateLimited(sub.ID)
			return subDeliveryResult{
				outcome:    outcomeThrottled,
				lastError:  ErrRateLimited.Error(),
				retryAfter: decision.RetryAfter,
			}
		}
	}

	// Acquire semaphore for this subscription
	// This limits concurrent requests per subscription across all workers
	if h.semaphore != nil {
		// Use distributed semaphore
		acquired, err := h.semaphore.Acquire(ctx, sub.ID, sub.EffectiveConcurrencyLimit())
		if err != nil {
			h.logger.Warn("semaphore acquire error", "error", err, "subscription_id", sub.ID)
		}
		if !acquired {
			h.logger.Debug("semaphore full", "subscription_id", sub.ID, "event_id", event.ID)
			h.recordThrottled()
			return subDeliveryResult{
				outcome:   outcomeThrottled,
				lastError: "concurrency limit reached",
			}
		}
		defer func() {
			if err := h.semaphore.Release(ctx, sub.ID); err != nil {
				h.logger.Warn("semaphore release error", "error", err, "subscription_id", sub.ID)
			}
		}()
	} else if sem, exists := subSemaphores[sub.ID]; exists {
		// Fallback to local semaphore
		select {
		case sem <- struct{}{}: // Acquire slot
			defer func() { <-sem }() // Release slot when done
		case <-ctx.Done():
			return subDeliveryResult{
				outcome:   outcomeThrottled,
				lastError: "context cancelled while waiting for semaphore",
			}
		}
	}

	// Deliver webhook
	h.recordAttempt()
	start := time.Now()
	statusCode, respBody, err := h.deliverWebhook(ctx, sub, event)
	duration := time.Since(start)
	h.recordAttemptDuration(duration.Seconds())

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
			return subDeliveryResult{
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
			return subDeliveryResult{
				outcome:   outcomeRetry,
				attempt:   attempt,
				lastError: errStr,
			}
		}

		return subDeliveryResult{
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

	return subDeliveryResult{
		outcome:     outcomeSuccess,
		attempt:     attempt,
		deliveredAt: &now,
	}
}

// injectTraceID creates a new context with the event's trace ID for log correlation.
func injectTraceID(ctx context.Context, evt *EventMessage) context.Context {
	if evt.TraceID != "" {
		return observability.ContextWithTraceID(ctx, evt.TraceID)
	}
	return ctx
}
