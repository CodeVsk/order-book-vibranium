package logger

import (
	"context"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// TraceFields returns zap.String("trace_id", ...) and zap.String("span_id", ...)
// when ctx carries a valid OpenTelemetry span context, or nil otherwise (e.g.
// in unit tests or code paths that never had a tracer attached). Spread
// alongside the existing request_id field at any log call site to correlate
// a log line with its trace in Jaeger:
//
//	logger.Info("...", append(logger.TraceFields(ctx), zap.String("request_id", id))...)
func TraceFields(ctx context.Context) []zap.Field {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}
	return []zap.Field{
		zap.String("trace_id", sc.TraceID().String()),
		zap.String("span_id", sc.SpanID().String()),
	}
}
