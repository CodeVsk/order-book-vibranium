package httpapi

import (
	"testing"

	"github.com/go-playground/validator/v10"
)

func int64Ptr(v int64) *int64 {
	return &v
}

func TestPlaceOrderRequest_Validate(t *testing.T) {
	validate := validator.New()

	tests := []struct {
		name    string
		req     PlaceOrderRequest
		wantErr bool
	}{
		{
			name: "valid BUY LIMIT",
			req: PlaceOrderRequest{
				UserID:     "3f333df6-90a4-4fda-8dd3-9485d27cee36",
				Side:       "BUY",
				Type:       "LIMIT",
				PriceCents: int64Ptr(1000),
				Quantity:   10,
			},
			wantErr: false,
		},
		{
			name: "valid BUY MARKET nil price",
			req: PlaceOrderRequest{
				UserID:     "3f333df6-90a4-4fda-8dd3-9485d27cee36",
				Side:       "BUY",
				Type:       "MARKET",
				PriceCents: nil,
				Quantity:   10,
			},
			wantErr: false,
		},
		{
			name: "missing price on LIMIT",
			req: PlaceOrderRequest{
				UserID:     "3f333df6-90a4-4fda-8dd3-9485d27cee36",
				Side:       "SELL",
				Type:       "LIMIT",
				PriceCents: nil,
				Quantity:   10,
			},
			wantErr: true,
		},
		{
			name: "non-positive quantity",
			req: PlaceOrderRequest{
				UserID:     "3f333df6-90a4-4fda-8dd3-9485d27cee36",
				Side:       "BUY",
				Type:       "MARKET",
				PriceCents: nil,
				Quantity:   0,
			},
			wantErr: true,
		},
		{
			name: "negative quantity",
			req: PlaceOrderRequest{
				UserID:     "3f333df6-90a4-4fda-8dd3-9485d27cee36",
				Side:       "BUY",
				Type:       "MARKET",
				PriceCents: nil,
				Quantity:   -5,
			},
			wantErr: true,
		},
		{
			name: "quantity exceeding bound",
			req: PlaceOrderRequest{
				UserID:     "3f333df6-90a4-4fda-8dd3-9485d27cee36",
				Side:       "BUY",
				Type:       "MARKET",
				PriceCents: nil,
				Quantity:   3000000001,
			},
			wantErr: true,
		},
		{
			name: "quantity at bound is valid",
			req: PlaceOrderRequest{
				UserID:     "3f333df6-90a4-4fda-8dd3-9485d27cee36",
				Side:       "BUY",
				Type:       "MARKET",
				PriceCents: nil,
				Quantity:   3000000000,
			},
			wantErr: false,
		},
		{
			name: "price exceeding bound on LIMIT",
			req: PlaceOrderRequest{
				UserID:     "3f333df6-90a4-4fda-8dd3-9485d27cee36",
				Side:       "SELL",
				Type:       "LIMIT",
				PriceCents: int64Ptr(3000000001),
				Quantity:   10,
			},
			wantErr: true,
		},
		{
			name: "price at bound on LIMIT is valid",
			req: PlaceOrderRequest{
				UserID:     "3f333df6-90a4-4fda-8dd3-9485d27cee36",
				Side:       "SELL",
				Type:       "LIMIT",
				PriceCents: int64Ptr(3000000000),
				Quantity:   10,
			},
			wantErr: false,
		},
		{
			name: "invalid UUID for user_id",
			req: PlaceOrderRequest{
				UserID:     "not-a-uuid",
				Side:       "BUY",
				Type:       "MARKET",
				PriceCents: nil,
				Quantity:   10,
			},
			wantErr: true,
		},
		{
			name: "invalid side enum",
			req: PlaceOrderRequest{
				UserID:     "3f333df6-90a4-4fda-8dd3-9485d27cee36",
				Side:       "HOLD",
				Type:       "MARKET",
				PriceCents: nil,
				Quantity:   10,
			},
			wantErr: true,
		},
		{
			name: "invalid type enum",
			req: PlaceOrderRequest{
				UserID:     "3f333df6-90a4-4fda-8dd3-9485d27cee36",
				Side:       "BUY",
				Type:       "STOP",
				PriceCents: nil,
				Quantity:   10,
			},
			wantErr: true,
		},
		{
			name: "price present on MARKET is not itself rejected by tags",
			req: PlaceOrderRequest{
				UserID:     "3f333df6-90a4-4fda-8dd3-9485d27cee36",
				Side:       "BUY",
				Type:       "MARKET",
				PriceCents: int64Ptr(500),
				Quantity:   10,
			},
			wantErr: false,
		},
		{
			name: "non-positive price on LIMIT",
			req: PlaceOrderRequest{
				UserID:     "3f333df6-90a4-4fda-8dd3-9485d27cee36",
				Side:       "BUY",
				Type:       "LIMIT",
				PriceCents: int64Ptr(0),
				Quantity:   10,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validate.Struct(tt.req)
			if tt.wantErr && err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected no validation error, got: %v", err)
			}
		})
	}
}

func TestCancelOrderRequest_Validate(t *testing.T) {
	validate := validator.New()

	tests := []struct {
		name    string
		req     CancelOrderRequest
		wantErr bool
	}{
		{
			name:    "valid uuid",
			req:     CancelOrderRequest{UserID: "3f333df6-90a4-4fda-8dd3-9485d27cee36"},
			wantErr: false,
		},
		{
			name:    "invalid uuid",
			req:     CancelOrderRequest{UserID: "abc"},
			wantErr: true,
		},
		{
			name:    "missing user id",
			req:     CancelOrderRequest{UserID: ""},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validate.Struct(tt.req)
			if tt.wantErr && err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected no validation error, got: %v", err)
			}
		})
	}
}
