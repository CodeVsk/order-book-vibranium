package trade

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNew_RejectsNonPositivePriceOrQuantity(t *testing.T) {
	ids := [5]uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	cases := []struct {
		name       string
		priceCents int64
		quantity   int64
	}{
		{"zero price", 0, 10},
		{"negative price", -100, 10},
		{"zero quantity", 1000, 0},
		{"negative quantity", 1000, -5},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(ids[0], ids[1], ids[2], ids[3], ids[4], tt.priceCents, tt.quantity, time.Now())
			if err != ErrInvalidTrade {
				t.Fatalf("expected ErrInvalidTrade, got %v", err)
			}
		})
	}
}

func TestNew_ValidTrade(t *testing.T) {
	ids := [5]uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	now := time.Now()
	tr, err := New(ids[0], ids[1], ids[2], ids[3], ids[4], 1000, 5, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr.PriceCents != 1000 || tr.Quantity != 5 || tr.ExecutedAt != now {
		t.Fatalf("unexpected trade: %+v", tr)
	}
}
