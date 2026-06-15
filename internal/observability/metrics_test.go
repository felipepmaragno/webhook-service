package observability

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestNewMetrics(t *testing.T) {
	// Reset default registry for test isolation
	reg := prometheus.NewRegistry()
	prometheus.DefaultRegisterer = reg
	prometheus.DefaultGatherer = reg

	m := NewMetrics("dispatch")

	if m.EventsReceived == nil {
		t.Error("EventsReceived counter should not be nil")
	}

	if m.EventsDelivered == nil {
		t.Error("EventsDelivered counter should not be nil")
	}

	if m.EventsFailed == nil {
		t.Error("EventsFailed counter should not be nil")
	}

	if m.DeliveryDuration == nil {
		t.Error("DeliveryDuration histogram should not be nil")
	}

	if m.HTTPRequestsTotal == nil {
		t.Error("HTTPRequestsTotal counter vec should not be nil")
	}

	if m.HTTPRequestDuration == nil {
		t.Error("HTTPRequestDuration histogram vec should not be nil")
	}
	if m.RetryEventsClaimed == nil || m.RetryActiveBatches == nil || m.RetrySchedulingLag == nil {
		t.Error("retry scheduler metrics should not be nil")
	}
}

func TestMetrics_Increment(t *testing.T) {
	reg := prometheus.NewRegistry()
	prometheus.DefaultRegisterer = reg
	prometheus.DefaultGatherer = reg

	m := NewMetrics("test")

	m.EventsReceived.Inc()
	m.EventsDelivered.Inc()
	m.EventsFailed.Inc()
	m.DeliveryAttempts.Inc()
	m.DeliveryDuration.Observe(0.5)
	m.HTTPRequestsTotal.WithLabelValues("GET", "/events", "200").Inc()
	m.HTTPRequestDuration.WithLabelValues("GET", "/events").Observe(0.1)
	m.RetryEventsClaimed.Add(2)
	m.RetryEventsReclaimed.Inc()
	m.RetryEmptyPolls.Inc()
	m.RetryClaimFailures.Inc()
	m.RetryPersistenceFailures.Inc()
	m.RetryStaleOwnerRejections.Inc()
	m.RetryActiveBatches.Inc()
	m.RetryDueEvents.Set(3)
	m.RetryExpiredClaims.Set(1)
	m.RetryLeasedEvents.Set(2)
	m.RetryOldestDueAge.Set(5)
	m.RetrySchedulingLag.Observe(0.5)

	// If we got here without panic, metrics are working
}

func TestWorkerMetricsEndpointIncludesRetrySchedulerMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	prometheus.DefaultRegisterer = reg
	prometheus.DefaultGatherer = reg

	NewMetrics("dispatch_worker")
	recorder := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))

	if recorder.Code != 200 {
		t.Fatalf("metrics status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, name := range []string{
		"dispatch_worker_retry_active_batches",
		"dispatch_worker_retry_due_events",
		"dispatch_worker_retry_claim_failures_total",
		"dispatch_worker_retry_scheduling_lag_seconds",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("metrics response does not contain %s", name)
		}
	}
}
