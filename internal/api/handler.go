// Package api implements the HTTP REST API for the webhook dispatcher.
//
// Uses github.com/go-chi/chi/v5 - a lightweight, idiomatic router.
// Chosen over alternatives like Gin or Echo for:
//   - Minimal dependencies and small footprint
//   - Full compatibility with net/http (middleware, handlers)
//   - Clean, composable routing with URL parameters
//
// Endpoints:
//
//	POST   /events              Create a new event
//	GET    /events/{id}         Get event by ID
//	GET    /events/{id}/attempts Get delivery attempts
//	POST   /subscriptions       Create subscription
//	GET    /subscriptions       List active subscriptions
//	DELETE /subscriptions/{id}  Delete subscription
//	GET    /health              Health check
//	GET    /ready               Readiness check
//	GET    /metrics             Prometheus metrics
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/observability"
	"github.com/felipemaragno/dispatch/internal/repository"
	"github.com/felipemaragno/dispatch/internal/repository/postgres"
)

// Handler implements the HTTP API endpoints.
// It depends on repositories for data access and optionally metrics.
type Handler struct {
	eventRepo repository.EventRepository
	subRepo   repository.SubscriptionRepository
	logger    *slog.Logger
	metrics   *observability.Metrics
}

func NewHandler(eventRepo repository.EventRepository, subRepo repository.SubscriptionRepository, logger *slog.Logger) *Handler {
	return &Handler{
		eventRepo: eventRepo,
		subRepo:   subRepo,
		logger:    logger,
	}
}

func (h *Handler) WithMetrics(m *observability.Metrics) *Handler {
	h.metrics = m
	return h
}

type CreateEventRequest struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Source string          `json:"source"`
	Data   json.RawMessage `json:"data"`
}

type CreateEventResponse struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateEvent handles POST /events.
// Creates a new event in pending status for delivery.
func (h *Handler) CreateEvent(w http.ResponseWriter, r *http.Request) {
	var req CreateEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.ID == "" || req.Type == "" || req.Source == "" {
		h.respondError(w, http.StatusBadRequest, "id, type, and source are required")
		return
	}

	now := time.Now()
	event := &domain.Event{
		ID:          req.ID,
		Type:        req.Type,
		Source:      req.Source,
		Data:        req.Data,
		Status:      domain.EventStatusPending,
		Attempts:    0,
		MaxAttempts: 5,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := h.eventRepo.Create(r.Context(), event); err != nil {
		h.logger.Error("failed to create event", "error", err, "event_id", req.ID)
		h.respondError(w, http.StatusInternalServerError, "failed to create event")
		return
	}

	if h.metrics != nil {
		h.metrics.EventsReceived.Inc()
	}

	h.respondJSON(w, http.StatusAccepted, CreateEventResponse{
		ID:        event.ID,
		Status:    string(event.Status),
		CreatedAt: event.CreatedAt,
	})
}

func (h *Handler) GetEvent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		h.respondError(w, http.StatusBadRequest, "event id is required")
		return
	}

	event, err := h.eventRepo.GetByID(r.Context(), id)
	if errors.Is(err, postgres.ErrNotFound) {
		h.respondError(w, http.StatusNotFound, "event not found")
		return
	}
	if err != nil {
		h.logger.Error("failed to get event", "error", err, "event_id", id)
		h.respondError(w, http.StatusInternalServerError, "failed to get event")
		return
	}

	h.respondJSON(w, http.StatusOK, event)
}

func (h *Handler) GetEventAttempts(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		h.respondError(w, http.StatusBadRequest, "event id is required")
		return
	}

	attempts, err := h.eventRepo.GetAttemptsByEventID(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to get attempts", "error", err, "event_id", id)
		h.respondError(w, http.StatusInternalServerError, "failed to get attempts")
		return
	}

	h.respondJSON(w, http.StatusOK, attempts)
}

type CreateSubscriptionRequest struct {
	ID         string   `json:"id"`
	URL        string   `json:"url"`
	EventTypes []string `json:"event_types"`
	Secret     *string  `json:"secret,omitempty"`
	RateLimit  int      `json:"rate_limit,omitempty"`
}

// CreateSubscription handles POST /subscriptions.
// Creates a new webhook subscription with event type filters.
func (h *Handler) CreateSubscription(w http.ResponseWriter, r *http.Request) {
	var req CreateSubscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.ID == "" || req.URL == "" || len(req.EventTypes) == 0 {
		h.respondError(w, http.StatusBadRequest, "id, url, and event_types are required")
		return
	}

	rateLimit := req.RateLimit
	if rateLimit <= 0 {
		rateLimit = 100
	}

	sub := &domain.Subscription{
		ID:         req.ID,
		URL:        req.URL,
		EventTypes: req.EventTypes,
		Secret:     req.Secret,
		RateLimit:  rateLimit,
		CreatedAt:  time.Now(),
		Active:     true,
	}

	if err := h.subRepo.Create(r.Context(), sub); err != nil {
		h.logger.Error("failed to create subscription", "error", err, "subscription_id", req.ID)
		h.respondError(w, http.StatusInternalServerError, "failed to create subscription")
		return
	}

	h.respondJSON(w, http.StatusCreated, sub)
}

func (h *Handler) GetSubscriptions(w http.ResponseWriter, r *http.Request) {
	subs, err := h.subRepo.GetActive(r.Context())
	if err != nil {
		h.logger.Error("failed to get subscriptions", "error", err)
		h.respondError(w, http.StatusInternalServerError, "failed to get subscriptions")
		return
	}

	h.respondJSON(w, http.StatusOK, subs)
}

func (h *Handler) DeleteSubscription(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		h.respondError(w, http.StatusBadRequest, "subscription id is required")
		return
	}

	if err := h.subRepo.Delete(r.Context(), id); err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			h.respondError(w, http.StatusNotFound, "subscription not found")
			return
		}
		h.logger.Error("failed to delete subscription", "error", err, "subscription_id", id)
		h.respondError(w, http.StatusInternalServerError, "failed to delete subscription")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	h.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type errorResponse struct {
	Error string `json:"error"`
}

func (h *Handler) respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		h.logger.Error("failed to encode response", "error", err)
	}
}

func (h *Handler) respondError(w http.ResponseWriter, status int, message string) {
	h.respondJSON(w, status, errorResponse{Error: message})
}
