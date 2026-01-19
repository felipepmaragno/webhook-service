package observability

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

type contextKey string

const (
	loggerKey  contextKey = "logger"
	eventIDKey contextKey = "event_id"
)

func LoggerFromContext(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return logger
	}
	return slog.Default()
}

func ContextWithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

func ContextWithEventID(ctx context.Context, eventID string) context.Context {
	return context.WithValue(ctx, eventIDKey, eventID)
}

func EventIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(eventIDKey).(string); ok {
		return id
	}
	return ""
}

func LoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			requestID := middleware.GetReqID(r.Context())

			reqLogger := logger.With(
				"request_id", requestID,
				"method", r.Method,
				"path", r.URL.Path,
				"remote_addr", r.RemoteAddr,
			)

			ctx := ContextWithLogger(r.Context(), reqLogger)

			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r.WithContext(ctx))

			// Use Debug level to avoid flooding logs during load tests
			reqLogger.Debug("request completed",
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}
