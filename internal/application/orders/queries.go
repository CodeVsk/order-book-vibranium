// internal/application/orders/queries.go
package orders

import (
	"context"

	"trade-market/internal/domain/order"
	"trade-market/internal/domain/trade"
	"trade-market/internal/domain/wallet"
	"trade-market/internal/infra/postgres"

	"github.com/google/uuid"
)

type GetOrderQuery struct {
	orders *postgres.OrderRepository
}

func NewGetOrderQuery(orders *postgres.OrderRepository) *GetOrderQuery {
	return &GetOrderQuery{orders: orders}
}

func (q *GetOrderQuery) Get(ctx context.Context, id uuid.UUID) (*order.Order, error) {
	return q.orders.Get(ctx, id)
}

type GetWalletQuery struct {
	wallets *postgres.WalletRepository
}

func NewGetWalletQuery(wallets *postgres.WalletRepository) *GetWalletQuery {
	return &GetWalletQuery{wallets: wallets}
}

func (q *GetWalletQuery) Get(ctx context.Context, userID uuid.UUID) (*wallet.Wallet, error) {
	return q.wallets.Get(ctx, userID)
}

type ListTradesQuery struct {
	trades *postgres.TradeRepository
}

func NewListTradesQuery(trades *postgres.TradeRepository) *ListTradesQuery {
	return &ListTradesQuery{trades: trades}
}

func (q *ListTradesQuery) List(ctx context.Context, f postgres.TradeFilter) ([]*trade.Trade, string, error) {
	return q.trades.ListPaginated(ctx, f)
}
