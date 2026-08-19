package trade

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrInvalidTrade = errors.New("trade: price and quantity must be positive")

// Trade is an immutable record of one execution between a buy and a sell
// order. Price is always the resting ("maker") order's price.
type Trade struct {
	ID           uuid.UUID
	BuyOrderID   uuid.UUID
	SellOrderID  uuid.UUID
	BuyerUserID  uuid.UUID
	SellerUserID uuid.UUID
	PriceCents   int64
	Quantity     int64
	ExecutedAt   time.Time
}

func New(id, buyOrderID, sellOrderID, buyerUserID, sellerUserID uuid.UUID, priceCents, quantity int64, executedAt time.Time) (*Trade, error) {
	if priceCents <= 0 || quantity <= 0 {
		return nil, ErrInvalidTrade
	}
	return &Trade{
		ID:           id,
		BuyOrderID:   buyOrderID,
		SellOrderID:  sellOrderID,
		BuyerUserID:  buyerUserID,
		SellerUserID: sellerUserID,
		PriceCents:   priceCents,
		Quantity:     quantity,
		ExecutedAt:   executedAt,
	}, nil
}
