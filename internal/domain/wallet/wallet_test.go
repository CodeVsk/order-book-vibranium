package wallet

import (
	"testing"

	"github.com/google/uuid"
)

func newTestWallet() *Wallet {
	return &Wallet{UserID: uuid.New(), BalanceBRLCents: 10000, BalanceVibranium: 50}
}

func TestWallet_ReserveBRL_InsufficientBalance(t *testing.T) {
	w := newTestWallet()
	err := w.ReserveBRL(10001)
	if err != ErrInsufficientBRL {
		t.Fatalf("expected ErrInsufficientBRL, got %v", err)
	}
	if w.BalanceBRLCents != 10000 || w.ReservedBRLCents != 0 {
		t.Fatalf("wallet must be unchanged on failed reservation, got %+v", w)
	}
}

func TestWallet_ReserveBRL_Success(t *testing.T) {
	w := newTestWallet()
	if err := w.ReserveBRL(4000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.BalanceBRLCents != 6000 || w.ReservedBRLCents != 4000 {
		t.Fatalf("expected balance=6000 reserved=4000, got %+v", w)
	}
	if w.AvailableBRLCents() != 2000 {
		t.Fatalf("available should be 2000, got %d", w.AvailableBRLCents())
	}
}

func TestWallet_SettleBuyMarketFill_DirectDebitNoReservation(t *testing.T) {
	w := newTestWallet()
	if err := w.SettleBuyMarketFill(1000, 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.BalanceBRLCents != 7000 || w.BalanceVibranium != 53 {
		t.Fatalf("expected balance=7000 vibranium=53, got %+v", w)
	}
}

func TestWallet_SettleSellFill_ReleasesReservationAndCreditsBRL(t *testing.T) {
	w := newTestWallet()
	if err := w.ReserveVibranium(20); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := w.SettleSellFill(1000, 20); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.ReservedVibranium != 0 || w.BalanceBRLCents != 30000 || w.BalanceVibranium != 30 {
		t.Fatalf("unexpected final state: %+v", w)
	}
}

func TestWallet_ReleaseBRLReservation_OnCancel(t *testing.T) {
	w := newTestWallet()
	_ = w.ReserveBRL(4000)
	if err := w.ReleaseBRLReservation(4000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.BalanceBRLCents != 10000 || w.ReservedBRLCents != 0 {
		t.Fatalf("expected full release back to balance, got %+v", w)
	}
}

func TestWallet_NeverGoesNegative_TableDriven(t *testing.T) {
	tests := []struct {
		name string
		run  func(w *Wallet) error
	}{
		{"reserve more BRL than available", func(w *Wallet) error { return w.ReserveBRL(999999) }},
		{"reserve more vibranium than available", func(w *Wallet) error { return w.ReserveVibranium(999999) }},
		{"release more BRL than reserved", func(w *Wallet) error { return w.ReleaseBRLReservation(1) }},
		{"settle sell fill without reservation", func(w *Wallet) error { return w.SettleSellFill(1000, 1) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newTestWallet()
			before := *w
			err := tt.run(w)
			if err == nil {
				t.Fatalf("expected an error for %s", tt.name)
			}
			if *w != before {
				t.Fatalf("wallet must be unchanged after rejected operation: before=%+v after=%+v", before, *w)
			}
			if w.BalanceBRLCents < 0 || w.ReservedBRLCents < 0 || w.BalanceVibranium < 0 || w.ReservedVibranium < 0 {
				t.Fatalf("wallet went negative: %+v", w)
			}
		})
	}
}
