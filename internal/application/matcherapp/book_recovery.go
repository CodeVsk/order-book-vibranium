// internal/application/matcherapp/book_recovery.go
package matcherapp

import (
	"context"

	"trade-market/internal/domain/matching"
	"trade-market/internal/domain/order"
	"trade-market/internal/infra/postgres"
)

// RecoverBook rebuilds the in-memory Book from every order still
// OPEN/PARTIALLY_FILLED in Postgres, oldest first — how the matcher
// survives a crash/restart without losing the resting book (Postgres is
// the source of truth; the in-memory book is a derived cache). MARKET
// orders are filtered out as defense-in-depth: PlaceOrderService inserts
// them with status OPEN before the matcher processes their stream entry,
// so a stray row can transiently exist here even though MARKET orders can
// never legitimately rest on the book (they have no price to rest at).
func RecoverBook(ctx context.Context, orderRepo *postgres.OrderRepository) (*matching.Book, error) {
	openOrders, err := orderRepo.ListOpenForRecovery(ctx)
	if err != nil {
		return nil, err
	}
	book := matching.NewBook()
	for _, o := range openOrders {
		if o.Type == order.TypeLimit {
			book.AddResting(o)
		}
	}
	return book, nil
}
