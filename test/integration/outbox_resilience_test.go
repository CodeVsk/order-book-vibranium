// test/integration/outbox_resilience_test.go
//go:build integration

package integration

import (
	"context"
	"testing"

	"trade-market/internal/application/orders"
	"trade-market/internal/application/outboxapp"
	"trade-market/internal/domain/order"
	"trade-market/internal/infra/postgres"
	"trade-market/internal/infra/redisstream"
	"trade-market/test/integration/testenv"

	"github.com/google/uuid"
)

func TestOutboxResilience_SurvivesRedisUnavailability(t *testing.T) {
	ctx := context.Background()
	env := testenv.Setup(t, ctx)

	user := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	// See roundtrip_test.go's seedWallets for the ON CONFLICT rationale.
	_, err := env.DB.Exec(`INSERT INTO wallets (user_id, balance_brl_cents, balance_vibranium) VALUES ($1, 1000000, 1000)
		ON CONFLICT (user_id) DO UPDATE SET
			balance_brl_cents = EXCLUDED.balance_brl_cents,
			reserved_brl_cents = 0,
			balance_vibranium = EXCLUDED.balance_vibranium,
			reserved_vibranium = 0`, user)
	if err != nil {
		t.Fatalf("failed to seed wallet: %v", err)
	}

	walletRepo := postgres.NewWalletRepository(env.DB)
	orderRepo := postgres.NewOrderRepository(env.DB)
	outboxRepo := postgres.NewOutboxRepository(env.DB)
	userRepo := postgres.NewUserRepository(env.DB)
	const streamName = "orders:incoming"
	placeOrderSvc := orders.NewPlaceOrderService(env.DB, walletRepo, userRepo, orderRepo, outboxRepo, streamName, testLogger(t))

	priceCents := int64(1000)
	_, err = placeOrderSvc.Place(ctx, orders.PlaceOrderInput{UserID: user, Side: order.SideSell, Type: order.TypeLimit, PriceCents: &priceCents, Quantity: 3})
	if err != nil {
		t.Fatalf("failed to place order: %v", err)
	}

	// Simulate Redis being unavailable: close the client the publisher
	// depends on before its first publish attempt.
	if err := env.Redis.Close(); err != nil {
		t.Fatalf("failed to close redis client: %v", err)
	}
	deadProducer := redisstream.NewProducer(env.Redis)
	deadPublisher := outboxapp.NewPublisher(env.DB, outboxRepo, deadProducer, 100, testLogger(t))

	if _, err := deadPublisher.PublishOnce(ctx); err == nil {
		t.Fatalf("expected PublishOnce to fail while redis is unavailable")
	}

	var unpublishedCount int
	if err := env.DB.Get(&unpublishedCount, `SELECT count(*) FROM outbox_events WHERE published = false`); err != nil {
		t.Fatalf("failed to count unpublished events: %v", err)
	}
	if unpublishedCount != 1 {
		t.Fatalf("expected the event to remain durably unpublished, got count=%d", unpublishedCount)
	}

	// Reconnect and retry: the event must now publish successfully with no
	// data loss and no duplication risk (still published=false until this
	// retry succeeds).
	freshEnv := testenv.Setup(t, ctx) // a second, independent redis for the "recovered" leg
	recoveredProducer := redisstream.NewProducer(freshEnv.Redis)
	recoveredPublisher := outboxapp.NewPublisher(env.DB, outboxRepo, recoveredProducer, 100, testLogger(t))

	n, err := recoveredPublisher.PublishOnce(ctx)
	if err != nil {
		t.Fatalf("expected publish to succeed once redis is back, got err: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 event published, got %d", n)
	}

	if err := env.DB.Get(&unpublishedCount, `SELECT count(*) FROM outbox_events WHERE published = false`); err != nil {
		t.Fatalf("failed to count unpublished events: %v", err)
	}
	if unpublishedCount != 0 {
		t.Fatalf("expected zero unpublished events after recovery, got %d", unpublishedCount)
	}

	// Confirm "no duplication risk" isn't just inferred from the failed
	// attempt never reaching the network — actually check the stream itself
	// has exactly one entry, not zero (lost) or more than one (duplicated).
	streamLen, err := freshEnv.Redis.XLen(ctx, streamName).Result()
	if err != nil {
		t.Fatalf("failed to get stream length: %v", err)
	}
	if streamLen != 1 {
		t.Fatalf("expected exactly 1 entry in the stream, got %d", streamLen)
	}
}
