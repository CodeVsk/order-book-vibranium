// Package telemetry bootstraps the OpenTelemetry tracer provider shared by
// all three binaries (api, outbox-publisher, matcher). Each binary calls
// InitTracerProvider once, near the top of main(), and defers the returned
// shutdown func so buffered spans are flushed before the process exits.
package telemetry

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
	"go.uber.org/zap"
)

// Config is the subset of platform/config.Config telemetry needs. Kept as
// its own small struct (rather than importing platform/config directly) to
// avoid a dependency cycle and keep this package trivially testable.
type Config struct {
	ExporterEndpoint string  // OTEL_EXPORTER_OTLP_ENDPOINT, e.g. "http://localhost:4318"
	SampleRatio      float64 // OTEL_TRACES_SAMPLER_ARG, 0.0-1.0
}

// InitTracerProvider builds an OTLP/HTTP exporter pointed at cfg.ExporterEndpoint,
// registers it as the global TracerProvider (and the W3C TraceContext
// propagator), and returns a shutdown func that flushes and closes the
// exporter. serviceName distinguishes the three binaries in Jaeger
// (trade-market-api, trade-market-outbox-publisher, trade-market-matcher).
//
// A misconfigured or unreachable collector must never take the application
// down: the OTLP exporter batches and retries asynchronously, so a failure
// to export surfaces only as a (generic, non-sensitive) log line from the
// SDK's internal error handler — never as an error returned from here or a
// panic. Never log the raw dial/export error verbatim; it can carry the
// endpoint host — see the handler registered below.
func InitTracerProvider(ctx context.Context, cfg Config, serviceName string, log *zap.Logger) (func(context.Context) error, error) {
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		// Deliberately generic message: the underlying error can embed the
		// exporter endpoint (host/port), which we don't want landing in
		// logs verbatim (CWE-209/532 guidance).
		log.Warn("telemetry: otel internal error (export/collector issue) — check OTEL_EXPORTER_OTLP_ENDPOINT connectivity")
	}))

	endpoint, insecure := splitEndpoint(cfg.ExporterEndpoint)
	opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(endpoint)}
	if insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: build otlp exporter: %w", err)
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(semconv.ServiceNameKey.String(serviceName)),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: build resource: %w", err)
	}

	ratio := cfg.SampleRatio
	if ratio <= 0 {
		ratio = 1.0
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return tp.Shutdown, nil
}

// Inject captures the current span context from ctx into a pair of W3C
// trace-context strings, suitable for embedding into an opaque payload
// that will be read back later (possibly in another process) — used to
// carry trace context across the outbox/Redis-Stream async boundary, where
// there is no header mechanism to piggyback on. Returns "", "" if ctx
// carries no live span.
func Inject(ctx context.Context) (traceparent, tracestate string) {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier.Get("traceparent"), carrier.Get("tracestate")
}

// Extract rebuilds a context carrying the span context encoded by
// traceparent/tracestate (as produced by Inject, potentially in another
// process). Returns ctx unchanged if traceparent is empty (e.g. the
// originating event predates tracing, or that request wasn't sampled).
//
// The resulting span context must only ever be used to create new spans
// for observability correlation — never to authenticate a caller or drive
// business logic. A W3C traceparent is attacker-controllable by design (any
// client can send one on an inbound request), so trusting it for anything
// beyond "which trace does this belong to" would be a spoofing risk.
func Extract(ctx context.Context, traceparent, tracestate string) context.Context {
	if traceparent == "" {
		return ctx
	}
	carrier := propagation.MapCarrier{"traceparent": traceparent}
	if tracestate != "" {
		carrier["tracestate"] = tracestate
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

// splitEndpoint strips a "http://"/"https://" scheme from endpoint (the
// otlptracehttp client wants a bare host:port) and reports whether the
// connection should be unencrypted. This project's docker-compose stack
// only ever talks to the Jaeger container over the internal Docker bridge
// network — plain HTTP is acceptable there, exactly like the already
// similarly-exposed postgres/redis services in that same compose file. Any
// real (non-local) deployment must put TLS + auth in front of the
// collector; this default is local-dev-only.
func splitEndpoint(endpoint string) (host string, insecure bool) {
	if rest, ok := strings.CutPrefix(endpoint, "https://"); ok {
		return rest, false
	}
	if rest, ok := strings.CutPrefix(endpoint, "http://"); ok {
		return rest, true
	}
	return endpoint, true
}
