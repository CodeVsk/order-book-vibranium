package matching

import (
	"testing"

	"trade-market/internal/domain/order"

	"github.com/google/uuid"
)

func TestBook_AddResting_BestOppositeReturnsBestPrice(t *testing.T) {
	book := NewBook()
	book.AddResting(newOrder(order.SideSell, order.TypeLimit, p(1020), 5))
	book.AddResting(newOrder(order.SideSell, order.TypeLimit, p(1000), 5)) // better (lower) ask
	book.AddResting(newOrder(order.SideSell, order.TypeLimit, p(1010), 5))

	best := book.BestOpposite(order.SideBuy)
	if best == nil || best.priceCents != 1000 {
		t.Fatalf("expected best ask 1000, got %+v", best)
	}
}

func TestBook_Cancel_RemovesRestingOrder(t *testing.T) {
	book := NewBook()
	sell := newOrder(order.SideSell, order.TypeLimit, p(1000), 10)
	book.AddResting(sell)

	removed, found := book.Cancel(sell.ID)
	if !found || removed.ID != sell.ID {
		t.Fatalf("expected to find and remove sell order")
	}
	if book.BestOpposite(order.SideBuy) != nil {
		t.Fatalf("book should be empty on that side after removing the only order")
	}
}

func TestBook_Cancel_UnknownOrderIsNoop(t *testing.T) {
	book := NewBook()
	_, found := book.Cancel(uuid.New())
	if found {
		t.Fatalf("expected not found for unknown order id")
	}
}

func TestBook_EmptyLevelIsPrunedAfterAllOrdersRemoved(t *testing.T) {
	book := NewBook()
	s1 := newOrder(order.SideSell, order.TypeLimit, p(1000), 5)
	s2 := newOrder(order.SideSell, order.TypeLimit, p(1000), 5)
	book.AddResting(s1)
	book.AddResting(s2)

	book.Cancel(s1.ID)
	book.Cancel(s2.ID)

	if book.BestOpposite(order.SideBuy) != nil {
		t.Fatalf("expected level 1000 to be pruned once empty")
	}
}
