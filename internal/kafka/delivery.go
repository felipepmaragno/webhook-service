package kafka

import (
	"context"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
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

func (h *DeliveryHandler) deliverDelivery(ctx context.Context, delivery *domain.Delivery, _ map[string]chan struct{}) deliveryResult {
	event := &EventMessage{
		ID:          delivery.EventID,
		Type:        delivery.EventType,
		Source:      delivery.Source,
		Data:        delivery.Data,
		MaxAttempts: delivery.MaxAttempts,
		Attempt:     delivery.Attempts,
	}
	sub := subscriptionFromDelivery(delivery)
	result := h.deliverToSubscription(ctx, event, sub)
	if result.attempt == nil {
		return deliveryResult{
			outcome:    result.outcome,
			lastError:  result.lastError,
			retryAfter: result.retryAfter,
		}
	}
	result.attempt.DeliveryID = delivery.ID
	result.attempt.SubscriptionID = delivery.SubscriptionID
	result.attempt.Generation = delivery.Generation
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
		ID:              delivery.SubscriptionID,
		URL:             delivery.SubscriptionURL,
		Secret:          delivery.SubscriptionSecret,
		EventTypes:      []string{delivery.EventType},
		MaxDeliveryRate: delivery.MaxDeliveryRate,
		Active:          true,
	}
}

// subDeliveryResult holds the outcome of delivering to a single subscription.
type subDeliveryResult struct {
	outcome     deliveryOutcome
	attempt     *domain.DeliveryAttempt
	lastError   string
	deliveredAt *time.Time
	retryAfter  time.Duration
}

// deliverToSubscription handles delivery to a single subscription.
func (h *DeliveryHandler) deliverToSubscription(ctx context.Context, event *EventMessage, sub *domain.Subscription) subDeliveryResult {
	// Check rate limiter before starting an HTTP attempt.
	if h.rateLimiter != nil {
		decision, err := h.rateLimiter.Allow(ctx, sub.ID, sub.EffectiveRatePolicy())
		if err != nil {
			h.logger.Warn("rate limiter error", "error", err, "subscription_id", sub.ID)
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
