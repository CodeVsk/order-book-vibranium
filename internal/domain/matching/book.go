package matching

import (
	"container/heap"
	"container/list"

	"trade-market/internal/domain/order"

	"github.com/google/uuid"
)

// level holds every resting order at one price point, in FIFO (price-time
// priority) order.
type level struct {
	priceCents int64
	orders     *list.List // *order.Order values
	index      int        // maintained by levelHeap for O(log n) removal
}

// levelHeap is a container/heap of price levels. less decides ordering:
// bids use price descending (best bid = highest price), asks use price
// ascending (best ask = lowest price).
type levelHeap struct {
	levels []*level
	less   func(a, b int64) bool
}

func (h *levelHeap) Len() int { return len(h.levels) }
func (h *levelHeap) Less(i, j int) bool {
	return h.less(h.levels[i].priceCents, h.levels[j].priceCents)
}
func (h *levelHeap) Swap(i, j int) {
	h.levels[i], h.levels[j] = h.levels[j], h.levels[i]
	h.levels[i].index = i
	h.levels[j].index = j
}
func (h *levelHeap) Push(x any) {
	lvl := x.(*level)
	lvl.index = len(h.levels)
	h.levels = append(h.levels, lvl)
}
func (h *levelHeap) Pop() any {
	old := h.levels
	n := len(old)
	lvl := old[n-1]
	old[n-1] = nil
	h.levels = old[:n-1]
	return lvl
}

func (h *levelHeap) peek() *level {
	if len(h.levels) == 0 {
		return nil
	}
	return h.levels[0]
}

type orderLocation struct {
	side order.Side
	lvl  *level
	elem *list.Element
}

// Book is the single in-memory order book for one symbol (Vibranium). It is
// NOT safe for concurrent use — the matcher consumer guarantees a single
// writer goroutine, per design (this is the invariant that resolves the
// challenge's core contradiction between "the book admits no concurrency"
// and "thousands of orders may arrive in the same millisecond").
type Book struct {
	bids        *levelHeap
	asks        *levelHeap
	bidsByPrice map[int64]*level
	asksByPrice map[int64]*level
	locations   map[uuid.UUID]*orderLocation
}

func NewBook() *Book {
	return &Book{
		bids:        &levelHeap{less: func(a, b int64) bool { return a > b }},
		asks:        &levelHeap{less: func(a, b int64) bool { return a < b }},
		bidsByPrice: map[int64]*level{},
		asksByPrice: map[int64]*level{},
		locations:   map[uuid.UUID]*orderLocation{},
	}
}

func (b *Book) heapAndIndex(side order.Side) (*levelHeap, map[int64]*level) {
	if side == order.SideBuy {
		return b.bids, b.bidsByPrice
	}
	return b.asks, b.asksByPrice
}

// BestOpposite returns the best resting price level on the side opposite to
// the given incoming order side, or nil if that side of the book is empty.
func (b *Book) BestOpposite(incomingSide order.Side) *level {
	if incomingSide == order.SideBuy {
		return b.asks.peek()
	}
	return b.bids.peek()
}

// FrontOrder returns the oldest order at a level without removing it.
func (lv *level) FrontOrder() *order.Order {
	if lv == nil || lv.orders.Len() == 0 {
		return nil
	}
	return lv.orders.Front().Value.(*order.Order)
}

// AddResting inserts an order at the back of its price level's FIFO queue,
// creating the level if necessary. PriceCents must be non-nil.
func (b *Book) AddResting(o *order.Order) {
	h, idx := b.heapAndIndex(o.Side)
	price := *o.PriceCents
	lvl, ok := idx[price]
	if !ok {
		lvl = &level{priceCents: price, orders: list.New()}
		idx[price] = lvl
		heap.Push(h, lvl)
	}
	elem := lvl.orders.PushBack(o)
	b.locations[o.ID] = &orderLocation{side: o.Side, lvl: lvl, elem: elem}
}

// removeFront removes a matched-away order from a level and prunes the
// level from the book if it becomes empty.
func (b *Book) removeFront(o *order.Order, side order.Side, lvl *level) {
	loc, ok := b.locations[o.ID]
	if !ok {
		return
	}
	lvl.orders.Remove(loc.elem)
	delete(b.locations, o.ID)
	if lvl.orders.Len() == 0 {
		h, idx := b.heapAndIndex(side)
		delete(idx, lvl.priceCents)
		heap.Remove(h, lvl.index)
	}
}

// Cancel removes a resting order from the book, if present. Returns
// (order, true) if found and removed, (nil, false) otherwise (already
// matched away, never rested — e.g. a MARKET order — or unknown id).
func (b *Book) Cancel(id uuid.UUID) (*order.Order, bool) {
	loc, ok := b.locations[id]
	if !ok {
		return nil, false
	}
	o := loc.elem.Value.(*order.Order)
	b.removeFront(o, loc.side, loc.lvl)
	return o, true
}
