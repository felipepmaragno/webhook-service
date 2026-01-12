package api

import (
	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/felipemaragno/dispatch/internal/observability"
)

type RouterConfig struct {
	Handler       *Handler
	HealthHandler *observability.HealthHandler
	Metrics       *observability.Metrics
	Logger        *slog.Logger
}

func NewRouter(cfg RouterConfig) *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	if cfg.Logger != nil {
		r.Use(observability.LoggingMiddleware(cfg.Logger))
	}

	if cfg.Metrics != nil {
		r.Use(observability.MetricsMiddleware(cfg.Metrics))
	}

	r.Get("/health", cfg.HealthHandler.Health)
	r.Get("/ready", cfg.HealthHandler.Ready)
	r.Handle("/metrics", promhttp.Handler())

	r.Route("/events", func(r chi.Router) {
		r.Post("/", cfg.Handler.CreateEvent)
		r.Get("/{id}", cfg.Handler.GetEvent)
		r.Get("/{id}/attempts", cfg.Handler.GetEventAttempts)
	})

	r.Route("/subscriptions", func(r chi.Router) {
		r.Post("/", cfg.Handler.CreateSubscription)
		r.Get("/", cfg.Handler.GetSubscriptions)
		r.Delete("/{id}", cfg.Handler.DeleteSubscription)
	})

	return r
}
