// test/integration/roundtrip_test.go
//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"trade-market/internal/application/matcherapp"
	"trade-market/internal/application/orders"
	"trade-market/internal/application/outboxapp"
	"trade-market/internal/domain/matching"
	"trade-market/internal/domain/order"
	"trade-market/internal/infra/postgres"
	"trade-market/internal/infra/redisstream"
	"trade-market/test/integration/testenv"

	"github.com/google/uuid"
)

var (
	alice = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	bob   = uuid.MustParse("00000000-0000-0000-0000-000000000002")
)

func seedWallets(t *testing.T, env *testenv.Env) {
	t.Helper()
	// ON CONFLICT DO UPDATE: migration 000002_seed_wallets.up.sql (which
	// testenv.Setup already applied) seeds these exact user_ids
	// (00000000-...-0001 through -0005) with much larger balances
	// (10000000000 BRL cents / 10000000 Vibranium) for manual/k6 load
	// testing. This test wants alice/bob to start from a known, smaller,
	// easy-to-verify-by-hand balance instead — a plain INSERT would
	// violate the primary key since the rows already exist. Resetting via
	// ON CONFLICT DO UPDATE (rather than picking different, unseeded UUIDs)
	// keeps this test's alice/bob consistent with the same well-known
	// UUIDs used throughout the rest of the codebase's manual smoke tests.
	_, err := env.DB.Exec(`INSERT INTO wallets (user_id, balance_brl_cents, balance_vibranium) VALUES
		($1, 1000000, 1000), ($2, 1000000, 1000)
		ON CONFLICT (user_id) DO UPDATE SET
			balance_brl_cents = EXCLUDED.balance_brl_cents,
			reserved_brl_cents = 0,
			balance_vibranium = EXCLUDED.balance_vibranium,
			reserved_vibranium = 0`, alice, bob)
	if err != nil {
		t.Fatalf("failed to seed wallets: %v", err)
	}
}

func TestRoundTrip_PlaceOrder_ThroughMatch_ToWalletSettlement(t *testing.T) {
	ctx := context.Background()
	env := testenv.Setup(t, ctx)
	seedWallets(t, env)

	walletRepo := postgres.NewWalletRepository(env.DB)
	orderRepo := postgres.NewOrderRepository(env.DB)
	tradeRepo := postgres.NewTradeRepository(env.DB)
	outboxRepo := postgres.NewOutboxRepository(env.DB)
	processedRepo := postgres.NewProcessedEventsRepository(env.DB)
	userRepo := postgres.NewUserRepository(env.DB)

	const streamName = "orders:incoming"
	placeOrderSvc := orders.NewPlaceOrderService(env.DB, walletRepo, userRepo, orderRepo, outboxRepo, streamName, testLogger(t))

	producer := redisstream.NewProducer(env.Redis)
	publisher := outboxapp.NewPublisher(env.DB, outboxRepo, producer, 100, testLogger(t))

	consumer, err := redisstream.NewConsumer(ctx, env.Redis, streamName, "matcher-group", "matcher-1")
	if err != nil {
		t.Fatalf("failed to create consumer: %v", err)
	}
	book := matching.NewBook()
	loop := matcherapp.NewLoop(env.DB, consumer, book, walletRepo, orderRepo, tradeRepo, processedRepo, streamName, 200, 50*time.Millisecond, testLogger(t))

	// Alice rests a SELL LIMIT at 1000 cents for 5 units.
	priceCents := int64(1000)
	_, err = placeOrderSvc.Place(ctx, orders.PlaceOrderInput{UserID: alice, Side: order.SideSell, Type: order.TypeLimit, PriceCents: &priceCents, Quantity: 5})
	if err != nil {
		t.Fatalf("failed to place sell order: %v", err)
	}
	if n, err := publisher.PublishOnce(ctx); err != nil || n != 1 {
		t.Fatalf("expected to publish 1 event, got n=%d err=%v", n, err)
	}
	if n, err := loop.ProcessOnce(ctx); err != nil || n != 1 {
		t.Fatalf("expected to process 1 event, got n=%d err=%v", n, err)
	}

	// Bob matches it with a BUY LIMIT at the same price.
	_, err = placeOrderSvc.Place(ctx, orders.PlaceOrderInput{UserID: bob, Side: order.SideBuy, Type: order.TypeLimit, PriceCents: &priceCents, Quantity: 5})
	if err != nil {
		t.Fatalf("failed to place buy order: %v", err)
	}
	if n, err := publisher.PublishOnce(ctx); err != nil || n != 1 {
		t.Fatalf("expected to publish 1 event, got n=%d err=%v", n, err)
	}
	if n, err := loop.ProcessOnce(ctx); err != nil || n != 1 {
		t.Fatalf("expected to process 1 event, got n=%d err=%v", n, err)
	}

	aliceWallet, err := walletRepo.Get(ctx, alice)
	if err != nil {
		t.Fatalf("failed to get alice wallet: %v", err)
	}
	bobWallet, err := walletRepo.Get(ctx, bob)
	if err != nil {
		t.Fatalf("failed to get bob wallet: %v", err)
	}

	if aliceWallet.BalanceVibranium != 995 || aliceWallet.ReservedVibranium != 0 {
		t.Fatalf("expected alice vibranium 995 reserved 0, got %+v", aliceWallet)
	}
	if aliceWallet.BalanceBRLCents != 1005000 {
		t.Fatalf("expected alice BRL 1005000 (1000000 + 5*1000), got %d", aliceWallet.BalanceBRLCents)
	}
	if bobWallet.BalanceVibranium != 1005 {
		t.Fatalf("expected bob vibranium 1005, got %d", bobWallet.BalanceVibranium)
	}
	if bobWallet.BalanceBRLCents != 995000 || bobWallet.ReservedBRLCents != 0 {
		t.Fatalf("expected bob BRL 995000 (1000000 - 5000) reserved 0, got %+v", bobWallet)
	}

	trades, _, err := tradeRepo.ListPaginated(ctx, postgres.TradeFilter{Limit: 10})
	if err != nil {
		t.Fatalf("failed to list trades: %v", err)
	}
	if len(trades) != 1 || trades[0].Quantity != 5 || trades[0].PriceCents != 1000 {
		t.Fatalf("expected exactly one trade qty=5 price=1000, got %+v", trades)
	}
}
