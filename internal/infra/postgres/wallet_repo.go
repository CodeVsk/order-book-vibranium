// internal/infra/postgres/wallet_repo.go
package postgres

import (
	"context"
	"database/sql"
	"errors"

	"trade-market/internal/domain/wallet"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

var ErrWalletNotFound = errors.New("postgres: wallet not found")

type WalletRepository struct {
	db *sqlx.DB
}

func NewWalletRepository(db *sqlx.DB) *WalletRepository {
	return &WalletRepository{db: db}
}

type walletRow struct {
	UserID            uuid.UUID `db:"user_id"`
	BalanceBRLCents   int64     `db:"balance_brl_cents"`
	ReservedBRLCents  int64     `db:"reserved_brl_cents"`
	BalanceVibranium  int64     `db:"balance_vibranium"`
	ReservedVibranium int64     `db:"reserved_vibranium"`
}

func toWallet(r walletRow) *wallet.Wallet {
	return &wallet.Wallet{
		UserID:            r.UserID,
		BalanceBRLCents:   r.BalanceBRLCents,
		ReservedBRLCents:  r.ReservedBRLCents,
		BalanceVibranium:  r.BalanceVibranium,
		ReservedVibranium: r.ReservedVibranium,
	}
}

// GetForUpdate locks the wallet row for the duration of tx. Must be called
// inside an open transaction. This is THE concurrency primitive that makes
// concurrent reservations against the same user safe (see spec's Critical
// Flows note on SELECT FOR UPDATE).
func (r *WalletRepository) GetForUpdate(ctx context.Context, tx *sqlx.Tx, userID uuid.UUID) (*wallet.Wallet, error) {
	var row walletRow
	const q = `SELECT user_id, balance_brl_cents, reserved_brl_cents, balance_vibranium, reserved_vibranium
	           FROM wallets WHERE user_id = $1 FOR UPDATE`
	if err := tx.GetContext(ctx, &row, q, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrWalletNotFound
		}
		return nil, err
	}
	return toWallet(row), nil
}

// Get performs a plain (non-locking) read, used by GET /wallets/{user_id}.
func (r *WalletRepository) Get(ctx context.Context, userID uuid.UUID) (*wallet.Wallet, error) {
	var row walletRow
	const q = `SELECT user_id, balance_brl_cents, reserved_brl_cents, balance_vibranium, reserved_vibranium
	           FROM wallets WHERE user_id = $1`
	if err := r.db.GetContext(ctx, &row, q, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrWalletNotFound
		}
		return nil, err
	}
	return toWallet(row), nil
}

// Update persists the full balance/reserved state of w inside tx.
func (r *WalletRepository) Update(ctx context.Context, tx *sqlx.Tx, w *wallet.Wallet) error {
	const q = `UPDATE wallets
	           SET balance_brl_cents = $1, reserved_brl_cents = $2,
	               balance_vibranium = $3, reserved_vibranium = $4, updated_at = now()
	           WHERE user_id = $5`
	_, err := tx.ExecContext(ctx, q, w.BalanceBRLCents, w.ReservedBRLCents, w.BalanceVibranium, w.ReservedVibranium, w.UserID)
	return err
}
