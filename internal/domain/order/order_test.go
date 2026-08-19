package order

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func newTestOrder(qty int64) *Order {
	price := int64(1000)
	return &Order{
		ID:         uuid.New(),
		UserID:     uuid.New(),
		Side:       SideBuy,
		Type:       TypeLimit,
		PriceCents: &price,
		Quantity:   qty,
		Status:     StatusOpen,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
}

func TestOrder_Fill_PartialThenFull(t *testing.T) {
	o := newTestOrder(10)
	if err := o.Fill(4); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Status != StatusPartiallyFilled || o.Remaining() != 6 {
		t.Fatalf("expected PARTIALLY_FILLED with 6 remaining, got status=%s remaining=%d", o.Status, o.Remaining())
	}
	if err := o.Fill(6); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Status != StatusFilled || o.Remaining() != 0 {
		t.Fatalf("expected FILLED with 0 remaining, got status=%s remaining=%d", o.Status, o.Remaining())
	}
}

func TestOrder_Fill_ExceedsRemaining(t *testing.T) {
	o := newTestOrder(5)
	if err := o.Fill(6); err != ErrFillExceedsRemaining {
		t.Fatalf("expected ErrFillExceedsRemaining, got %v", err)
	}
}

func TestOrder_Cancel_AlreadyFilledIsNoop(t *testing.T) {
	o := newTestOrder(10)
	_ = o.Fill(10)
	o.Cancel()
	if o.Status != StatusFilled {
		t.Fatalf("cancel on FILLED order must be no-op, got %s", o.Status)
	}
}

func TestOrder_Cancel_OpenOrderBecomesCancelled(t *testing.T) {
	o := newTestOrder(10)
	o.Cancel()
	if o.Status != StatusCancelled {
		t.Fatalf("expected CANCELLED, got %s", o.Status)
	}
}
