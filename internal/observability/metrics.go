// Package observability provides Prometheus metrics, health checks, and logging.
//
// Uses github.com/prometheus/client_golang - the official Prometheus client.
// Chosen for its maturity, wide adoption, and seamless integration with
// the Prometheus ecosystem (Grafana, Alertmanager, etc.).
package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus metrics for the dispatch service.
// Metrics are automatically registered via promauto.
//
// Key metrics for monitoring:
//   - events_received_total: Inbound event rate
//   - events_delivered_total: Successful delivery rate
//   - events_failed_total: Permanent failures (alerts)
//   - delivery_duration_seconds: Latency distribution
//   - circuit_breaker_state: Destination health (0=ok, 2=failing)
type Metrics struct {
	EventsReceived      prometheus.Counter
	EventsDelivered     prometheus.Counter
	EventsFailed        prometheus.Counter
	EventsRetrying      prometheus.Counter
	EventsThrottled     prometheus.Counter
	DeliveryDuration    prometheus.Histogram
	DeliveryAttempts    prometheus.Counter
	HTTPRequestsTotal   *prometheus.CounterVec
	HTTPRequestDuration *prometheus.HistogramVec

	CircuitBreakerState   *prometheus.GaugeVec
	CircuitBreakerTrips   *prometheus.CounterVec
	RateLimiterRejections *prometheus.CounterVec
	RateLimiterDegraded   prometheus.Counter

	RetryEventsClaimed        prometheus.Counter
	RetryEventsReclaimed      prometheus.Counter
	RetryEmptyPolls           prometheus.Counter
	RetryClaimFailures        prometheus.Counter
	RetryPersistenceFailures  prometheus.Counter
	RetryStaleOwnerRejections prometheus.Counter
	RetryActiveBatches        prometheus.Gauge
	RetryDueEvents            prometheus.Gauge
	RetryExpiredClaims        prometheus.Gauge
	RetryLeasedEvents         prometheus.Gauge
	RetryOldestDueAge         prometheus.Gauge
	RetrySchedulingLag        prometheus.Histogram
}

// NewMetrics creates and registers all Prometheus metrics.
// The namespace prefixes all metric names (e.g., "dispatch_events_received_total").
func NewMetrics(namespace string) *Metrics {
	return &Metrics{
		EventsReceived: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "events_received_total",
			Help:      "Total number of events received via API",
		}),
		EventsDelivered: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "events_delivered_total",
			Help:      "Total number of events successfully delivered",
		}),
		EventsFailed: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "events_failed_total",
			Help:      "Total number of events that failed after all retries",
		}),
		EventsRetrying: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "events_retrying_total",
			Help:      "Total number of events scheduled for retry",
		}),
		EventsThrottled: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "events_throttled_total",
			Help:      "Total number of events throttled by rate limiting or circuit breaker",
		}),
		DeliveryDuration: promauto.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "delivery_duration_seconds",
			Help:      "Duration of webhook delivery attempts in seconds",
			Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}),
		DeliveryAttempts: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "delivery_attempts_total",
			Help:      "Total number of delivery attempts made",
		}),
		HTTPRequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "http_requests_total",
			Help:      "Total number of HTTP requests by method and path",
		}, []string{"method", "path", "status"}),
		HTTPRequestDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "http_request_duration_seconds",
			Help:      "Duration of HTTP requests in seconds",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method", "path"}),

		CircuitBreakerState: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "circuit_breaker_state",
			Help:      "Current state of circuit breaker (0=closed, 1=half-open, 2=open)",
		}, []string{"subscription_id"}),
		CircuitBreakerTrips: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "circuit_breaker_trips_total",
			Help:      "Total number of times circuit breaker tripped to open state",
		}, []string{"subscription_id"}),
		RateLimiterRejections: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "rate_limiter_rejections_total",
			Help:      "Total number of requests rejected by rate limiter",
		}, []string{"subscription_id"}),
		RateLimiterDegraded: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "rate_limiter_degraded_total",
			Help:      "Total number of rate limiter decisions served by local fallback because Redis was unavailable",
		}),
		RetryEventsClaimed: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "retry_events_claimed_total",
			Help:      "Total retry events claimed for processing",
		}),
		RetryEventsReclaimed: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "retry_events_reclaimed_total",
			Help:      "Total expired retry claims reclaimed after lease expiration",
		}),
		RetryEmptyPolls: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "retry_empty_polls_total",
			Help:      "Total retry claims that returned no work",
		}),
		RetryClaimFailures: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "retry_claim_failures_total",
			Help:      "Total failures while claiming retry work",
		}),
		RetryPersistenceFailures: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "retry_persistence_failures_total",
			Help:      "Total retry batches whose outcomes failed to persist",
		}),
		RetryStaleOwnerRejections: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "retry_stale_owner_rejections_total",
			Help:      "Total retry outcome writes rejected after ownership was lost",
		}),
		RetryActiveBatches: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "retry_active_batches",
			Help:      "Current retry batches being processed by this worker",
		}),
		RetryDueEvents: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "retry_due_events",
			Help:      "Current retrying or throttled events whose schedule is due",
		}),
		RetryExpiredClaims: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "retry_expired_claims",
			Help:      "Current processing events whose retry lease has expired",
		}),
		RetryLeasedEvents: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "retry_leased_events",
			Help:      "Current processing events with an unexpired retry lease",
		}),
		RetryOldestDueAge: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "retry_oldest_due_age_seconds",
			Help:      "Age in seconds of the oldest due retry or expired claim",
		}),
		RetrySchedulingLag: promauto.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "retry_scheduling_lag_seconds",
			Help:      "Delay between a retry becoming eligible and being claimed",
			Buckets:   []float64{0.01, 0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 300},
		}),
	}
}
