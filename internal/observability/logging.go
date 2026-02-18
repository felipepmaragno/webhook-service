package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

type contextKey string

const (
	loggerKey  contextKey = "logger"
	eventIDKey contextKey = "event_id"
	traceIDKey contextKey = "trace_id"

	// TraceIDHeader is the W3C-compatible header for trace propagation between services.
	TraceIDHeader = "X-Trace-ID"
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

// TraceIDFromContext returns the trace ID from context.
func TraceIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(traceIDKey).(string); ok {
		return id
	}
	return ""
}

// ContextWithTraceID stores a trace ID in the context.
func ContextWithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// NewTraceID generates a random 16-byte hex trace ID.
func NewTraceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func LoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			requestID := middleware.GetReqID(r.Context())

			// Accept incoming trace ID or generate a new one.
			// This enables end-to-end tracing across services via X-Trace-ID header.
			traceID := r.Header.Get(TraceIDHeader)
			if traceID == "" {
				traceID = NewTraceID()
			}

			// Propagate trace ID in response so callers can correlate.
			w.Header().Set(TraceIDHeader, traceID)

			reqLogger := logger.With(
				"trace_id", traceID,
				"request_id", requestID,
				"method", r.Method,
				"path", r.URL.Path,
				"remote_addr", r.RemoteAddr,
			)

			ctx := ContextWithLogger(r.Context(), reqLogger)
			ctx = ContextWithTraceID(ctx, traceID)

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
