// internal/infra/postgres/trade_repo.go
package postgres

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"trade-market/internal/domain/trade"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

var ErrInvalidCursor = errors.New("postgres: invalid trade cursor")

type TradeRepository struct {
	db *sqlx.DB
}

func NewTradeRepository(db *sqlx.DB) *TradeRepository {
	return &TradeRepository{db: db}
}

type tradeRow struct {
	ID           uuid.UUID `db:"id"`
	BuyOrderID   uuid.UUID `db:"buy_order_id"`
	SellOrderID  uuid.UUID `db:"sell_order_id"`
	BuyerUserID  uuid.UUID `db:"buyer_user_id"`
	SellerUserID uuid.UUID `db:"seller_user_id"`
	PriceCents   int64     `db:"price_cents"`
	Quantity     int64     `db:"quantity"`
	ExecutedAt   time.Time `db:"executed_at"`
}

// Insert persists a trade inside tx (called by the matcher consumer, same
// transaction as the wallet settlements and order updates it produced).
func (r *TradeRepository) Insert(ctx context.Context, tx *sqlx.Tx, t *trade.Trade) error {
	const q = `INSERT INTO trades (id, buy_order_id, sell_order_id, buyer_user_id, seller_user_id, price_cents, quantity, executed_at)
	           VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	_, err := tx.ExecContext(ctx, q, t.ID, t.BuyOrderID, t.SellOrderID, t.BuyerUserID, t.SellerUserID, t.PriceCents, t.Quantity, t.ExecutedAt)
	return err
}

// TradeFilter narrows GET /trades results. Zero-value UUID fields mean "no
// filter" for that field.
type TradeFilter struct {
	UserID  *uuid.UUID
	OrderID *uuid.UUID
	Limit   int
	Cursor  string // opaque, from a previous ListPaginated call's NextCursor
}

// ListPaginated returns trades ordered by executed_at DESC, id DESC (most
// recent first) using keyset pagination, plus an opaque cursor for the next
// page (empty string if there are no more results).
func (r *TradeRepository) ListPaginated(ctx context.Context, f TradeFilter) ([]*trade.Trade, string, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var conditions []string
	args := []any{}
	argN := 1

	if f.UserID != nil {
		conditions = append(conditions, fmt.Sprintf("(buyer_user_id = $%d OR seller_user_id = $%d)", argN, argN))
		args = append(args, *f.UserID)
		argN++
	}
	if f.OrderID != nil {
		conditions = append(conditions, fmt.Sprintf("(buy_order_id = $%d OR sell_order_id = $%d)", argN, argN))
		args = append(args, *f.OrderID)
		argN++
	}
	if f.Cursor != "" {
		executedAt, id, err := decodeTradeCursor(f.Cursor)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrInvalidCursor, err)
		}
		conditions = append(conditions, fmt.Sprintf("(executed_at, id) < ($%d, $%d)", argN, argN+1))
		args = append(args, executedAt, id)
		argN += 2
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}
	args = append(args, limit+1) // fetch one extra to know if there's a next page

	q := fmt.Sprintf(`SELECT id, buy_order_id, sell_order_id, buyer_user_id, seller_user_id, price_cents, quantity, executed_at
	                   FROM trades %s ORDER BY executed_at DESC, id DESC LIMIT $%d`, where, argN)

	var rows []tradeRow
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, "", err
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	out := make([]*trade.Trade, 0, len(rows))
	for _, row := range rows {
		t, err := trade.New(row.ID, row.BuyOrderID, row.SellOrderID, row.BuyerUserID, row.SellerUserID, row.PriceCents, row.Quantity, row.ExecutedAt)
		if err != nil {
			return nil, "", err
		}
		out = append(out, t)
	}

	nextCursor := ""
	if hasMore && len(out) > 0 {
		last := out[len(out)-1]
		nextCursor = encodeTradeCursor(last.ExecutedAt, last.ID)
	}
	return out, nextCursor, nil
}

func encodeTradeCursor(executedAt time.Time, id uuid.UUID) string {
	raw := fmt.Sprintf("%s|%s", executedAt.Format(time.RFC3339Nano), id.String())
	return base64.URLEncoding.EncodeToString([]byte(raw))
}

func decodeTradeCursor(cursor string) (time.Time, uuid.UUID, error) {
	raw, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.UUID{}, fmt.Errorf("invalid cursor: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.UUID{}, fmt.Errorf("invalid cursor format")
	}
	executedAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, uuid.UUID{}, fmt.Errorf("invalid cursor timestamp: %w", err)
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return time.Time{}, uuid.UUID{}, fmt.Errorf("invalid cursor id: %w", err)
	}
	return executedAt, id, nil
}
