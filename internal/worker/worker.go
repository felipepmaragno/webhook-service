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

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

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

type Pool struct {
	config      Config
	eventRepo   repository.EventRepository
	subRepo     repository.SubscriptionRepository
	httpClient  HTTPClient
	clock       clock.Clock
	retryPolicy retry.Policy
	logger      *slog.Logger
	metrics     *observability.Metrics

	rateLimiter    *resilience.RateLimiterManager
	circuitBreaker *resilience.CircuitBreakerManager

	wg     sync.WaitGroup
	cancel context.CancelFunc
}

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

func (p *Pool) WithMetrics(m *observability.Metrics) *Pool {
	p.metrics = m
	return p
}

func (p *Pool) WithResilience(rl *resilience.RateLimiterManager, cb *resilience.CircuitBreakerManager) *Pool {
	p.rateLimiter = rl
	p.circuitBreaker = cb

	if cb != nil && p.metrics != nil {
		cb.OnStateChange(func(subID string, from, to resilience.CircuitBreakerState) {
			p.logger.Info("circuit breaker state changed",
				"subscription_id", subID,
				"from", from,
				"to", to,
			)
			p.metrics.CircuitBreakerState.WithLabelValues(subID).Set(stateToFloat(to))
			if to == resilience.CircuitBreakerStateOpen {
				p.metrics.CircuitBreakerTrips.WithLabelValues(subID).Inc()
			}
		})
	}
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
	if p.rateLimiter != nil && !p.rateLimiter.Allow(sub.ID) {
		p.logger.Debug("rate limited", "subscription_id", sub.ID, "event_id", event.ID)
		if p.metrics != nil {
			p.metrics.RateLimiterRejections.WithLabelValues(sub.ID).Inc()
		}
		return ErrRateLimited
	}

	if p.circuitBreaker != nil {
		state := p.circuitBreaker.State(sub.ID)
		if state == resilience.CircuitBreakerStateOpen {
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
	if p.circuitBreaker != nil {
		result, cbErr := p.circuitBreaker.Execute(sub.ID, func() (interface{}, error) {
			r, httpErr := p.httpClient.Do(req)
			if httpErr != nil {
				return nil, httpErr
			}
			if r.StatusCode >= 500 {
				return r, fmt.Errorf("server error: %d", r.StatusCode)
			}
			return r, nil
		})
		if result != nil {
			resp = result.(*http.Response)
		}
		if cbErr != nil && resp == nil {
			err = cbErr
		}
	} else {
		resp, err = p.httpClient.Do(req)
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
	event.RescheduleWithoutAttemptIncrement(nextAttempt)

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

func (p *Pool) recordMetricAttempt(duration time.Duration) {
	if p.metrics != nil {
		p.metrics.DeliveryAttempts.Inc()
		p.metrics.DeliveryDuration.Observe(duration.Seconds())
	}
}
