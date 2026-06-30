package observability

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
)

type HealthChecker interface {
	Ping(ctx context.Context) error
}

type ReadinessCheck struct {
	Name    string
	Checker HealthChecker
}

type HealthHandler struct {
	checks []ReadinessCheck
	ready  atomic.Bool
}

func NewHealthHandler(checks ...ReadinessCheck) *HealthHandler {
	h := &HealthHandler{checks: checks}
	h.ready.Store(false)
	return h
}

func (h *HealthHandler) SetReady(ready bool) {
	h.ready.Store(ready)
}

type HealthResponse struct {
	Status string `json:"status"`
}

type ReadyResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func (h *HealthHandler) Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
}

func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	checks := make(map[string]string)
	allHealthy := true

	if !h.ready.Load() {
		checks["app"] = "not ready"
		allHealthy = false
	} else {
		checks["app"] = "ok"
	}

	for _, check := range h.checks {
		if check.Name == "" || check.Checker == nil {
			continue
		}
		if err := check.Checker.Ping(r.Context()); err != nil {
			checks[check.Name] = "unavailable"
			allHealthy = false
		} else {
			checks[check.Name] = "ok"
		}
	}

	status := "ok"
	statusCode := http.StatusOK
	if !allHealthy {
		status = "degraded"
		statusCode = http.StatusServiceUnavailable
	}

	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(ReadyResponse{
		Status: status,
		Checks: checks,
	})
}
