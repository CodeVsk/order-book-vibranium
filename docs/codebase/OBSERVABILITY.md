# Observability

OpenTelemetry distributed tracing plus structured logging, wired across all
three binaries. Added in `19ef8b5 feat(tracing): add opentelemetry
distributed tracing and jaeger` / `9049415 feat(build): add nginx reverse
proxy with load-balanced api replicas`.

## Tracing setup — `internal/platform/telemetry/telemetry.go`

Each binary calls `InitTracerProvider(ctx, cfg, serviceName, log)` once near
the top of `main()` and defers the returned shutdown func so buffered spans
flush before exit. It:

- Builds an OTLP/HTTP exporter (`otlptracehttp`) pointed at
  `cfg.ExporterEndpoint` (`OTEL_EXPORTER_OTLP_ENDPOINT`, default
  `http://localhost:4318`).
- Registers the W3C `TraceContext` propagator as the global text-map
  propagator.
- Uses `sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))` as the
  sampler; `ratio` comes from `OTEL_TRACES_SAMPLER_ARG` and defaults to
  `1.0` if unset or `<= 0`.
- Sets a per-binary `serviceName` so the three binaries are distinguishable
  in Jaeger: `trade-market-api`, `trade-market-outbox-publisher`,
  `trade-market-matcher`.

## Security-relevant caveats

These are deliberate, documented design choices in
`internal/platform/telemetry/telemetry.go` — worth knowing before changing
error handling or endpoint parsing in this package:

1. **The OTel error handler never logs the raw export error**
   (`telemetry.go:42-47`). A misconfigured or unreachable collector must
   never take the app down — export failures surface only as a generic
   warning ("telemetry: otel internal error (export/collector issue) —
   check OTEL_EXPORTER_OTLP_ENDPOINT connectivity"), never the underlying
   error string, because it can embed the collector host (CWE-209/532:
   information exposure through an error message).
2. **`Inject`/`Extract` are for correlation only, never auth.**
   (`telemetry.go:96-105`). They carry W3C trace context across the
   outbox → Redis Stream async boundary, where there's no HTTP header to
   piggyback on. `Extract`'s doc comment is explicit: "the resulting span
   context must only ever be used to create new spans for observability
   correlation — never to authenticate a caller or drive business logic."
   A W3C `traceparent` is attacker-controllable by design (any client can
   send one on an inbound request), so trusting it for anything beyond
   "which trace does this belong to" would be a spoofing risk. The same
   caution is called out again where `otelchi` extracts inbound
   `traceparent` headers in `internal/infra/httpapi/router.go:16-21`.
3. **Plain HTTP to the collector is a local-dev-only default.**
   `splitEndpoint` (`telemetry.go:117-133`) strips the scheme from
   `OTEL_EXPORTER_OTLP_ENDPOINT` and treats `http://` (or no scheme) as
   insecure. This is acceptable in the compose stack only because traffic
   to the `jaeger` container stays on the internal Docker bridge network —
   exactly like the already-similarly-exposed `postgres`/`redis` services
   in the same file. Any real (non-local) deployment must put TLS + auth in
   front of the collector.

## Log/trace correlation

- `internal/platform/logger/trace_fields.go` — `TraceFields(ctx)` returns
  `zap.String("trace_id", ...)` and `zap.String("span_id", ...)` when `ctx`
  carries a valid OpenTelemetry span context, `nil` otherwise (e.g. unit
  tests, or code paths with no tracer attached). Meant to be spread
  alongside the existing `request_id` field at any log call site.
- `X-Request-Id` is generated/propagated at the edge by nginx
  (`build/nginx/nginx.conf`, `map $http_x_request_id $req_id`) and read by
  the API's `CorrelationID` middleware, so request correlation survives
  behind the load balancer — see
  [ARCHITECTURE.md § Deployment topology](./ARCHITECTURE.md#deployment-topology).

## Instrumentation inventory

| Call site | Mechanism | Spans / effect |
|---|---|---|
| `internal/infra/httpapi/router.go:22` | `otelchi.Middleware("trade-market-api", ...)` | Root span per HTTP request, named by the matched chi route pattern (e.g. `POST /orders/{id}`). |
| `internal/platform/db/db.go:20-24` | `otelsql.Open("pgx", ...)` + `otelsql.RegisterDBStatsMetrics` | Every Postgres query/exec emits a child span. |
| `internal/platform/redisclient/redisclient.go:21` | `redisotel.InstrumentTracing(client)` | Every Redis command (XADD, XREADGROUP, XACK, ...) emits a child span. |
| `internal/application/matcherapp/consumer_loop.go` | `tracer = otel.Tracer("trade-market/matcherapp")` (line 27) | Spans: `matcher.apply_batch` (line 210), `matcher.process_order_event` (line 283), `matcher.match_engine` (line 337). |
| `internal/application/outboxapp/publisher_loop.go` | `tracer = otel.Tracer("trade-market/outboxapp")` (line 20) | Spans: `outbox.publish_once` (line 73), `outbox.publish_message` with an `outbox_id` attribute (line 140). |
| `internal/application/orders/place_order_service.go` | `tracer = otel.Tracer("trade-market/orders")` (line 23) | Span: `orders.place_order` (line 98). |
| `internal/application/orders/cancel_order_service.go` | (shares the `orders` tracer) | Span: `orders.cancel_order` (line 50). |

## Viewing traces locally

With the compose stack up (`make run`):

- Jaeger UI: `http://localhost:16686`
- OTLP/HTTP receiver: `http://jaeger:4318` (internal) / exposed on host
  `:4318`

The `jaeger` service in `build/docker-compose.yml` is explicitly commented
as local-dev-only — not a production deployment artifact.
