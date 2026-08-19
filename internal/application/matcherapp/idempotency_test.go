// internal/application/matcherapp/idempotency_test.go
//go:build integration

package matcherapp

import (
	"context"
	"encoding/json"
	"testing"

	"trade-market/internal/application/orders"
	"trade-market/internal/domain/matching"
	"trade-market/internal/domain/order"
	"trade-market/internal/infra/postgres"
	"trade-market/test/integration/testenv"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func TestApplyBatch_ReprocessingSameEntryID_HasSingleEffect(t *testing.T) {
	ctx := context.Background()
	env := testenv.Setup(t, ctx) // migrations include seeded wallets 000..001 and 000..002

	alice := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	bob := uuid.MustParse("00000000-0000-0000-0000-000000000002")

	walletRepo := postgres.NewWalletRepository(env.DB)
	orderRepo := postgres.NewOrderRepository(env.DB)
	tradeRepo := postgres.NewTradeRepository(env.DB)
	processedRepo := postgres.NewProcessedEventsRepository(env.DB)
	logger, _ := zap.NewDevelopment()

	// Pre-rest a sell order directly (bypassing the API) so the matcher has
	// something to match against.
	price := int64(1000)
	sellOrder := &order.Order{ID: uuid.New(), UserID: alice, Side: order.SideSell, Type: order.TypeLimit, PriceCents: &price, Quantity: 5, Status: order.StatusOpen}
	book := matching.NewBook()
	book.AddResting(sellOrder)

	// The buy order's row and wallet reservation must both exist BEFORE the
	// matcher processes it — in production PlaceOrderService creates the
	// order row and reserves BRL synchronously, atomically, before the
	// event is ever queued (BUY LIMIT reserves price*quantity upfront).
	// This white-box test injects the matcher's event directly, bypassing
	// PlaceOrderService, so it must replicate both effects manually: the
	// trades table's FK on buy_order_id requires the order row to exist,
	// and SettleBuyLimitFill requires an actual reservation to release.
	buyOrderID := uuid.New()
	buyOrder := &order.Order{ID: buyOrderID, UserID: bob, Side: order.SideBuy, Type: order.TypeLimit, PriceCents: &price, Quantity: 5, Status: order.StatusOpen}

	tx, err := env.DB.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin tx: %v", err)
	}
	if err := orderRepo.Insert(ctx, tx, sellOrder); err != nil {
		t.Fatalf("failed to insert sell order: %v", err)
	}
	if err := orderRepo.Insert(ctx, tx, buyOrder); err != nil {
		t.Fatalf("failed to insert buy order: %v", err)
	}
	sellerWallet, err := walletRepo.GetForUpdate(ctx, tx, alice)
	if err != nil {
		t.Fatalf("failed to load seller wallet: %v", err)
	}
	if err := sellerWallet.ReserveVibranium(5); err != nil {
		t.Fatalf("failed to reserve seller vibranium: %v", err)
	}
	if err := walletRepo.Update(ctx, tx, sellerWallet); err != nil {
		t.Fatalf("failed to update seller wallet: %v", err)
	}
	buyerWallet, err := walletRepo.GetForUpdate(ctx, tx, bob)
	if err != nil {
		t.Fatalf("failed to load buyer wallet: %v", err)
	}
	if err := buyerWallet.ReserveBRL(price * 5); err != nil {
		t.Fatalf("failed to reserve buyer BRL: %v", err)
	}
	if err := walletRepo.Update(ctx, tx, buyerWallet); err != nil {
		t.Fatalf("failed to update buyer wallet: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("failed to commit setup tx: %v", err)
	}

	loop := NewLoop(env.DB, nil /* consumer unused: we call applyBatch directly */, book, walletRepo, orderRepo, tradeRepo, processedRepo, "orders:incoming", 200, 0, logger)

	event := orders.StreamEvent{Type: orders.EventTypeNewOrder, OrderID: buyOrderID, UserID: bob, Side: order.SideBuy, OrderType: order.TypeLimit, PriceCents: &price, Quantity: 5}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal event: %v", err)
	}
	msg := redis.XMessage{ID: "1700000000000-0", Values: map[string]any{"payload": string(payload)}}

	// First application: should match and settle.
	if err := loop.applyBatch(ctx, []redis.XMessage{msg}); err != nil {
		t.Fatalf("first applyBatch failed: %v", err)
	}
	// Second application with the SAME entry ID: simulates redelivery after
	// a crash between commit and XACK. Must be a complete no-op.
	if err := loop.applyBatch(ctx, []redis.XMessage{msg}); err != nil {
		t.Fatalf("second (duplicate) applyBatch failed: %v", err)
	}

	trades, _, err := tradeRepo.ListPaginated(ctx, postgres.TradeFilter{Limit: 10})
	if err != nil {
		t.Fatalf("failed to list trades: %v", err)
	}
	if len(trades) != 1 {
		t.Fatalf("expected exactly 1 trade despite double processing, got %d: %+v", len(trades), trades)
	}

	bobWallet, err := walletRepo.Get(ctx, bob)
	if err != nil {
		t.Fatalf("failed to get bob wallet: %v", err)
	}
	if bobWallet.BalanceVibranium != 10000005 { // seeded 10000000 (migration 000002_seed_wallets.up.sql) + 5, exactly once
		t.Fatalf("expected bob vibranium credited exactly once (10000005), got %d", bobWallet.BalanceVibranium)
	}

	// Without the idempotency guard, the second applyBatch call would
	// re-run Match() for the same buy order against an already-empty book
	// (the sell order was fully consumed by the first call) — finding
	// nothing left to match, it would rest the buy order again and
	// overwrite its Postgres row back to Status=OPEN/FilledQuantity=0,
	// even though bobWallet's balance and the trade count alone would
	// still look correct (the corruption is only visible on the order
	// row itself). Assert the buy order explicitly to catch that failure
	// mode, not just the wallet/trade-count side effects.
	finalBuyOrder, err := orderRepo.Get(ctx, buyOrderID)
	if err != nil {
		t.Fatalf("failed to get buy order: %v", err)
	}
	if finalBuyOrder.Status != order.StatusFilled || finalBuyOrder.FilledQuantity != 5 {
		t.Fatalf("expected buy order FILLED with filled_quantity=5 (proving the second applyBatch call did not re-process it), got status=%s filled_quantity=%d", finalBuyOrder.Status, finalBuyOrder.FilledQuantity)
	}
}
