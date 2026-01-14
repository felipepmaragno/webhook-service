// Package worker implements the webhook delivery engine.
//
// Architecture:
//
//	┌─────────────┐     ┌─────────────┐     ┌─────────────┐
//	│   Worker 1  │     │   Worker 2  │     │   Worker N  │
//	└──────┬──────┘     └──────┬──────┘     └──────┬──────┘
//	       │                   │                   │
//	       └───────────────────┼───────────────────┘
//	                           │
//	                    ┌──────▼──────┐
//	                    │  Event Repo │  (FOR UPDATE SKIP LOCKED)
//	                    └──────┬──────┘
//	                           │
//	                    ┌──────▼──────┐
//	                    │  PostgreSQL │
//	                    └─────────────┘
//
// The Pool manages a configurable number of worker goroutines that:
//  1. Poll the database for pending events using FOR UPDATE SKIP LOCKED
//  2. Apply rate limiting and circuit breaker checks per destination
//  3. Deliver webhooks with HMAC-SHA256 signatures
//  4. Handle retries with exponential backoff and jitter
//  5. Record delivery attempts and update event status
//
// Concurrency is safe because each worker claims events atomically,
// preventing duplicate deliveries even with multiple instances.
package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/felipemaragno/dispatch/internal/clock"
	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/observability"
	"github.com/felipemaragno/dispatch/internal/repository"
	"github.com/felipemaragno/dispatch/internal/resilience"
	"github.com/felipemaragno/dispatch/internal/retry"
)

// HTTPClient abstracts HTTP operations for testability.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Config defines worker pool parameters.
//
// Workers: Number of concurrent delivery goroutines.
// PollInterval: How often to check for new events.
// BatchSize: Maximum events to fetch per poll.
// Timeout: HTTP request timeout for webhook delivery.
type Config struct {
	Workers      int
	PollInterval time.Duration
	BatchSize    int
	Timeout      time.Duration
}

func DefaultConfig() Config {
	return Config{
		Workers:      10,
		PollInterval: 100 * time.Millisecond,
		BatchSize:    10,
		Timeout:      30 * time.Second,
	}
}

// Pool manages worker goroutines for webhook delivery.
// Use NewPool to create, then call Start to begin processing.
// Call Stop for graceful shutdown.
type Pool struct {
	config      Config
	eventRepo   repository.EventRepository
	subRepo     repository.SubscriptionRepository
	httpClient  HTTPClient
	clock       clock.Clock
	retryPolicy retry.Policy
	logger      *slog.Logger
	metrics     *observability.Metrics

	rateLimiter    resilience.RateLimiter
	circuitBreaker resilience.CircuitBreaker

	wg     sync.WaitGroup
	cancel context.CancelFunc
}

// NewPool creates a worker pool with the given dependencies.
// Use WithMetrics and WithResilience to add optional features.
func NewPool(
	config Config,
	eventRepo repository.EventRepository,
	subRepo repository.SubscriptionRepository,
	httpClient HTTPClient,
	clk clock.Clock,
	retryPolicy retry.Policy,
	logger *slog.Logger,
) *Pool {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Pool{
		config:      config,
		eventRepo:   eventRepo,
		subRepo:     subRepo,
		httpClient:  httpClient,
		clock:       clk,
		retryPolicy: retryPolicy,
		logger:      logger,
	}
}

// WithMetrics enables Prometheus metrics collection.
func (p *Pool) WithMetrics(m *observability.Metrics) *Pool {
	p.metrics = m
	return p
}

// WithResilience enables rate limiting and circuit breaker protection.
// When enabled, deliveries are checked against rate limits and circuit
// breaker state before attempting HTTP requests.
// Accepts the RateLimiter and CircuitBreaker interfaces, allowing both
// in-memory and Redis-backed implementations.
func (p *Pool) WithResilience(rl resilience.RateLimiter, cb resilience.CircuitBreaker) *Pool {
	p.rateLimiter = rl
	p.circuitBreaker = cb
	return p
}

func stateToFloat(s resilience.CircuitBreakerState) float64 {
	switch s {
	case resilience.CircuitBreakerStateClosed:
		return 0
	case resilience.CircuitBreakerStateHalfOpen:
		return 1
	case resilience.CircuitBreakerStateOpen:
		return 2
	default:
		return 0
	}
}

func (p *Pool) Start(ctx context.Context) {
	ctx, p.cancel = context.WithCancel(ctx)

	for i := 0; i < p.config.Workers; i++ {
		p.wg.Add(1)
		go p.worker(ctx, i)
	}

	p.logger.Info("worker pool started", "workers", p.config.Workers)
}

func (p *Pool) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
	p.logger.Info("worker pool stopped")
}

func (p *Pool) worker(ctx context.Context, id int) {
	defer p.wg.Done()

	ticker := time.NewTicker(p.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Debug("worker shutting down", "worker_id", id)
			return
		case <-ticker.C:
			p.processEvents(ctx, id)
		}
	}
}

func (p *Pool) processEvents(ctx context.Context, workerID int) {
	events, err := p.eventRepo.GetPendingEvents(ctx, p.config.BatchSize)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			p.logger.Error("failed to get pending events", "error", err, "worker_id", workerID)
		}
		return
	}

	for _, event := range events {
		if ctx.Err() != nil {
			return
		}
		p.deliverEvent(ctx, event)
	}
}

func (p *Pool) deliverEvent(ctx context.Context, event *domain.Event) {
	if !event.CanRetry() {
		p.logger.Warn("event has exhausted retries, marking as failed",
			"event_id", event.ID,
			"attempts", event.Attempts,
		)
		event.MarkAsFailed("max retries exhausted")
		if err := p.eventRepo.UpdateStatus(ctx, event); err != nil {
			p.logger.Error("failed to update event status", "error", err, "event_id", event.ID)
		}
		p.recordMetricFailed()
		return
	}

	subs, err := p.subRepo.GetByEventType(ctx, event.Type)
	if err != nil {
		p.logger.Error("failed to get subscriptions", "error", err, "event_id", event.ID)
		p.rescheduleEvent(ctx, event, "failed to get subscriptions")
		return
	}

	if len(subs) == 0 {
		p.logger.Debug("no subscriptions for event type", "event_id", event.ID, "type", event.Type)
		event.MarkAsDelivered(p.clock.Now())
		if err := p.eventRepo.UpdateStatus(ctx, event); err != nil {
			p.logger.Error("failed to update event status", "error", err, "event_id", event.ID)
		}
		p.recordMetricDelivered()
		return
	}

	var lastErr error
	for _, sub := range subs {
		if err := p.deliverToSubscription(ctx, event, sub); err != nil {
			lastErr = err
		}
	}

	if lastErr != nil {
		p.handleDeliveryFailure(ctx, event, lastErr)
	} else {
		event.MarkAsDelivered(p.clock.Now())
		if err := p.eventRepo.UpdateStatus(ctx, event); err != nil {
			p.logger.Error("failed to update event status", "error", err, "event_id", event.ID)
		}
		p.recordMetricDelivered()
	}
}

var ErrRateLimited = errors.New("rate limited")
var ErrCircuitOpen = errors.New("circuit breaker is open")

func (p *Pool) deliverToSubscription(ctx context.Context, event *domain.Event, sub *domain.Subscription) error {
	if p.rateLimiter != nil {
		allowed, rlErr := p.rateLimiter.Allow(ctx, sub.ID, sub.RateLimit)
		if rlErr != nil {
			p.logger.Warn("rate limiter error", "error", rlErr, "subscription_id", sub.ID)
		}
		if !allowed {
			p.logger.Debug("rate limited", "subscription_id", sub.ID, "event_id", event.ID)
			if p.metrics != nil {
				p.metrics.RateLimiterRejections.WithLabelValues(sub.ID).Inc()
			}
			return ErrRateLimited
		}
	}

	if p.circuitBreaker != nil {
		allowed, cbErr := p.circuitBreaker.Allow(ctx, sub.ID)
		if cbErr != nil {
			p.logger.Warn("circuit breaker error", "error", cbErr, "subscription_id", sub.ID)
		}
		if !allowed {
			p.logger.Debug("circuit breaker open", "subscription_id", sub.ID, "event_id", event.ID)
			return ErrCircuitOpen
		}
	}

	start := p.clock.Now()

	payload, err := p.buildPayload(event)
	if err != nil {
		return fmt.Errorf("failed to build payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dispatch-Event-ID", event.ID)
	req.Header.Set("X-Dispatch-Event-Type", event.Type)
	req.Header.Set("X-Dispatch-Timestamp", strconv.FormatInt(start.Unix(), 10))

	if sub.Secret != nil && *sub.Secret != "" {
		signature := p.computeSignature(payload, *sub.Secret)
		req.Header.Set("X-Dispatch-Signature", "sha256="+signature)
	}

	var resp *http.Response
	resp, err = p.httpClient.Do(req)

	// Record success/failure for circuit breaker
	if p.circuitBreaker != nil {
		if err != nil || (resp != nil && resp.StatusCode >= 500) {
			p.circuitBreaker.RecordFailure(ctx, sub.ID)
		} else if resp != nil && resp.StatusCode < 500 {
			p.circuitBreaker.RecordSuccess(ctx, sub.ID)
		}
	}
	duration := p.clock.Now().Sub(start)
	p.recordMetricAttempt(duration)

	attempt := &domain.DeliveryAttempt{
		EventID:       event.ID,
		AttemptNumber: event.Attempts + 1,
		DurationMs:    int(duration.Milliseconds()),
	}

	if err != nil {
		errStr := err.Error()
		attempt.Error = &errStr
		p.recordAttempt(ctx, attempt)
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	attempt.StatusCode = &resp.StatusCode

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if len(body) > 0 {
		bodyStr := string(body)
		attempt.ResponseBody = &bodyStr
	}

	p.recordAttempt(ctx, attempt)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		p.logger.Debug("delivery successful",
			"event_id", event.ID,
			"subscription_id", sub.ID,
			"status_code", resp.StatusCode,
			"duration_ms", attempt.DurationMs,
		)
		return nil
	}

	return fmt.Errorf("delivery failed with status %d", resp.StatusCode)
}

func (p *Pool) buildPayload(event *domain.Event) ([]byte, error) {
	payload := map[string]any{
		"id":        event.ID,
		"type":      event.Type,
		"source":    event.Source,
		"data":      event.Data,
		"timestamp": event.CreatedAt.Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

func (p *Pool) computeSignature(payload []byte, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

func (p *Pool) recordAttempt(ctx context.Context, attempt *domain.DeliveryAttempt) {
	if err := p.eventRepo.RecordAttempt(ctx, attempt); err != nil {
		p.logger.Error("failed to record attempt", "error", err, "event_id", attempt.EventID)
	}
}

func (p *Pool) handleDeliveryFailure(ctx context.Context, event *domain.Event, err error) {
	// Rate limiting and circuit breaker are internal backpressure, not delivery failures.
	// Don't increment attempts - just reschedule for immediate retry.
	if errors.Is(err, ErrRateLimited) || errors.Is(err, ErrCircuitOpen) {
		// Reschedule with small delay (backpressure, not exponential backoff)
		nextAttempt := p.clock.Now().Add(time.Second)
		event.MarkAsThrottled(nextAttempt)
		p.logger.Debug("event throttled",
			"event_id", event.ID,
			"reason", err.Error(),
			"next_attempt_at", nextAttempt,
		)
		p.recordMetricThrottled()
		if updateErr := p.eventRepo.UpdateStatus(ctx, event); updateErr != nil {
			p.logger.Error("failed to update event status", "error", updateErr, "event_id", event.ID)
		}
		return
	}

	// Actual delivery failure - use retry with exponential backoff
	if event.CanRetry() {
		nextAttempt := p.retryPolicy.NextAttemptTime(p.clock.Now(), event.Attempts+1)
		event.MarkAsRetrying(nextAttempt, err.Error())
		p.logger.Info("scheduling retry",
			"event_id", event.ID,
			"attempt", event.Attempts,
			"next_attempt_at", nextAttempt,
		)
		p.recordMetricRetrying()
	} else {
		event.MarkAsFailed(err.Error())
		p.logger.Warn("event failed permanently",
			"event_id", event.ID,
			"attempts", event.Attempts,
			"error", err.Error(),
		)
		p.recordMetricFailed()
	}

	if err := p.eventRepo.UpdateStatus(ctx, event); err != nil {
		p.logger.Error("failed to update event status", "error", err, "event_id", event.ID)
	}
}

func (p *Pool) rescheduleEvent(ctx context.Context, event *domain.Event, reason string) {
	nextAttempt := p.clock.Now().Add(p.config.PollInterval * 10)
	event.MarkAsThrottled(nextAttempt)

	if err := p.eventRepo.UpdateStatus(ctx, event); err != nil {
		p.logger.Error("failed to reschedule event", "error", err, "event_id", event.ID)
	}
}

func (p *Pool) recordMetricDelivered() {
	if p.metrics != nil {
		p.metrics.EventsDelivered.Inc()
	}
}

func (p *Pool) recordMetricFailed() {
	if p.metrics != nil {
		p.metrics.EventsFailed.Inc()
	}
}

func (p *Pool) recordMetricRetrying() {
	if p.metrics != nil {
		p.metrics.EventsRetrying.Inc()
	}
}

func (p *Pool) recordMetricThrottled() {
	if p.metrics != nil {
		p.metrics.EventsThrottled.Inc()
	}
}

func (p *Pool) recordMetricAttempt(duration time.Duration) {
	if p.metrics != nil {
		p.metrics.DeliveryAttempts.Inc()
		p.metrics.DeliveryDuration.Observe(duration.Seconds())
	}
}
