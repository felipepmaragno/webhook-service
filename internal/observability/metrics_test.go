package observability

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
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

	// If we got here without panic, metrics are working
}
