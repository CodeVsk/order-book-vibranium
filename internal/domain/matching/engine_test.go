package matching

import (
	"testing"
	"time"

	"trade-market/internal/domain/order"

	"github.com/google/uuid"
)

func newOrder(side order.Side, typ order.Type, price *int64, qty int64) *order.Order {
	return &order.Order{
		ID:         uuid.New(),
		UserID:     uuid.New(),
		Side:       side,
		Type:       typ,
		PriceCents: price,
		Quantity:   qty,
		Status:     order.StatusOpen,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
}

func p(v int64) *int64 { return &v }

func TestMatch_FullMatch_SamePrice(t *testing.T) {
	book := NewBook()
	sell := newOrder(order.SideSell, order.TypeLimit, p(1000), 10)
	book.AddResting(sell)

	buy := newOrder(order.SideBuy, order.TypeLimit, p(1000), 10)
	res := Match(book, buy, nil)

	if len(res.Trades) != 1 || res.Trades[0].Quantity != 10 || res.Trades[0].PriceCents != 1000 {
		t.Fatalf("expected one trade qty=10 price=1000, got %+v", res.Trades)
	}
	if buy.Status != order.StatusFilled || sell.Status != order.StatusFilled {
		t.Fatalf("expected both orders FILLED, got buy=%s sell=%s", buy.Status, sell.Status)
	}
	if res.RestedOnBook {
		t.Fatalf("fully matched order should not rest")
	}
}

func TestMatch_PartialFill_PriceTimePriority(t *testing.T) {
	book := NewBook()
	sell1 := newOrder(order.SideSell, order.TypeLimit, p(1000), 5)
	sell2 := newOrder(order.SideSell, order.TypeLimit, p(1000), 5)
	book.AddResting(sell1)
	book.AddResting(sell2) // same price, later in FIFO

	buy := newOrder(order.SideBuy, order.TypeLimit, p(1000), 7)
	res := Match(book, buy, nil)

	if len(res.Trades) != 2 {
		t.Fatalf("expected 2 trades (FIFO across sell1 then sell2), got %d", len(res.Trades))
	}
	if res.Trades[0].SellOrderID != sell1.ID || res.Trades[0].Quantity != 5 {
		t.Fatalf("expected first trade to fully consume sell1 (oldest), got %+v", res.Trades[0])
	}
	if res.Trades[1].SellOrderID != sell2.ID || res.Trades[1].Quantity != 2 {
		t.Fatalf("expected second trade to partially consume sell2, got %+v", res.Trades[1])
	}
	if sell1.Status != order.StatusFilled {
		t.Fatalf("sell1 should be FILLED")
	}
	if sell2.Status != order.StatusPartiallyFilled || sell2.Remaining() != 3 {
		t.Fatalf("sell2 should be PARTIALLY_FILLED with 3 remaining, got status=%s remaining=%d", sell2.Status, sell2.Remaining())
	}
	if buy.Status != order.StatusFilled {
		t.Fatalf("buy should be FILLED")
	}
}

func TestMatch_NoMatch_PriceIncompatible(t *testing.T) {
	book := NewBook()
	sell := newOrder(order.SideSell, order.TypeLimit, p(1100), 10)
	book.AddResting(sell)

	buy := newOrder(order.SideBuy, order.TypeLimit, p(1000), 10)
	res := Match(book, buy, nil)

	if len(res.Trades) != 0 {
		t.Fatalf("expected no trades, got %+v", res.Trades)
	}
	if !res.RestedOnBook || buy.Status != order.StatusOpen {
		t.Fatalf("buy should rest on book untouched, got rested=%v status=%s", res.RestedOnBook, buy.Status)
	}
}

func TestMatch_MarketOrder_ConsumesMultipleLevels(t *testing.T) {
	book := NewBook()
	book.AddResting(newOrder(order.SideSell, order.TypeLimit, p(1000), 3))
	book.AddResting(newOrder(order.SideSell, order.TypeLimit, p(1010), 3))
	book.AddResting(newOrder(order.SideSell, order.TypeLimit, p(1020), 3))

	buy := newOrder(order.SideBuy, order.TypeMarket, nil, 7)
	res := Match(book, buy, nil)

	if len(res.Trades) != 3 {
		t.Fatalf("expected 3 trades across 3 levels, got %d", len(res.Trades))
	}
	total := int64(0)
	for _, tr := range res.Trades {
		total += tr.Quantity
	}
	if total != 7 {
		t.Fatalf("expected total matched qty 7, got %d", total)
	}
	if buy.Status != order.StatusFilled {
		t.Fatalf("market buy should be FILLED, got %s", buy.Status)
	}
}

func TestMatch_BuyMarket_StopsOnInsufficientFunds(t *testing.T) {
	book := NewBook()
	book.AddResting(newOrder(order.SideSell, order.TypeLimit, p(1000), 5)) // costs 5000 for all
	book.AddResting(newOrder(order.SideSell, order.TypeLimit, p(1010), 5))

	funds := int64(3500) // affords 3 units at 1000, then nothing more at that level
	buy := newOrder(order.SideBuy, order.TypeMarket, nil, 10)
	res := Match(book, buy, &funds)

	if len(res.Trades) != 1 || res.Trades[0].Quantity != 3 {
		t.Fatalf("expected single trade of 3 units, got %+v", res.Trades)
	}
	if funds != 500 {
		t.Fatalf("expected 500 cents left unspent, got %d", funds)
	}
	if res.CancelledQuantity != 7 {
		t.Fatalf("expected remaining 7 units cancelled, got %d", res.CancelledQuantity)
	}
	if buy.Status != order.StatusCancelled {
		t.Fatalf("expected market remainder to end CANCELLED, got %s", buy.Status)
	}
	if buy.FilledQuantity != 3 {
		t.Fatalf("expected filled_quantity=3 retained after cancellation, got %d", buy.FilledQuantity)
	}
}

func TestMatch_SellMarket_NoLiquidity_CancelsImmediately(t *testing.T) {
	book := NewBook() // empty book, no bids at all
	sell := newOrder(order.SideSell, order.TypeMarket, nil, 10)
	res := Match(book, sell, nil)

	if len(res.Trades) != 0 {
		t.Fatalf("expected no trades, got %+v", res.Trades)
	}
	if res.CancelledQuantity != 10 || sell.Status != order.StatusCancelled {
		t.Fatalf("expected full quantity cancelled, got cancelled=%d status=%s", res.CancelledQuantity, sell.Status)
	}
}

func TestBook_Cancel_ThenReMatch_NoTrade(t *testing.T) {
	book := NewBook()
	sell := newOrder(order.SideSell, order.TypeLimit, p(1000), 10)
	book.AddResting(sell)
	book.Cancel(sell.ID)

	buy := newOrder(order.SideBuy, order.TypeLimit, p(1000), 10)
	res := Match(book, buy, nil)
	if len(res.Trades) != 0 {
		t.Fatalf("cancelled order must not match, got %+v", res.Trades)
	}
}

func TestOrder_Cancel_AlreadyFilledIsNoop_ViaEngine(t *testing.T) {
	o := newOrder(order.SideBuy, order.TypeLimit, p(1000), 10)
	_ = o.Fill(10)
	o.Cancel()
	if o.Status != order.StatusFilled {
		t.Fatalf("cancel on FILLED order must be no-op, got %s", o.Status)
	}
}
