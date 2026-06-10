// Seed tool for local development and demos.
// Creates subscriptions and publishes events via the HTTP API so the full
// pipeline (API → Kafka → Worker → Receiver) is exercised end-to-end.
//
// Scenarios:
//
//	normal        — healthy receiver, all deliveries should succeed
//	retry         — receiver returns 500 on 70% of requests, events retry
//	circuit-break — receiver returns 500 on all requests until circuit opens,
//	                then receiver is healed and circuit recovers to closed
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// ---- API request / response types (mirrors internal/api/handler.go) --------

type createSubRequest struct {
	ID         string   `json:"id"`
	URL        string   `json:"url"`
	EventTypes []string `json:"event_types"`
	RateLimit  int      `json:"rate_limit,omitempty"`
}

type createEventRequest struct {
	ID     string         `json:"id"`
	Type   string         `json:"type"`
	Source string         `json:"source"`
	Data   map[string]any `json:"data"`
}

// ---- receiver control API --------------------------------------------------

type receiverConfig struct {
	addr     string
	failRate float64
	latency  int
}

// setReceiverBehavior calls the receiver's /control endpoint to change its
// fail rate and latency without restarting the container.
// The testserver exposes /control only when ALLOW_CONTROL=true is set.
// If unreachable, we log a warning and continue — the env var on the compose
// service is the fallback.
func setReceiverBehavior(logger *slog.Logger, cfg receiverConfig) {
	body := fmt.Sprintf(`{"fail_rate":%.2f,"latency_ms":%d}`, cfg.failRate, cfg.latency)
	resp, err := http.Post(cfg.addr+"/control", "application/json", strings.NewReader(body))
	if err != nil {
		logger.Warn("could not reach receiver /control — using compose env vars instead",
			"error", err, "addr", cfg.addr)
		return
	}
	defer resp.Body.Close()
	logger.Info("receiver behavior set", "fail_rate", cfg.failRate, "latency_ms", cfg.latency)
}

// ---- helpers ---------------------------------------------------------------

func postJSON(client *http.Client, url string, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return client.Post(url, "application/json", bytes.NewReader(b))
}

func createSubscription(client *http.Client, api string, req createSubRequest, logger *slog.Logger) {
	resp, err := postJSON(client, api+"/subscriptions", req)
	if err != nil {
		logger.Error("failed to create subscription", "id", req.ID, "error", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		logger.Error("unexpected status creating subscription", "id", req.ID, "status", resp.StatusCode)
		os.Exit(1)
	}
	logger.Info("subscription created", "id", req.ID, "url", req.URL, "event_types", req.EventTypes)
}

func publishEvent(client *http.Client, api string, req createEventRequest, logger *slog.Logger) {
	resp, err := postJSON(client, api+"/events", req)
	if err != nil {
		logger.Error("failed to publish event", "id", req.ID, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		logger.Warn("unexpected status publishing event", "id", req.ID, "status", resp.StatusCode)
	}
}

func waitForAPI(client *http.Client, api string, logger *slog.Logger) {
	logger.Info("waiting for API to be ready", "addr", api)
	for i := 0; i < 30; i++ {
		resp, err := client.Get(api + "/health")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			logger.Info("API is ready")
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(2 * time.Second)
	}
	logger.Error("API did not become ready in time")
	os.Exit(1)
}

// ---- scenarios -------------------------------------------------------------

// scenarioNormal: healthy receiver, all deliveries expected to succeed.
// Shows the happy-path data flow and fills all the Grafana counters.
func scenarioNormal(client *http.Client, api, receiver, receiverControl string, numSubs, numEvents int, logger *slog.Logger) {
	logger.Info("=== scenario: normal ===",
		"subscriptions", numSubs, "events", numEvents)

	setReceiverBehavior(logger, receiverConfig{addr: receiverControl, failRate: 0.0, latency: 50})

	// Create subscriptions — one per event type so fan-out is visible
	for i := 1; i <= numSubs; i++ {
		eventType := fmt.Sprintf("demo.event.%s.type%d", runID, i)
		createSubscription(client, api, createSubRequest{
			ID:         fmt.Sprintf("seed-sub-normal-%s-%d", runID, i),
			URL:        fmt.Sprintf("%s/webhook", receiver),
			EventTypes: []string{eventType},
			RateLimit:  100,
		}, logger)
	}

	// Publish events round-robin across types
	logger.Info("publishing events", "count", numEvents)
	for i := 1; i <= numEvents; i++ {
		typeIdx := ((i - 1) % numSubs) + 1
		publishEvent(client, api, createEventRequest{
			ID:     fmt.Sprintf("seed-evt-normal-%d-%d", time.Now().UnixNano(), i),
			Type:   fmt.Sprintf("demo.event.%s.type%d", runID, typeIdx),
			Source: "seed",
			Data:   map[string]any{"index": i, "scenario": "normal"},
		}, logger)
	}

	logger.Info("normal scenario complete — watch Grafana: events received → delivered")
}

// scenarioRetry: receiver returns 500 on 70% of requests.
// Shows retrying counter climb, then eventual delivery.
func scenarioRetry(client *http.Client, api, receiver, receiverControl string, numEvents int, logger *slog.Logger) {
	logger.Info("=== scenario: retry ===", "events", numEvents)

	setReceiverBehavior(logger, receiverConfig{addr: receiverControl, failRate: 0.7, latency: 50})

	createSubscription(client, api, createSubRequest{
		ID:         fmt.Sprintf("seed-sub-retry-%s", runID),
		URL:        fmt.Sprintf("%s/webhook", receiver),
		EventTypes: []string{fmt.Sprintf("demo.retry.%s", runID)},
		RateLimit:  100,
	}, logger)

	logger.Info("publishing events to flaky receiver (70% fail rate)", "count", numEvents)
	for i := 1; i <= numEvents; i++ {
		publishEvent(client, api, createEventRequest{
			ID:     fmt.Sprintf("seed-evt-retry-%d-%d", time.Now().UnixNano(), i),
			Type:   fmt.Sprintf("demo.retry.%s", runID),
			Source: "seed",
			Data:   map[string]any{"index": i, "scenario": "retry"},
		}, logger)
	}

	logger.Info("retry scenario running — watch Grafana: retrying_total climbs, then delivered_total follows")
}

// scenarioCircuitBreak: receiver is fully broken (100% fail) until the circuit
// opens, then healed so the circuit can recover through half-open → closed.
func scenarioCircuitBreak(client *http.Client, api, receiver, receiverControl string, logger *slog.Logger) {
	logger.Info("=== scenario: circuit-break ===")

	// Phase 1: break the receiver — enough failures to trip the circuit
	// Default RedisCircuitBreaker config: FailureThreshold=5, Window=60s
	// We send 10 events to ensure we exceed the threshold comfortably.
	setReceiverBehavior(logger, receiverConfig{addr: receiverControl, failRate: 1.0, latency: 50})

	createSubscription(client, api, createSubRequest{
		ID:         fmt.Sprintf("seed-sub-cb-%s", runID),
		URL:        fmt.Sprintf("%s/webhook", receiver),
		EventTypes: []string{fmt.Sprintf("demo.circuitbreak.%s", runID)},
		RateLimit:  100,
	}, logger)

	logger.Info("phase 1: publishing events to broken receiver (100% fail) — watch circuit_breaker_state go to 2")
	for i := 1; i <= 10; i++ {
		publishEvent(client, api, createEventRequest{
			ID:     fmt.Sprintf("seed-evt-cb-break-%d-%d", time.Now().UnixNano(), i),
			Type:   fmt.Sprintf("demo.circuitbreak.%s", runID),
			Source: "seed",
			Data:   map[string]any{"index": i, "phase": "break"},
		}, logger)
		time.Sleep(200 * time.Millisecond) // spread slightly so circuit sees sequential failures
	}

	logger.Info("phase 1 complete — circuit should be open (state=2). Waiting 5s before healing...")
	time.Sleep(5 * time.Second)

	// Phase 2: heal the receiver — circuit will transition half-open → closed on next attempt
	setReceiverBehavior(logger, receiverConfig{addr: receiverControl, failRate: 0.0, latency: 50})
	logger.Info("phase 2: receiver healed — watch circuit_breaker_state recover to 0 after timeout (30s)")
	logger.Info("publish a few more events to keep the worker busy while recovery happens")
	for i := 1; i <= 5; i++ {
		publishEvent(client, api, createEventRequest{
			ID:     fmt.Sprintf("seed-evt-cb-heal-%d-%d", time.Now().UnixNano(), i),
			Type:   fmt.Sprintf("demo.circuitbreak.%s", runID),
			Source: "seed",
			Data:   map[string]any{"index": i, "phase": "heal"},
		}, logger)
		time.Sleep(500 * time.Millisecond)
	}

	logger.Info("circuit-break scenario complete — watch Grafana: circuit_breaker_trips_total=1, state recovers to 0")
}

// ---- main ------------------------------------------------------------------

// runID is a short suffix appended to subscription IDs so re-runs don't
// collide with soft-deleted rows from previous runs.
var runID = fmt.Sprintf("%d", time.Now().Unix())

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	api := flag.String("api", envStr("API_ADDR", "http://localhost:8090"), "dispatch API base URL")
	// receiver: URL stored in subscriptions — must be routable from the worker container.
	receiver := flag.String("receiver", envStr("RECEIVER_ADDR", "http://receiver:9000"), "receiver URL stored in subscriptions (must be reachable from the worker)")
	// receiver-control: URL seed uses to call /control — must be reachable from the host.
	receiverControl := flag.String("receiver-control", envStr("RECEIVER_CONTROL_ADDR", "http://localhost:9000"), "receiver URL used by seed to call /control (reachable from host)")
	scenario := flag.String("scenario", "normal", "scenario to run: normal | retry | circuit-break")
	numEvents := flag.Int("events", 50, "number of events to publish")
	numSubs := flag.Int("subs", 3, "number of subscriptions to create (normal scenario)")
	flag.Parse()

	client := &http.Client{Timeout: 10 * time.Second}

	waitForAPI(client, *api, logger)

	switch *scenario {
	case "normal":
		scenarioNormal(client, *api, *receiver, *receiverControl, *numSubs, *numEvents, logger)
	case "retry":
		scenarioRetry(client, *api, *receiver, *receiverControl, *numEvents, logger)
	case "circuit-break":
		scenarioCircuitBreak(client, *api, *receiver, *receiverControl, logger)
	default:
		logger.Error("unknown scenario", "scenario", *scenario, "valid", "normal|retry|circuit-break")
		os.Exit(1)
	}
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
