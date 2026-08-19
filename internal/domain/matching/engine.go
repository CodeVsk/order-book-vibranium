package matching

import (
	"trade-market/internal/domain/order"

	"github.com/google/uuid"
)

// Trade is the pure result of a single execution between two orders. The
// price is always the resting ("maker") order's price.
type Trade struct {
	BuyOrderID   uuid.UUID
	SellOrderID  uuid.UUID
	BuyerUserID  uuid.UUID
	SellerUserID uuid.UUID
	PriceCents   int64
	Quantity     int64
}

// MatchResult is the pure output of processing one incoming order. No I/O
// happens here; the caller (internal/application/matcherapp) persists
// Trades/Incoming/TouchedMakers and settles wallets inside a single
// Postgres transaction.
type MatchResult struct {
	Trades            []Trade
	Incoming          *order.Order
	TouchedMakers     []*order.Order // resting orders that changed (partial or full fill)
	RestedOnBook      bool           // true if Incoming now rests (LIMIT, remainder > 0)
	CancelledQuantity int64          // remainder cancelled for MARKET orders with leftover
}

// Match runs price-time priority matching for an incoming order against the
// book. For BUY MARKET orders, availableBuyerFundsCents must be a non-nil
// pointer to the buyer's currently available BRL balance (balance minus
// reserved), read once under SELECT ... FOR UPDATE by the caller before
// calling Match — see design decision #2 at the top of the plan for why a
// single upfront read is sufficient. The pointer's value is decremented in
// place as fills are simulated. For every other order type/side, pass nil.
func Match(book *Book, incoming *order.Order, availableBuyerFundsCents *int64) MatchResult {
	result := MatchResult{Incoming: incoming}

	for incoming.Remaining() > 0 {
		lvl := book.BestOpposite(incoming.Side)
		if lvl == nil {
			break
		}
		if incoming.Type == order.TypeLimit {
			if incoming.Side == order.SideBuy && *incoming.PriceCents < lvl.priceCents {
				break
			}
			if incoming.Side == order.SideSell && *incoming.PriceCents > lvl.priceCents {
				break
			}
		}

		maker := lvl.FrontOrder()
		if maker == nil {
			break
		}

		matchQty := min64(incoming.Remaining(), maker.Remaining())

		isBuyMarket := incoming.Side == order.SideBuy && incoming.Type == order.TypeMarket
		if isBuyMarket && availableBuyerFundsCents != nil {
			affordableQty := *availableBuyerFundsCents / lvl.priceCents
			if affordableQty < matchQty {
				matchQty = affordableQty
			}
		}
		if matchQty == 0 {
			break
		}

		trade := Trade{PriceCents: lvl.priceCents, Quantity: matchQty}
		if incoming.Side == order.SideBuy {
			trade.BuyOrderID, trade.BuyerUserID = incoming.ID, incoming.UserID
			trade.SellOrderID, trade.SellerUserID = maker.ID, maker.UserID
		} else {
			trade.SellOrderID, trade.SellerUserID = incoming.ID, incoming.UserID
			trade.BuyOrderID, trade.BuyerUserID = maker.ID, maker.UserID
		}
		result.Trades = append(result.Trades, trade)

		_ = incoming.Fill(matchQty)
		_ = maker.Fill(matchQty)
		result.TouchedMakers = append(result.TouchedMakers, maker)

		if isBuyMarket && availableBuyerFundsCents != nil {
			*availableBuyerFundsCents -= matchQty * lvl.priceCents
		}

		if maker.Remaining() == 0 {
			oppositeSide := order.SideSell
			if incoming.Side == order.SideSell {
				oppositeSide = order.SideBuy
			}
			book.removeFront(maker, oppositeSide, lvl)
		}
	}

	if incoming.Remaining() > 0 {
		if incoming.Type == order.TypeLimit {
			book.AddResting(incoming)
			result.RestedOnBook = true
		} else {
			result.CancelledQuantity = incoming.Remaining()
			incoming.Cancel()
		}
	}

	return result
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
