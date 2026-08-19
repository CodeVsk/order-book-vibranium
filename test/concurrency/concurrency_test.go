// test/concurrency/concurrency_test.go
//go:build integration

package concurrency

import (
	"context"
	"math/rand"
	"sync"
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
	"go.uber.org/zap"
)

var seededUsers = []uuid.UUID{
	uuid.MustParse("00000000-0000-0000-0000-000000000001"),
	uuid.MustParse("00000000-0000-0000-0000-000000000002"),
	uuid.MustParse("00000000-0000-0000-0000-000000000003"),
	uuid.MustParse("00000000-0000-0000-0000-000000000004"),
	uuid.MustParse("00000000-0000-0000-0000-000000000005"),
}

type walletTotals struct {
	brl       int64
	vibranium int64
}

func sumWalletTotals(t *testing.T, walletRepo *postgres.WalletRepository, ctx context.Context) walletTotals {
	t.Helper()
	var totals walletTotals
	for _, u := range seededUsers {
		w, err := walletRepo.Get(ctx, u)
		if err != nil {
			t.Fatalf("failed to read wallet %s: %v", u, err)
		}
		if w.BalanceBRLCents < 0 || w.ReservedBRLCents < 0 || w.BalanceVibranium < 0 || w.ReservedVibranium < 0 {
			t.Fatalf("wallet %s went negative: %+v", u, w)
		}
		totals.brl += w.BalanceBRLCents + w.ReservedBRLCents
		totals.vibranium += w.BalanceVibranium + w.ReservedVibranium
	}
	return totals
}

func TestConcurrentOrderPlacement_NoNegativeBalances_AndConservation(t *testing.T) {
	ctx := context.Background()
	env := testenv.Setup(t, ctx) // migrations include the 5 seeded wallets already

	logger, _ := zap.NewDevelopment()
	walletRepo := postgres.NewWalletRepository(env.DB)
	orderRepo := postgres.NewOrderRepository(env.DB)
	tradeRepo := postgres.NewTradeRepository(env.DB)
	outboxRepo := postgres.NewOutboxRepository(env.DB)
	processedRepo := postgres.NewProcessedEventsRepository(env.DB)
	userRepo := postgres.NewUserRepository(env.DB)
	const streamName = "orders:incoming"

	placeOrderSvc := orders.NewPlaceOrderService(env.DB, walletRepo, userRepo, orderRepo, outboxRepo, streamName, logger)

	before := sumWalletTotals(t, walletRepo, ctx)

	const numGoroutines = 200
	var wg sync.WaitGroup
	errs := make(chan error, numGoroutines)
	rng := rand.New(rand.NewSource(42))
	var rngMu sync.Mutex

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			localRand := rand.New(rand.NewSource(seed))

			rngMu.Lock()
			user := seededUsers[rng.Intn(len(seededUsers))]
			rngMu.Unlock()

			side := order.SideBuy
			if localRand.Intn(2) == 0 {
				side = order.SideSell
			}
			price := int64(900 + localRand.Intn(200)) // 900..1099 cents
			qty := int64(1 + localRand.Intn(10))      // 1..10 units

			_, err := placeOrderSvc.Place(ctx, orders.PlaceOrderInput{
				UserID: user, Side: side, Type: order.TypeLimit, PriceCents: &price, Quantity: qty,
			})
			if err != nil && err != orders.ErrInsufficientBalance {
				errs <- err
			}
		}(int64(i))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("unexpected error during concurrent placement: %v", err)
	}

	// Drain: publish + match everything that got queued.
	producer := redisstream.NewProducer(env.Redis)
	publisher := outboxapp.NewPublisher(env.DB, outboxRepo, producer, 500, logger)
	consumer, err := redisstream.NewConsumer(ctx, env.Redis, streamName, "matcher-group", "matcher-1")
	if err != nil {
		t.Fatalf("failed to create consumer: %v", err)
	}
	book := matching.NewBook()
	loop := matcherapp.NewLoop(env.DB, consumer, book, walletRepo, orderRepo, tradeRepo, processedRepo, streamName, 500, 100*time.Millisecond, logger)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		published, err := publisher.PublishOnce(ctx)
		if err != nil {
			t.Fatalf("publish cycle failed: %v", err)
		}
		processed, err := loop.ProcessOnce(ctx)
		if err != nil {
			t.Fatalf("process cycle failed: %v", err)
		}
		if published == 0 && processed == 0 {
			break
		}
	}

	after := sumWalletTotals(t, walletRepo, ctx) // also asserts no negative balances

	if before.brl != after.brl {
		t.Fatalf("BRL conservation violated: before=%d after=%d (diff=%d)", before.brl, after.brl, after.brl-before.brl)
	}
	if before.vibranium != after.vibranium {
		t.Fatalf("Vibranium conservation violated: before=%d after=%d (diff=%d)", before.vibranium, after.vibranium, after.vibranium-before.vibranium)
	}
}
