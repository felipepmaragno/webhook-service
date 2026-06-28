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
//	POST   /events              Publish event to Kafka
//	GET    /events/{id}         Get event status from DB
//	GET    /events/{id}/attempts Get delivery attempts
//	GET    /events/{id}/deliveries Get per-subscription deliveries
//	POST   /deliveries/{id}/replay Replay a failed delivery
//	POST   /subscriptions       Create subscription
//	GET    /subscriptions       List active subscriptions
//	PUT    /subscriptions/{id}/secret Rotate subscription secret
//	DELETE /subscriptions/{id}  Delete subscription
//	GET    /health              Health check
//	GET    /ready               Readiness check
//	GET    /metrics             Prometheus metrics
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/kafka"
	"github.com/felipemaragno/dispatch/internal/observability"
	"github.com/felipemaragno/dispatch/internal/repository"
)

// EventPublisher publishes events to the message queue.
type EventPublisher interface {
	Publish(ctx context.Context, event kafka.EventMessage) error
	Close() error
}

// SubscriptionRepository contains only subscription operations owned by the HTTP API.
type SubscriptionRepository interface {
	Create(ctx context.Context, sub *domain.Subscription) error
	GetActive(ctx context.Context) ([]*domain.Subscription, error)
	Delete(ctx context.Context, id string) error
	UpdateSecret(ctx context.Context, id, secret string) error
}

type EventRepository interface {
	repository.APIEventRepository
	ReplayFailedDelivery(ctx context.Context, id string, scheduledAt time.Time) (*domain.Delivery, error)
}

// Handler implements the HTTP API endpoints.
// Events are published to Kafka, subscriptions/status are in PostgreSQL.
type Handler struct {
	publisher EventPublisher
	eventRepo EventRepository
	subRepo   SubscriptionRepository
	logger    *slog.Logger
	metrics   *observability.Metrics
}

func NewHandler(publisher EventPublisher, eventRepo EventRepository, subRepo SubscriptionRepository, logger *slog.Logger) *Handler {
	return &Handler{
		publisher: publisher,
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
// Publishes the event directly to Kafka for delivery.
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

	event := kafka.EventMessage{
		ID:          req.ID,
		Type:        req.Type,
		Source:      req.Source,
		Data:        req.Data,
		MaxAttempts: 5,
	}

	if err := h.publisher.Publish(r.Context(), event); err != nil {
		h.logger.Error("failed to publish event", "error", err, "event_id", req.ID)
		h.respondError(w, http.StatusInternalServerError, "failed to publish event")
		return
	}

	if h.metrics != nil {
		h.metrics.EventsReceived.Inc()
	}

	h.respondJSON(w, http.StatusAccepted, CreateEventResponse{
		ID:        req.ID,
		Status:    "pending",
		CreatedAt: time.Now(),
	})
}

func (h *Handler) GetEvent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		h.respondError(w, http.StatusBadRequest, "event id is required")
		return
	}

	event, err := h.eventRepo.GetByID(r.Context(), id)
	if errors.Is(err, domain.ErrNotFound) {
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

func (h *Handler) GetEventDeliveries(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		h.respondError(w, http.StatusBadRequest, "event id is required")
		return
	}

	deliveries, err := h.eventRepo.GetDeliveriesByEventID(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to get deliveries", "error", err, "event_id", id)
		h.respondError(w, http.StatusInternalServerError, "failed to get deliveries")
		return
	}

	h.respondJSON(w, http.StatusOK, deliveries)
}

type CreateSubscriptionRequest struct {
	ID              string   `json:"id"`
	URL             string   `json:"url"`
	EventTypes      []string `json:"event_types"`
	Secret          *string  `json:"secret,omitempty"`
	MaxDeliveryRate int      `json:"max_delivery_rate,omitempty"`
}

type SubscriptionResponse struct {
	ID              string    `json:"id"`
	URL             string    `json:"url"`
	EventTypes      []string  `json:"event_types"`
	MaxDeliveryRate int       `json:"max_delivery_rate"`
	CreatedAt       time.Time `json:"created_at"`
	Active          bool      `json:"active"`
}

type RotateSubscriptionSecretRequest struct {
	Secret string `json:"secret"`
}

type RotateSubscriptionSecretResponse struct {
	ID            string `json:"id"`
	SecretRotated bool   `json:"secret_rotated"`
}

type ReplayDeliveryResponse struct {
	ID          string                `json:"id"`
	EventID     string                `json:"event_id"`
	Status      domain.DeliveryStatus `json:"status"`
	Generation  int                   `json:"generation"`
	ScheduledAt time.Time             `json:"scheduled_at"`
}

const maxSubscriptionSecretBytes = 4096

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
	if req.Secret != nil && (*req.Secret == "" || len(*req.Secret) > maxSubscriptionSecretBytes) {
		h.respondError(w, http.StatusBadRequest, "secret must be between 1 and 4096 bytes")
		return
	}

	maxDeliveryRate := req.MaxDeliveryRate
	if maxDeliveryRate <= 0 {
		maxDeliveryRate = domain.DefaultSubscriptionMaxDeliveryRate
	}

	sub := &domain.Subscription{
		ID:              req.ID,
		URL:             req.URL,
		EventTypes:      req.EventTypes,
		Secret:          req.Secret,
		MaxDeliveryRate: maxDeliveryRate,
		CreatedAt:       time.Now(),
		Active:          true,
	}

	if err := h.subRepo.Create(r.Context(), sub); err != nil {
		h.logger.Error("failed to create subscription", "error", err, "subscription_id", req.ID)
		h.respondError(w, http.StatusInternalServerError, "failed to create subscription")
		return
	}

	h.respondJSON(w, http.StatusCreated, subscriptionResponse(sub))
}

func (h *Handler) GetSubscriptions(w http.ResponseWriter, r *http.Request) {
	subs, err := h.subRepo.GetActive(r.Context())
	if err != nil {
		h.logger.Error("failed to get subscriptions", "error", err)
		h.respondError(w, http.StatusInternalServerError, "failed to get subscriptions")
		return
	}

	responses := make([]SubscriptionResponse, 0, len(subs))
	for _, sub := range subs {
		responses = append(responses, subscriptionResponse(sub))
	}

	h.respondJSON(w, http.StatusOK, responses)
}

func (h *Handler) RotateSubscriptionSecret(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		h.respondError(w, http.StatusBadRequest, "subscription id is required")
		return
	}

	var req RotateSubscriptionSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Secret == "" || len(req.Secret) > maxSubscriptionSecretBytes {
		h.respondError(w, http.StatusBadRequest, "secret must be between 1 and 4096 bytes")
		return
	}

	if err := h.subRepo.UpdateSecret(r.Context(), id, req.Secret); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			h.respondError(w, http.StatusNotFound, "subscription not found")
			return
		}
		h.logger.Error("failed to rotate subscription secret", "error", err, "subscription_id", id)
		h.respondError(w, http.StatusInternalServerError, "failed to rotate subscription secret")
		return
	}

	h.respondJSON(w, http.StatusOK, RotateSubscriptionSecretResponse{ID: id, SecretRotated: true})
}

func (h *Handler) ReplayDelivery(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		h.respondError(w, http.StatusBadRequest, "delivery id is required")
		return
	}

	scheduledAt := time.Now().UTC()
	delivery, err := h.eventRepo.ReplayFailedDelivery(r.Context(), id, scheduledAt)
	if errors.Is(err, domain.ErrNotFound) {
		h.respondError(w, http.StatusNotFound, "delivery not found")
		return
	}
	if errors.Is(err, domain.ErrReplayNotEligible) {
		h.respondError(w, http.StatusConflict, "delivery is not failed")
		return
	}
	if err != nil {
		h.logger.Error("failed to replay delivery", "error", err, "delivery_id", id)
		h.respondError(w, http.StatusInternalServerError, "failed to replay delivery")
		return
	}

	h.respondJSON(w, http.StatusAccepted, ReplayDeliveryResponse{
		ID:          delivery.ID,
		EventID:     delivery.EventID,
		Status:      delivery.Status,
		Generation:  delivery.Generation,
		ScheduledAt: *delivery.NextAttemptAt,
	})
}

func (h *Handler) DeleteSubscription(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		h.respondError(w, http.StatusBadRequest, "subscription id is required")
		return
	}

	if err := h.subRepo.Delete(r.Context(), id); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			h.respondError(w, http.StatusNotFound, "subscription not found")
			return
		}
		h.logger.Error("failed to delete subscription", "error", err, "subscription_id", id)
		h.respondError(w, http.StatusInternalServerError, "failed to delete subscription")
		return
	}

	w.WriteHeader(http.StatusNoContent)
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

func subscriptionResponse(sub *domain.Subscription) SubscriptionResponse {
	return SubscriptionResponse{
		ID:              sub.ID,
		URL:             sub.URL,
		EventTypes:      sub.EventTypes,
		MaxDeliveryRate: sub.MaxDeliveryRate,
		CreatedAt:       sub.CreatedAt,
		Active:          sub.Active,
	}
}
