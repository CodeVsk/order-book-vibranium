// internal/application/outboxapp/publisher_loop.go
package outboxapp

import (
	"context"
	"encoding/json"
	"time"

	"trade-market/internal/infra/postgres"
	"trade-market/internal/platform/telemetry"

	"github.com/jmoiron/sqlx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

var tracer = otel.Tracer("trade-market/outboxapp")

// traceCarrier extracts just the two trace-context fields embedded in a
// StreamEvent payload, without outboxapp needing to depend on the orders
// package (the outbox is deliberately payload-agnostic — see StreamEvent's
// doc comment in internal/application/orders/events.go).
type traceCarrier struct {
	TraceParent string `json:"traceparent,omitempty"`
	TraceState  string `json:"tracestate,omitempty"`
}

// Producer is the minimal Redis Stream publish capability the outbox
// publisher depends on. Satisfied by *redisstream.Producer in production;
// substitutable with a fake in tests to deterministically simulate a
// mid-batch failure (a specific event failing while others around it
// succeed) — the exact scenario the per-event commit in publishAndMarkOne
// exists to make safe.
type Producer interface {
	Publish(ctx context.Context, stream string, payload []byte) (string, error)
}

type Publisher struct {
	db        *sqlx.DB
	outbox    *postgres.OutboxRepository
	producer  Producer
	batchSize int
	logger    *zap.Logger
}

func NewPublisher(db *sqlx.DB, outbox *postgres.OutboxRepository, producer Producer, batchSize int, logger *zap.Logger) *Publisher {
	return &Publisher{db: db, outbox: outbox, producer: producer, batchSize: batchSize, logger: logger}
}

// PublishOnce fetches one batch of unpublished outbox events and publishes
// each to its target stream, marking each published individually and
// atomically with its own publish (see publishAndMarkOne). Events stay
// queued in Postgres and are retried on the next tick until published;
// once marked published, an event is never touched again even if a later
// event in the same batch fails.
//
// Each event commits in its own transaction, immediately after its own
// XAdd — publishing the whole batch under one wrapping transaction would
// be unsafe, since a mid-batch failure would roll back the "published"
// bookkeeping for events already durably XAdd'd to Redis, causing the
// next tick to redeliver them as duplicate stream entries (spec §8.3).
//
// Returns the number of events published. Exported (rather than folded
// into Run) so tests can drive exactly one cycle deterministically.
func (p *Publisher) PublishOnce(ctx context.Context) (published int, err error) {
	// This span is the outbox-publisher's own operational trace (cycle
	// timing/lag), independent of any single order's trace — each event
	// inside the loop gets its own separate span, reconnected to that
	// order's originating trace via publishAndMarkOne below.
	ctx, span := tracer.Start(ctx, "outbox.publish_once")
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.SetAttributes(attribute.Int("outbox.published_count", published))
		span.End()
	}()

	events, err := p.fetchBatch(ctx)
	if err != nil {
		return 0, err
	}
	span.SetAttributes(attribute.Int("outbox.batch_size", len(events)))
	if len(events) == 0 {
		return 0, nil
	}

	for _, e := range events {
		ok, err := p.publishAndMarkOne(ctx, e)
		if err != nil {
			p.logger.Error("failed to publish outbox event; will retry next tick", zap.Int64("outbox_id", e.ID), zap.Error(err))
			return published, err
		}
		if ok {
			published++
		}
	}
	if published > 0 {
		p.logger.Info("outbox batch published", zap.Int("count", published))
	}
	return published, nil
}

// fetchBatch reads a batch of unpublished-event candidates. It briefly
// locks them (FOR UPDATE SKIP LOCKED, so a concurrent publisher replica —
// if one were ever run — can't pick the same candidates), then releases
// the lock immediately on commit: no lock needs to survive past this read,
// because publishAndMarkOne re-locks and re-checks each row individually
// right before publishing it.
func (p *Publisher) fetchBatch(ctx context.Context) ([]postgres.OutboxEvent, error) {
	tx, err := p.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	events, err := p.outbox.FetchUnpublishedBatch(ctx, tx, p.batchSize)
	if err != nil {
		return nil, err
	}
	return events, tx.Commit()
}

// publishAndMarkOne re-locks a single candidate row and, only if it's
// still unpublished, publishes it and marks it published — all inside one
// transaction, committed only after the XAdd succeeds. Returns false (no
// error) if the row turned out to already be published or locked by a
// concurrent publisher; both are safe no-ops.
func (p *Publisher) publishAndMarkOne(ctx context.Context, e postgres.OutboxEvent) (ok bool, err error) {
	// Reconnect to the originating request's trace: the gap between outbox
	// insert and publish is at most one poll interval, so a direct
	// parent/child span (not a link) accurately represents "this publish
	// belongs to that request's trace".
	var tc traceCarrier
	_ = json.Unmarshal(e.Payload, &tc) // best-effort: malformed/pre-tracing payloads just get no parent
	spanCtx := telemetry.Extract(ctx, tc.TraceParent, tc.TraceState)
	spanCtx, span := tracer.Start(spanCtx, "outbox.publish_message", trace.WithAttributes(attribute.Int64("outbox_id", e.ID)))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	tx, err := p.db.BeginTxx(spanCtx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	locked, err := p.outbox.LockIfUnpublished(spanCtx, tx, e.ID)
	if err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}

	if _, err := p.producer.Publish(spanCtx, e.StreamName, e.Payload); err != nil {
		return false, err
	}
	if err := p.outbox.MarkPublished(spanCtx, tx, []int64{e.ID}); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Run polls PublishOnce every interval until ctx is cancelled.
func (p *Publisher) Run(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := p.PublishOnce(ctx); err != nil {
				p.logger.Error("publish cycle failed", zap.Error(err))
			}
		}
	}
}
