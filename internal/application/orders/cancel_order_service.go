// internal/application/orders/cancel_order_service.go
package orders

import (
	"context"
	"encoding/json"
	"errors"

	"trade-market/internal/domain/order"
	"trade-market/internal/infra/postgres"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

var (
	ErrOrderNotFound = errors.New("orders: order not found")
	ErrForbidden     = errors.New("orders: order belongs to a different user")
)

type CancelResult struct {
	OrderID         uuid.UUID
	Status          order.Status
	AlreadyTerminal bool // true => nothing was queued, current status returned as-is (HTTP 200)
}

type CancelOrderService struct {
	db         *sqlx.DB
	orders     *postgres.OrderRepository
	outbox     *postgres.OutboxRepository
	streamName string
	logger     *zap.Logger
}

func NewCancelOrderService(db *sqlx.DB, orders *postgres.OrderRepository, outbox *postgres.OutboxRepository, streamName string, logger *zap.Logger) *CancelOrderService {
	return &CancelOrderService{db: db, orders: orders, outbox: outbox, streamName: streamName, logger: logger}
}

// Cancel checks ownership and current status, then — only if the order is
// still OPEN/PARTIALLY_FILLED — writes a CANCEL_ORDER outbox event inside a
// transaction (durability matches PlaceOrderService's POST /orders path).
// Cancelling an already FILLED/CANCELLED order is a pure no-op read (idempotent,
// per spec §6): AlreadyTerminal is set, no outbox event is written. Logs
// per spec §7's correlation-by-id requirement (order_id/user_id).
func (s *CancelOrderService) Cancel(ctx context.Context, orderID, requestingUserID uuid.UUID) (result CancelResult, err error) {
	ctx, span := tracer.Start(ctx, "orders.cancel_order",
		trace.WithAttributes(
			attribute.String("order_id", orderID.String()),
			attribute.String("user_id", requestingUserID.String()),
		),
	)
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return CancelResult{}, err
	}
	defer tx.Rollback()

	o, err := s.orders.GetTx(ctx, tx, orderID)
	if err != nil {
		if errors.Is(err, postgres.ErrOrderNotFound) {
			return CancelResult{}, ErrOrderNotFound
		}
		return CancelResult{}, err
	}
	if o.UserID != requestingUserID {
		return CancelResult{}, ErrForbidden
	}
	if o.IsDone() {
		s.logger.Info("cancel request on already-terminal order (no-op)", zap.String("order_id", o.ID.String()), zap.String("status", string(o.Status)))
		return CancelResult{OrderID: o.ID, Status: o.Status, AlreadyTerminal: true}, nil
	}

	event := StreamEvent{
		Type:        EventTypeCancelOrder,
		OrderID:     o.ID,
		UserID:      o.UserID,
		RequestedBy: &requestingUserID,
	}
	InjectTraceContext(ctx, &event)
	payload, err := json.Marshal(event)
	if err != nil {
		return CancelResult{}, err
	}
	if err := s.outbox.Insert(ctx, tx, s.streamName, payload); err != nil {
		return CancelResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CancelResult{}, err
	}
	s.logger.Info("cancellation queued", zap.String("order_id", o.ID.String()), zap.String("requested_by", requestingUserID.String()))
	return CancelResult{OrderID: o.ID, Status: "CANCEL_QUEUED", AlreadyTerminal: false}, nil
}
