package orders

import (
	"context"

	"trade-market/internal/domain/order"
	"trade-market/internal/platform/telemetry"

	"github.com/google/uuid"
)

// EventType discriminates the two kinds of events the matcher consumes
// from orders:incoming.
type EventType string

const (
	EventTypeNewOrder    EventType = "NEW_ORDER"
	EventTypeCancelOrder EventType = "CANCEL_ORDER"
)

// StreamEvent is the JSON payload stored in outbox_events.payload and
// published verbatim (as the "payload" field) to the orders:incoming
// Redis Stream.
//
// TraceParent/TraceState carry the W3C trace context of the request that
// placed/cancelled this order. Redis Streams have no header concept, and
// the outbox pattern already flows this exact payload byte-identical from
// Postgres to Redis to the matcher — embedding trace context here (rather
// than adding a DB column or a stream-message field) lets it ride the same
// path for free. Populated by InjectTraceContext at outbox-insert time and
// read back by ExtractTraceContext downstream; never used for anything
// other than observability correlation (an inbound traceparent is
// caller-controlled by design and must never drive auth/business logic).
type StreamEvent struct {
	Type        EventType  `json:"type"`
	OrderID     uuid.UUID  `json:"order_id"`
	UserID      uuid.UUID  `json:"user_id"`
	Side        order.Side `json:"side,omitempty"`
	OrderType   order.Type `json:"order_type,omitempty"`
	PriceCents  *int64     `json:"price_cents,omitempty"`
	Quantity    int64      `json:"quantity,omitempty"`
	RequestedBy *uuid.UUID `json:"requested_by,omitempty"` // CANCEL_ORDER only
	TraceParent string     `json:"traceparent,omitempty"`
	TraceState  string     `json:"tracestate,omitempty"`
}

// InjectTraceContext captures the current span context from ctx (the live
// HTTP request span, if any) into ev's TraceParent/TraceState fields, ready
// for JSON marshaling into the outbox row.
func InjectTraceContext(ctx context.Context, ev *StreamEvent) {
	ev.TraceParent, ev.TraceState = telemetry.Inject(ctx)
}

// ExtractTraceContext rebuilds a context carrying ev's embedded trace
// context, for downstream (matcher) span creation. Returns ctx unchanged if
// ev carries no trace context (e.g. it was placed before tracing was wired
// up, or the placing request wasn't sampled).
func ExtractTraceContext(ctx context.Context, ev StreamEvent) context.Context {
	return telemetry.Extract(ctx, ev.TraceParent, ev.TraceState)
}
