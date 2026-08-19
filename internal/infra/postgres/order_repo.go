// internal/infra/postgres/order_repo.go
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"trade-market/internal/domain/order"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

var ErrOrderNotFound = errors.New("postgres: order not found")

type OrderRepository struct {
	db *sqlx.DB
}

func NewOrderRepository(db *sqlx.DB) *OrderRepository {
	return &OrderRepository{db: db}
}

type orderRow struct {
	ID             uuid.UUID `db:"id"`
	UserID         uuid.UUID `db:"user_id"`
	Side           string    `db:"side"`
	Type           string    `db:"type"`
	PriceCents     *int64    `db:"price_cents"`
	Quantity       int64     `db:"quantity"`
	FilledQuantity int64     `db:"filled_quantity"`
	Status         string    `db:"status"`
	CreatedAt      time.Time `db:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"`
}

func toOrder(r orderRow) *order.Order {
	return &order.Order{
		ID:             r.ID,
		UserID:         r.UserID,
		Side:           order.Side(r.Side),
		Type:           order.Type(r.Type),
		PriceCents:     r.PriceCents,
		Quantity:       r.Quantity,
		FilledQuantity: r.FilledQuantity,
		Status:         order.Status(r.Status),
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
}

// Insert persists a brand-new order inside tx (called by PlaceOrderService,
// same transaction as the wallet reservation and the outbox event).
func (r *OrderRepository) Insert(ctx context.Context, tx *sqlx.Tx, o *order.Order) error {
	const q = `INSERT INTO orders (id, user_id, side, type, price_cents, quantity, filled_quantity, status, created_at, updated_at)
	           VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	_, err := tx.ExecContext(ctx, q, o.ID, o.UserID, o.Side, o.Type, o.PriceCents, o.Quantity, o.FilledQuantity, o.Status, o.CreatedAt, o.UpdatedAt)
	return err
}

// Get performs a plain read, used by GET /orders/{id} and by
// CancelOrderService's ownership check.
func (r *OrderRepository) Get(ctx context.Context, id uuid.UUID) (*order.Order, error) {
	var row orderRow
	const q = `SELECT id, user_id, side, type, price_cents, quantity, filled_quantity, status, created_at, updated_at
	           FROM orders WHERE id = $1`
	if err := r.db.GetContext(ctx, &row, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrderNotFound
		}
		return nil, err
	}
	return toOrder(row), nil
}

// GetTx is the transactional counterpart of Get, used inside
// CancelOrderService's transaction to read-then-decide atomically. Locks
// the row for the duration of tx (SELECT ... FOR UPDATE) so two concurrent
// cancel requests for the same order can't both pass the terminal-state
// check and both enqueue a CANCEL_ORDER event.
func (r *OrderRepository) GetTx(ctx context.Context, tx *sqlx.Tx, id uuid.UUID) (*order.Order, error) {
	var row orderRow
	const q = `SELECT id, user_id, side, type, price_cents, quantity, filled_quantity, status, created_at, updated_at
	           FROM orders WHERE id = $1 FOR UPDATE`
	if err := tx.GetContext(ctx, &row, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrderNotFound
		}
		return nil, err
	}
	return toOrder(row), nil
}

// Update persists filled_quantity/status changes made by the matching
// engine (or a cancellation) inside tx.
func (r *OrderRepository) Update(ctx context.Context, tx *sqlx.Tx, o *order.Order) error {
	const q = `UPDATE orders SET filled_quantity = $1, status = $2, updated_at = now() WHERE id = $3`
	_, err := tx.ExecContext(ctx, q, o.FilledQuantity, o.Status, o.ID)
	return err
}

// ListOpenForRecovery returns every order still eligible to rest on or
// match against the book (OPEN or PARTIALLY_FILLED), oldest first — used by
// the matcher on boot to rebuild the in-memory Book after a crash/restart.
func (r *OrderRepository) ListOpenForRecovery(ctx context.Context) ([]*order.Order, error) {
	var rows []orderRow
	const q = `SELECT id, user_id, side, type, price_cents, quantity, filled_quantity, status, created_at, updated_at
	           FROM orders WHERE status IN ('OPEN', 'PARTIALLY_FILLED') ORDER BY created_at ASC`
	if err := r.db.SelectContext(ctx, &rows, q); err != nil {
		return nil, err
	}
	out := make([]*order.Order, 0, len(rows))
	for _, row := range rows {
		out = append(out, toOrder(row))
	}
	return out, nil
}
