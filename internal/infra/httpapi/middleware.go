// internal/infra/httpapi/middleware.go
package httpapi

import (
	"context"
	"net/http"
	"time"

	"trade-market/internal/platform/logger"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type contextKey string

const (
	requestIDKey    contextKey = "request_id"
	maxRequestIDLen int        = 128 // generous for a UUID or typical trace-id; guards against log/response bloat from a malicious value
)

// RequestIDFromContext returns the correlation id set by CorrelationID, or
// "" if none is present (should not happen once the middleware is wired).
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// CorrelationID assigns a request_id (reusing an inbound X-Request-Id
// header if present) so every log line for a request can be correlated,
// per spec §7.
func CorrelationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > maxRequestIDLen {
			id = uuid.New().String()
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestLogging logs one structured line per request: method, path,
// status, latency, request_id, and (when a trace is live) trace_id/span_id.
func RequestLogging(zapLog *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			fields := append(logger.TraceFields(r.Context()),
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", sw.status),
				zap.Duration("latency", time.Since(start)),
				zap.String("request_id", RequestIDFromContext(r.Context())),
			)
			zapLog.Info("http_request", fields...)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
