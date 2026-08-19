package wallet

import (
	"errors"

	"github.com/google/uuid"
)

var (
	ErrInsufficientBRL       = errors.New("wallet: insufficient available BRL balance")
	ErrInsufficientVibranium = errors.New("wallet: insufficient available Vibranium balance")
	ErrInvalidAmount         = errors.New("wallet: amount/quantity must be positive")
	ErrReleaseExceedsReserve = errors.New("wallet: release amount exceeds current reservation")
)

// Wallet is the pure domain representation of one user's balances. All
// money is integer BRL cents; Vibranium is integer units. Persistence
// (infra/postgres) maps this 1:1 to the wallets table.
type Wallet struct {
	UserID            uuid.UUID
	BalanceBRLCents   int64
	ReservedBRLCents  int64
	BalanceVibranium  int64
	ReservedVibranium int64
}

func (w *Wallet) AvailableBRLCents() int64  { return w.BalanceBRLCents - w.ReservedBRLCents }
func (w *Wallet) AvailableVibranium() int64 { return w.BalanceVibranium - w.ReservedVibranium }

// ReserveBRL is called when placing a BUY LIMIT order: moves amountCents
// from available balance into the reserved bucket.
func (w *Wallet) ReserveBRL(amountCents int64) error {
	if amountCents <= 0 {
		return ErrInvalidAmount
	}
	if w.AvailableBRLCents() < amountCents {
		return ErrInsufficientBRL
	}
	w.BalanceBRLCents -= amountCents
	w.ReservedBRLCents += amountCents
	return nil
}

// ReserveVibranium is called when placing a SELL LIMIT or SELL MARKET order.
func (w *Wallet) ReserveVibranium(qty int64) error {
	if qty <= 0 {
		return ErrInvalidAmount
	}
	if w.AvailableVibranium() < qty {
		return ErrInsufficientVibranium
	}
	w.BalanceVibranium -= qty
	w.ReservedVibranium += qty
	return nil
}

// ReleaseBRLReservation returns a previously reserved amount to the
// available balance (order cancelled, or unfilled remainder cancelled).
func (w *Wallet) ReleaseBRLReservation(amountCents int64) error {
	if amountCents <= 0 {
		return ErrInvalidAmount
	}
	if amountCents > w.ReservedBRLCents {
		return ErrReleaseExceedsReserve
	}
	w.ReservedBRLCents -= amountCents
	w.BalanceBRLCents += amountCents
	return nil
}

// ReleaseVibraniumReservation is the SELL-side counterpart of
// ReleaseBRLReservation.
func (w *Wallet) ReleaseVibraniumReservation(qty int64) error {
	if qty <= 0 {
		return ErrInvalidAmount
	}
	if qty > w.ReservedVibranium {
		return ErrReleaseExceedsReserve
	}
	w.ReservedVibranium -= qty
	w.BalanceVibranium += qty
	return nil
}

// SettleBuyLimitFill applies a trade execution to a buyer that had funds
// reserved up front (BUY LIMIT): the reservation is consumed and Vibranium
// is credited.
func (w *Wallet) SettleBuyLimitFill(priceCents, qty int64) error {
	if priceCents <= 0 || qty <= 0 {
		return ErrInvalidAmount
	}
	cost := priceCents * qty
	if cost > w.ReservedBRLCents {
		return ErrReleaseExceedsReserve
	}
	w.ReservedBRLCents -= cost
	w.BalanceVibranium += qty
	return nil
}

// SettleBuyMarketFill applies a trade execution to a buyer that had NO
// reservation up front (BUY MARKET never reserves — see design decision #1
// at the top of this plan). BRL is debited directly from available
// balance, which the matching engine already guaranteed was sufficient
// before this trade was produced.
func (w *Wallet) SettleBuyMarketFill(priceCents, qty int64) error {
	if priceCents <= 0 || qty <= 0 {
		return ErrInvalidAmount
	}
	cost := priceCents * qty
	if cost > w.BalanceBRLCents {
		return ErrInsufficientBRL
	}
	w.BalanceBRLCents -= cost
	w.BalanceVibranium += qty
	return nil
}

// SettleSellFill applies a trade execution to a seller (LIMIT or MARKET —
// both reserve Vibranium up front, so the settlement path is identical).
func (w *Wallet) SettleSellFill(priceCents, qty int64) error {
	if priceCents <= 0 || qty <= 0 {
		return ErrInvalidAmount
	}
	if qty > w.ReservedVibranium {
		return ErrReleaseExceedsReserve
	}
	w.ReservedVibranium -= qty
	w.BalanceBRLCents += priceCents * qty
	return nil
}
