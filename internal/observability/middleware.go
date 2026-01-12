package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func MetricsMiddleware(m *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(rw, r)

			duration := time.Since(start).Seconds()

			path := chi.RouteContext(r.Context()).RoutePattern()
			if path == "" {
				path = r.URL.Path
			}

			m.HTTPRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(rw.statusCode)).Inc()
			m.HTTPRequestDuration.WithLabelValues(r.Method, path).Observe(duration)
		})
	}
}
