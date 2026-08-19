// internal/application/orders/place_order_service.go
package orders

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"trade-market/internal/domain/order"
	"trade-market/internal/infra/postgres"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

var tracer = otel.Tracer("trade-market/orders")

var (
	ErrUserNotFound        = errors.New("orders: user_id does not exist")
	ErrWalletNotFound      = errors.New("orders: user has no wallet provisioned")
	ErrInsufficientBalance = errors.New("orders: insufficient available balance")
	ErrInvalidQuantity     = errors.New("orders: quantity must be positive")
	ErrInvalidOrderRequest = errors.New("orders: invalid side/type combination or price/quantity mismatch")
)

type PlaceOrderInput struct {
	UserID     uuid.UUID
	Side       order.Side
	Type       order.Type
	PriceCents *int64 // required for LIMIT, nil for MARKET
	Quantity   int64
}

type PlaceOrderService struct {
	db         *sqlx.DB
	wallets    *postgres.WalletRepository
	users      *postgres.UserRepository
	orders     *postgres.OrderRepository
	outbox     *postgres.OutboxRepository
	streamName string
	logger     *zap.Logger
}

func NewPlaceOrderService(db *sqlx.DB, wallets *postgres.WalletRepository, users *postgres.UserRepository, orders *postgres.OrderRepository, outbox *postgres.OutboxRepository, streamName string, logger *zap.Logger) *PlaceOrderService {
	return &PlaceOrderService{db: db, wallets: wallets, users: users, orders: orders, outbox: outbox, streamName: streamName, logger: logger}
}

// validatePlaceOrderInput enforces invariants that must hold regardless of
// whether the caller (HTTP layer) already validated the request — this is
// a funds-reservation code path and should not rely solely on an upstream
// validator. Returns nil if in is well-formed.
func validatePlaceOrderInput(in PlaceOrderInput) error {
	if in.Quantity <= 0 {
		return ErrInvalidQuantity
	}
	switch {
	case in.Side == order.SideBuy && in.Type == order.TypeLimit:
		if in.PriceCents == nil || *in.PriceCents <= 0 {
			return ErrInvalidOrderRequest
		}
		if *in.PriceCents > math.MaxInt64/in.Quantity {
			return ErrInvalidOrderRequest // would overflow int64 cost calculation
		}
	case in.Side == order.SideSell && in.Type == order.TypeLimit:
		if in.PriceCents == nil || *in.PriceCents <= 0 {
			return ErrInvalidOrderRequest
		}
	case in.Side == order.SideSell && in.Type == order.TypeMarket:
		if in.PriceCents != nil {
			return ErrInvalidOrderRequest
		}
	case in.Side == order.SideBuy && in.Type == order.TypeMarket:
		if in.PriceCents != nil {
			return ErrInvalidOrderRequest
		}
	default:
		return ErrInvalidOrderRequest
	}
	return nil
}

// Place validates the user_id and funds/holdings (synchronously reserving
// funds for every case except BUY MARKET, per spec §5), persists the order,
// and writes the outbox event — all inside one Postgres transaction. Returns
// ErrUserNotFound (-> 404, user_id not in the users table), ErrWalletNotFound
// (-> 404, user exists but has no wallet provisioned), ErrInsufficientBalance
// (-> 409), or a generic error (-> 500); the caller (HTTP handler) maps these
// to status codes. Logs "order received" and "reservation applied" per spec
// §7's correlation-by-id requirement (order_id/user_id).
func (s *PlaceOrderService) Place(ctx context.Context, in PlaceOrderInput) (o *order.Order, err error) {
	ctx, span := tracer.Start(ctx, "orders.place_order",
		trace.WithAttributes(
			attribute.String("user_id", in.UserID.String()),
			attribute.String("side", string(in.Side)),
			attribute.String("order_type", string(in.Type)),
		),
	)
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	s.logger.Info("order received", zap.String("user_id", in.UserID.String()), zap.String("side", string(in.Side)), zap.String("type", string(in.Type)), zap.Int64("quantity", in.Quantity))

	if err := validatePlaceOrderInput(in); err != nil {
		return nil, err
	}

	// Cheap existence check outside any transaction — rejects unknown
	// user_ids before we pay for BeginTxx/row locking.
	exists, err := s.users.Exists(ctx, in.UserID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrUserNotFound
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	w, err := s.wallets.GetForUpdate(ctx, tx, in.UserID)
	if err != nil {
		if errors.Is(err, postgres.ErrWalletNotFound) {
			return nil, ErrWalletNotFound
		}
		return nil, err
	}

	now := time.Now().UTC()
	o = &order.Order{
		ID:         uuid.New(),
		UserID:     in.UserID,
		Side:       in.Side,
		Type:       in.Type,
		PriceCents: in.PriceCents,
		Quantity:   in.Quantity,
		Status:     order.StatusOpen,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	span.SetAttributes(attribute.String("order_id", o.ID.String()))

	switch {
	case in.Side == order.SideBuy && in.Type == order.TypeLimit:
		cost := *in.PriceCents * in.Quantity
		if err := w.ReserveBRL(cost); err != nil {
			return nil, ErrInsufficientBalance
		}
	case in.Side == order.SideSell:
		if err := w.ReserveVibranium(in.Quantity); err != nil {
			return nil, ErrInsufficientBalance
		}
	case in.Side == order.SideBuy && in.Type == order.TypeMarket:
		// No reservation — see design decision #1/#2. Affordability is
		// checked live by the matcher at match time.
	}

	if err := s.orders.Insert(ctx, tx, o); err != nil {
		return nil, err
	}
	if err := s.wallets.Update(ctx, tx, w); err != nil {
		return nil, err
	}
	s.logger.Info("reservation applied", zap.String("order_id", o.ID.String()), zap.String("user_id", o.UserID.String()))

	event := StreamEvent{
		Type:       EventTypeNewOrder,
		OrderID:    o.ID,
		UserID:     o.UserID,
		Side:       o.Side,
		OrderType:  o.Type,
		PriceCents: o.PriceCents,
		Quantity:   o.Quantity,
	}
	InjectTraceContext(ctx, &event)
	payload, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	if err := s.outbox.Insert(ctx, tx, s.streamName, payload); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.logger.Info("order event written to outbox", zap.String("order_id", o.ID.String()))
	return o, nil
}
