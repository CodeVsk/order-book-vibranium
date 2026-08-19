package orders

import (
	"testing"

	"trade-market/internal/domain/order"
)

func p(v int64) *int64 { return &v }

func TestValidatePlaceOrderInput(t *testing.T) {
	tests := []struct {
		name    string
		in      PlaceOrderInput
		wantErr error
	}{
		{"valid buy limit", PlaceOrderInput{Side: order.SideBuy, Type: order.TypeLimit, PriceCents: p(1000), Quantity: 5}, nil},
		{"valid sell limit", PlaceOrderInput{Side: order.SideSell, Type: order.TypeLimit, PriceCents: p(1000), Quantity: 5}, nil},
		{"valid sell market", PlaceOrderInput{Side: order.SideSell, Type: order.TypeMarket, Quantity: 5}, nil},
		{"valid buy market", PlaceOrderInput{Side: order.SideBuy, Type: order.TypeMarket, Quantity: 5}, nil},
		{"zero quantity", PlaceOrderInput{Side: order.SideBuy, Type: order.TypeLimit, PriceCents: p(1000), Quantity: 0}, ErrInvalidQuantity},
		{"negative quantity", PlaceOrderInput{Side: order.SideBuy, Type: order.TypeLimit, PriceCents: p(1000), Quantity: -1}, ErrInvalidQuantity},
		{"buy limit nil price", PlaceOrderInput{Side: order.SideBuy, Type: order.TypeLimit, Quantity: 5}, ErrInvalidOrderRequest},
		{"buy limit zero price", PlaceOrderInput{Side: order.SideBuy, Type: order.TypeLimit, PriceCents: p(0), Quantity: 5}, ErrInvalidOrderRequest},
		{"sell limit nil price", PlaceOrderInput{Side: order.SideSell, Type: order.TypeLimit, Quantity: 5}, ErrInvalidOrderRequest},
		{"buy market with price set", PlaceOrderInput{Side: order.SideBuy, Type: order.TypeMarket, PriceCents: p(1000), Quantity: 5}, ErrInvalidOrderRequest},
		{"sell market with price set", PlaceOrderInput{Side: order.SideSell, Type: order.TypeMarket, PriceCents: p(1000), Quantity: 5}, ErrInvalidOrderRequest},
		{"buy limit overflow", PlaceOrderInput{Side: order.SideBuy, Type: order.TypeLimit, PriceCents: p(9223372036854775807), Quantity: 2}, ErrInvalidOrderRequest},
		{"invalid side", PlaceOrderInput{Side: "INVALID", Type: order.TypeLimit, PriceCents: p(1000), Quantity: 5}, ErrInvalidOrderRequest},
		{"invalid type", PlaceOrderInput{Side: order.SideBuy, Type: "INVALID", PriceCents: p(1000), Quantity: 5}, ErrInvalidOrderRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePlaceOrderInput(tt.in)
			if err != tt.wantErr {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}
