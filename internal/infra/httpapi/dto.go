package httpapi

import "time"

// maxAmountCents/maxQuantity bound PriceCents and Quantity so their product
// (used as int64 in wallet reservation cost, trade settlement, and
// matching-engine affordability checks) can never overflow int64
// (max ~9.22e18). Both fields are bounded by 3_000_000_000 (3 billion),
// so the worst-case product is 9e18, safely under the int64 limit.
type PlaceOrderRequest struct {
	UserID     string `json:"user_id" validate:"required,uuid"`
	Side       string `json:"side" validate:"required,oneof=BUY SELL"`
	Type       string `json:"type" validate:"required,oneof=LIMIT MARKET"`
	PriceCents *int64 `json:"price_cents" validate:"required_if=Type LIMIT,omitempty,gt=0,lte=3000000000"`
	Quantity   int64  `json:"quantity" validate:"required,gt=0,lte=3000000000"`
}

type PlaceOrderResponse struct {
	OrderID   string    `json:"order_id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type CancelOrderRequest struct {
	UserID string `json:"user_id" validate:"required,uuid"`
}

type CancelOrderResponse struct {
	OrderID string `json:"order_id"`
	Status  string `json:"status"`
}

type GetOrderResponse struct {
	OrderID        string    `json:"order_id"`
	Status         string    `json:"status"`
	FilledQuantity int64     `json:"filled_quantity"`
	Quantity       int64     `json:"quantity"`
	PriceCents     *int64    `json:"price_cents"`
	Side           string    `json:"side"`
	Type           string    `json:"type"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type GetWalletResponse struct {
	UserID             string `json:"user_id"`
	BalanceBRLCents    int64  `json:"balance_brl_cents"`
	ReservedBRLCents   int64  `json:"reserved_brl_cents"`
	AvailableBRLCents  int64  `json:"available_brl_cents"`
	BalanceVibranium   int64  `json:"balance_vibranium"`
	ReservedVibranium  int64  `json:"reserved_vibranium"`
	AvailableVibranium int64  `json:"available_vibranium"`
}

type TradeDTO struct {
	TradeID     string    `json:"trade_id"`
	BuyOrderID  string    `json:"buy_order_id"`
	SellOrderID string    `json:"sell_order_id"`
	PriceCents  int64     `json:"price_cents"`
	Quantity    int64     `json:"quantity"`
	ExecutedAt  time.Time `json:"executed_at"`
}

type ListTradesResponse struct {
	Trades     []TradeDTO `json:"trades"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

type UserDTO struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
}

type ListUsersResponse struct {
	Users []UserDTO `json:"users"`
}

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
