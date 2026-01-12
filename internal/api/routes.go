package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func NewRouter(h *Handler) *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Get("/health", h.Health)

	r.Route("/events", func(r chi.Router) {
		r.Post("/", h.CreateEvent)
		r.Get("/{id}", h.GetEvent)
		r.Get("/{id}/attempts", h.GetEventAttempts)
	})

	r.Route("/subscriptions", func(r chi.Router) {
		r.Post("/", h.CreateSubscription)
		r.Get("/", h.GetSubscriptions)
		r.Delete("/{id}", h.DeleteSubscription)
	})

	return r
}
