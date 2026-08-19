package order

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type Side string

const (
	SideBuy  Side = "BUY"
	SideSell Side = "SELL"
)

type Type string

const (
	TypeLimit  Type = "LIMIT"
	TypeMarket Type = "MARKET"
)

type Status string

const (
	StatusOpen            Status = "OPEN"
	StatusPartiallyFilled Status = "PARTIALLY_FILLED"
	StatusFilled          Status = "FILLED"
	StatusCancelled       Status = "CANCELLED"
)

var (
	ErrFillExceedsRemaining = errors.New("order: fill quantity exceeds remaining quantity")
	ErrInvalidQuantity      = errors.New("order: quantity must be positive")
)

// Order is the domain representation of a buy/sell order, shared by the
// matching engine's in-memory book and the persistence layer.
type Order struct {
	ID             uuid.UUID
	UserID         uuid.UUID
	Side           Side
	Type           Type
	PriceCents     *int64 // nil for MARKET orders
	Quantity       int64
	FilledQuantity int64
	Status         Status
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Remaining returns the quantity not yet filled.
func (o *Order) Remaining() int64 {
	return o.Quantity - o.FilledQuantity
}

// IsDone reports whether the order can no longer participate in matching.
func (o *Order) IsDone() bool {
	return o.Status == StatusFilled || o.Status == StatusCancelled
}

// Fill records a (partial) execution of qty units and updates status.
func (o *Order) Fill(qty int64) error {
	if qty <= 0 {
		return ErrInvalidQuantity
	}
	if qty > o.Remaining() {
		return ErrFillExceedsRemaining
	}
	o.FilledQuantity += qty
	if o.Remaining() == 0 {
		o.Status = StatusFilled
	} else {
		o.Status = StatusPartiallyFilled
	}
	o.UpdatedAt = time.Now().UTC()
	return nil
}

// Cancel marks the order CANCELLED. It is a no-op (idempotent) if the order
// is already FILLED or CANCELLED.
func (o *Order) Cancel() {
	if o.IsDone() {
		return
	}
	o.Status = StatusCancelled
	o.UpdatedAt = time.Now().UTC()
}
