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

type HealthHandler struct {
	db    HealthChecker
	ready atomic.Bool
}

func NewHealthHandler(db HealthChecker) *HealthHandler {
	h := &HealthHandler{db: db}
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
	json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
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

	if h.db != nil {
		if err := h.db.Ping(r.Context()); err != nil {
			checks["database"] = err.Error()
			allHealthy = false
		} else {
			checks["database"] = "ok"
		}
	}

	status := "ok"
	statusCode := http.StatusOK
	if !allHealthy {
		status = "degraded"
		statusCode = http.StatusServiceUnavailable
	}

	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(ReadyResponse{
		Status: status,
		Checks: checks,
	})
}
