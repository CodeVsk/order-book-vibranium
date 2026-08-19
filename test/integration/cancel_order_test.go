// test/integration/cancel_order_test.go
//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
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

// cancelTestEnv bundles the same real Postgres+Redis-backed services
// roundtrip_test.go wires inline, plus both the cancel-side application
// service and the matcher Loop, so every scenario in this file can drive
// the full place -> cancel -> publish -> match pipeline without repeating
// the wiring in each test function.
type cancelTestEnv struct {
	env         *testenv.Env
	orderRepo   *postgres.OrderRepository
	walletRepo  *postgres.WalletRepository
	tradeRepo   *postgres.TradeRepository
	outboxRepo  *postgres.OutboxRepository
	placeOrder  *orders.PlaceOrderService
	cancelOrder *orders.CancelOrderService
	publisher   *outboxapp.Publisher
	loop        *matcherapp.Loop
}

const cancelTestStreamName = "orders:incoming"

func setupCancelTestEnv(t *testing.T, ctx context.Context) *cancelTestEnv {
	t.Helper()
	env := testenv.Setup(t, ctx)

	walletRepo := postgres.NewWalletRepository(env.DB)
	orderRepo := postgres.NewOrderRepository(env.DB)
	tradeRepo := postgres.NewTradeRepository(env.DB)
	outboxRepo := postgres.NewOutboxRepository(env.DB)
	processedRepo := postgres.NewProcessedEventsRepository(env.DB)
	userRepo := postgres.NewUserRepository(env.DB)

	placeOrderSvc := orders.NewPlaceOrderService(env.DB, walletRepo, userRepo, orderRepo, outboxRepo, cancelTestStreamName, testLogger(t))
	cancelOrderSvc := orders.NewCancelOrderService(env.DB, orderRepo, outboxRepo, cancelTestStreamName, testLogger(t))

	producer := redisstream.NewProducer(env.Redis)
	publisher := outboxapp.NewPublisher(env.DB, outboxRepo, producer, 100, testLogger(t))

	consumer, err := redisstream.NewConsumer(ctx, env.Redis, cancelTestStreamName, "matcher-group", "matcher-1")
	if err != nil {
		t.Fatalf("failed to create consumer: %v", err)
	}
	book := matching.NewBook()
	loop := matcherapp.NewLoop(env.DB, consumer, book, walletRepo, orderRepo, tradeRepo, processedRepo, cancelTestStreamName, 200, 50*time.Millisecond, testLogger(t))

	return &cancelTestEnv{
		env: env, orderRepo: orderRepo, walletRepo: walletRepo, tradeRepo: tradeRepo, outboxRepo: outboxRepo,
		placeOrder: placeOrderSvc, cancelOrder: cancelOrderSvc, publisher: publisher, loop: loop,
	}
}

// seedWallet resets (or creates) a single wallet to a known balance; see
// roundtrip_test.go's seedWallets for the ON CONFLICT rationale.
func seedWallet(t *testing.T, env *testenv.Env, userID uuid.UUID, brlCents, vibranium int64) {
	t.Helper()
	_, err := env.DB.Exec(`INSERT INTO wallets (user_id, balance_brl_cents, balance_vibranium) VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET
			balance_brl_cents = EXCLUDED.balance_brl_cents,
			reserved_brl_cents = 0,
			balance_vibranium = EXCLUDED.balance_vibranium,
			reserved_vibranium = 0`, userID, brlCents, vibranium)
	if err != nil {
		t.Fatalf("failed to seed wallet for %s: %v", userID, err)
	}
}

// seedUser inserts a row directly into users — without a matching wallet —
// so tests can exercise the "user_id exists but has no wallet provisioned"
// path distinctly from "user_id does not exist at all".
func seedUser(t *testing.T, env *testenv.Env, userID uuid.UUID, username string) {
	t.Helper()
	_, err := env.DB.Exec(`INSERT INTO users (id, username) VALUES ($1, $2)`, userID, username)
	if err != nil {
		t.Fatalf("failed to seed user %s: %v", userID, err)
	}
}

// placeAndProcess places an order and immediately drives its NEW_ORDER
// event through the outbox and matcher, so the caller gets back a fully
// settled/resting order without repeating the publish+process ceremony in
// every test.
func (c *cancelTestEnv) placeAndProcess(t *testing.T, ctx context.Context, in orders.PlaceOrderInput) *order.Order {
	t.Helper()
	o, err := c.placeOrder.Place(ctx, in)
	if err != nil {
		t.Fatalf("failed to place order: %v", err)
	}
	if n, err := c.publisher.PublishOnce(ctx); err != nil || n != 1 {
		t.Fatalf("expected to publish 1 event, got n=%d err=%v", n, err)
	}
	if n, err := c.loop.ProcessOnce(ctx); err != nil || n != 1 {
		t.Fatalf("expected to process 1 event, got n=%d err=%v", n, err)
	}
	return o
}

// cancelAndProcess drives the CANCEL_ORDER path end to end: the
// application service call (ownership/terminal check + outbox write) then,
// only if an event was actually queued, publish + matcher apply.
func (c *cancelTestEnv) cancelAndProcess(t *testing.T, ctx context.Context, orderID, userID uuid.UUID) orders.CancelResult {
	t.Helper()
	res, err := c.cancelOrder.Cancel(ctx, orderID, userID)
	if err != nil {
		t.Fatalf("failed to cancel order: %v", err)
	}
	if !res.AlreadyTerminal {
		if n, err := c.publisher.PublishOnce(ctx); err != nil || n != 1 {
			t.Fatalf("expected to publish 1 cancel event, got n=%d err=%v", n, err)
		}
		if n, err := c.loop.ProcessOnce(ctx); err != nil || n != 1 {
			t.Fatalf("expected to process 1 cancel event, got n=%d err=%v", n, err)
		}
	}
	return res
}

// --- CancelOrderService.Cancel scenarios (spec §6's error-mapping table) ---

func TestCancelOrderService_ForbiddenWhenNotOwner(t *testing.T) {
	ctx := context.Background()
	c := setupCancelTestEnv(t, ctx)

	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	stranger := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	seedWallet(t, c.env, owner, 1000000, 1000)
	seedWallet(t, c.env, stranger, 1000000, 1000)

	price := int64(1000)
	o, err := c.placeOrder.Place(ctx, orders.PlaceOrderInput{UserID: owner, Side: order.SideBuy, Type: order.TypeLimit, PriceCents: &price, Quantity: 5})
	if err != nil {
		t.Fatalf("failed to place order: %v", err)
	}

	_, err = c.cancelOrder.Cancel(ctx, o.ID, stranger)
	if !errors.Is(err, orders.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestCancelOrderService_NotFound(t *testing.T) {
	ctx := context.Background()
	c := setupCancelTestEnv(t, ctx)

	requester := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	seedWallet(t, c.env, requester, 1000000, 1000)

	_, err := c.cancelOrder.Cancel(ctx, uuid.New(), requester)
	if !errors.Is(err, orders.ErrOrderNotFound) {
		t.Fatalf("expected ErrOrderNotFound, got %v", err)
	}
}

func TestCancelOrderService_AlreadyTerminal_Filled_NoOutboxEventWritten(t *testing.T) {
	ctx := context.Background()
	c := setupCancelTestEnv(t, ctx)

	seller := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	buyer := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	seedWallet(t, c.env, seller, 1000000, 1000)
	seedWallet(t, c.env, buyer, 1000000, 1000)

	price := int64(1000)
	sellOrder := c.placeAndProcess(t, ctx, orders.PlaceOrderInput{UserID: seller, Side: order.SideSell, Type: order.TypeLimit, PriceCents: &price, Quantity: 5})
	c.placeAndProcess(t, ctx, orders.PlaceOrderInput{UserID: buyer, Side: order.SideBuy, Type: order.TypeLimit, PriceCents: &price, Quantity: 5})

	fetched, err := c.orderRepo.Get(ctx, sellOrder.ID)
	if err != nil {
		t.Fatalf("failed to fetch sell order: %v", err)
	}
	if fetched.Status != order.StatusFilled {
		t.Fatalf("expected sell order to be FILLED before the cancel attempt, got %s", fetched.Status)
	}

	var outboxCountBefore int
	if err := c.env.DB.Get(&outboxCountBefore, `SELECT count(*) FROM outbox_events`); err != nil {
		t.Fatalf("failed to count outbox events: %v", err)
	}

	res, err := c.cancelOrder.Cancel(ctx, sellOrder.ID, seller)
	if err != nil {
		t.Fatalf("expected no error cancelling an already-FILLED order, got %v", err)
	}
	if !res.AlreadyTerminal {
		t.Fatalf("expected AlreadyTerminal=true for a FILLED order, got %+v", res)
	}
	if res.Status != order.StatusFilled {
		t.Fatalf("expected returned status FILLED, got %s", res.Status)
	}

	var outboxCountAfter int
	if err := c.env.DB.Get(&outboxCountAfter, `SELECT count(*) FROM outbox_events`); err != nil {
		t.Fatalf("failed to count outbox events: %v", err)
	}
	if outboxCountAfter != outboxCountBefore {
		t.Fatalf("expected no new outbox event for an already-terminal cancel, before=%d after=%d", outboxCountBefore, outboxCountAfter)
	}
}

func TestCancelOrderService_AlreadyTerminal_Cancelled_NoOutboxEventWritten(t *testing.T) {
	ctx := context.Background()
	c := setupCancelTestEnv(t, ctx)

	buyer := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	seedWallet(t, c.env, buyer, 1000000, 1000)

	price := int64(1000)
	o := c.placeAndProcess(t, ctx, orders.PlaceOrderInput{UserID: buyer, Side: order.SideBuy, Type: order.TypeLimit, PriceCents: &price, Quantity: 5})

	// First cancel: order is OPEN, so this actually queues+applies a
	// CANCEL_ORDER event, driving the order to CANCELLED.
	first := c.cancelAndProcess(t, ctx, o.ID, buyer)
	if first.AlreadyTerminal {
		t.Fatalf("expected the first cancel of an OPEN order to NOT be a no-op, got %+v", first)
	}
	fetched, err := c.orderRepo.Get(ctx, o.ID)
	if err != nil {
		t.Fatalf("failed to fetch order: %v", err)
	}
	if fetched.Status != order.StatusCancelled {
		t.Fatalf("expected order CANCELLED after first cancel round-trip, got %s", fetched.Status)
	}

	var outboxCountBefore int
	if err := c.env.DB.Get(&outboxCountBefore, `SELECT count(*) FROM outbox_events`); err != nil {
		t.Fatalf("failed to count outbox events: %v", err)
	}

	// Second cancel: order is already CANCELLED -> pure no-op idempotent read.
	res, err := c.cancelOrder.Cancel(ctx, o.ID, buyer)
	if err != nil {
		t.Fatalf("expected no error re-cancelling an already-CANCELLED order, got %v", err)
	}
	if !res.AlreadyTerminal {
		t.Fatalf("expected AlreadyTerminal=true for a CANCELLED order, got %+v", res)
	}
	if res.Status != order.StatusCancelled {
		t.Fatalf("expected returned status CANCELLED, got %s", res.Status)
	}

	var outboxCountAfter int
	if err := c.env.DB.Get(&outboxCountAfter, `SELECT count(*) FROM outbox_events`); err != nil {
		t.Fatalf("failed to count outbox events: %v", err)
	}
	if outboxCountAfter != outboxCountBefore {
		t.Fatalf("expected no new outbox event for a second, already-terminal cancel, before=%d after=%d", outboxCountBefore, outboxCountAfter)
	}
}

func TestCancelOrderService_Success_WritesCancelOrderOutboxEvent(t *testing.T) {
	ctx := context.Background()
	c := setupCancelTestEnv(t, ctx)

	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	seedWallet(t, c.env, owner, 1000000, 1000)

	price := int64(1000)
	o, err := c.placeOrder.Place(ctx, orders.PlaceOrderInput{UserID: owner, Side: order.SideBuy, Type: order.TypeLimit, PriceCents: &price, Quantity: 5})
	if err != nil {
		t.Fatalf("failed to place order: %v", err)
	}

	res, err := c.cancelOrder.Cancel(ctx, o.ID, owner)
	if err != nil {
		t.Fatalf("expected successful cancel, got %v", err)
	}
	if res.AlreadyTerminal {
		t.Fatalf("expected AlreadyTerminal=false for an OPEN order, got %+v", res)
	}

	type outboxRow struct {
		StreamName string `db:"stream_name"`
		Payload    []byte `db:"payload"`
	}
	var rows []outboxRow
	if err := c.env.DB.Select(&rows, `SELECT stream_name, payload FROM outbox_events ORDER BY id DESC LIMIT 1`); err != nil {
		t.Fatalf("failed to read outbox row: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 outbox row, got %d", len(rows))
	}
	if rows[0].StreamName != cancelTestStreamName {
		t.Fatalf("expected stream_name %q, got %q", cancelTestStreamName, rows[0].StreamName)
	}

	var decoded orders.StreamEvent
	if err := json.Unmarshal(rows[0].Payload, &decoded); err != nil {
		t.Fatalf("failed to decode outbox payload: %v", err)
	}
	if decoded.Type != orders.EventTypeCancelOrder {
		t.Fatalf("expected type CANCEL_ORDER, got %q", decoded.Type)
	}
	if decoded.OrderID != o.ID {
		t.Fatalf("expected order_id %s, got %s", o.ID, decoded.OrderID)
	}
	if decoded.RequestedBy == nil || *decoded.RequestedBy != owner {
		t.Fatalf("expected requested_by %s, got %+v", owner, decoded.RequestedBy)
	}
}

// --- applyCancel wallet-release scenarios (full round-trip) ---

func TestCancelOrder_RestingBuyLimit_FullyUnfilled_ReleasesFullBRLReservation(t *testing.T) {
	ctx := context.Background()
	c := setupCancelTestEnv(t, ctx)

	buyer := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	seedWallet(t, c.env, buyer, 1000000, 1000)

	price := int64(1000)
	o := c.placeAndProcess(t, ctx, orders.PlaceOrderInput{UserID: buyer, Side: order.SideBuy, Type: order.TypeLimit, PriceCents: &price, Quantity: 5})

	buyerWalletBeforeCancel, err := c.walletRepo.Get(ctx, buyer)
	if err != nil {
		t.Fatalf("failed to get buyer wallet: %v", err)
	}
	if buyerWalletBeforeCancel.BalanceBRLCents != 995000 || buyerWalletBeforeCancel.ReservedBRLCents != 5000 {
		t.Fatalf("expected reservation of 5000 BRL cents before cancel, got %+v", buyerWalletBeforeCancel)
	}

	res := c.cancelAndProcess(t, ctx, o.ID, buyer)
	if res.AlreadyTerminal {
		t.Fatalf("expected a real cancel, not a no-op, got %+v", res)
	}

	buyerWallet, err := c.walletRepo.Get(ctx, buyer)
	if err != nil {
		t.Fatalf("failed to get buyer wallet: %v", err)
	}
	if buyerWallet.BalanceBRLCents != 1000000 || buyerWallet.ReservedBRLCents != 0 {
		t.Fatalf("expected full BRL reservation released back (balance=1000000, reserved=0), got %+v", buyerWallet)
	}

	fetched, err := c.orderRepo.Get(ctx, o.ID)
	if err != nil {
		t.Fatalf("failed to fetch order: %v", err)
	}
	if fetched.Status != order.StatusCancelled {
		t.Fatalf("expected CANCELLED, got %s", fetched.Status)
	}
	if fetched.FilledQuantity != 0 {
		t.Fatalf("expected FilledQuantity=0, got %d", fetched.FilledQuantity)
	}
}

func TestCancelOrder_RestingSellLimit_FullyUnfilled_ReleasesFullVibraniumReservation(t *testing.T) {
	ctx := context.Background()
	c := setupCancelTestEnv(t, ctx)

	seller := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	seedWallet(t, c.env, seller, 1000000, 1000)

	price := int64(1000)
	o := c.placeAndProcess(t, ctx, orders.PlaceOrderInput{UserID: seller, Side: order.SideSell, Type: order.TypeLimit, PriceCents: &price, Quantity: 5})

	sellerWalletBeforeCancel, err := c.walletRepo.Get(ctx, seller)
	if err != nil {
		t.Fatalf("failed to get seller wallet: %v", err)
	}
	if sellerWalletBeforeCancel.BalanceVibranium != 995 || sellerWalletBeforeCancel.ReservedVibranium != 5 {
		t.Fatalf("expected reservation of 5 Vibranium before cancel, got %+v", sellerWalletBeforeCancel)
	}

	res := c.cancelAndProcess(t, ctx, o.ID, seller)
	if res.AlreadyTerminal {
		t.Fatalf("expected a real cancel, not a no-op, got %+v", res)
	}

	sellerWallet, err := c.walletRepo.Get(ctx, seller)
	if err != nil {
		t.Fatalf("failed to get seller wallet: %v", err)
	}
	if sellerWallet.BalanceVibranium != 1000 || sellerWallet.ReservedVibranium != 0 {
		t.Fatalf("expected full Vibranium reservation released back (balance=1000, reserved=0), got %+v", sellerWallet)
	}

	fetched, err := c.orderRepo.Get(ctx, o.ID)
	if err != nil {
		t.Fatalf("failed to fetch order: %v", err)
	}
	if fetched.Status != order.StatusCancelled {
		t.Fatalf("expected CANCELLED, got %s", fetched.Status)
	}
	if fetched.FilledQuantity != 0 {
		t.Fatalf("expected FilledQuantity=0, got %d", fetched.FilledQuantity)
	}
}

// TestCancelOrder_PartialFillThenCancel_ReleasesOnlyRemainder is spec §8
// item 1's "cancelamento parcial liberando só o restante" scenario — the
// one flagged as completely untested before this file existed. A SELL
// LIMIT for 10 units gets partially filled for 4, then cancelled: only the
// unfilled 6 units' worth of Vibranium reservation may release, the 4
// already-filled units (and their trade) must be left untouched.
//
// Mutation check (per task instructions): if applyCancel's
// `remaining := removed.Remaining()` were changed to use the full
// `removed.Quantity` (10) instead, releasing 10 against a reserved balance
// of only 6 (10 - 4 already consumed by the fill) would trip
// wallet.ErrReleaseExceedsReserve inside applyCancel, which surfaces here
// as ProcessOnce returning a non-nil error from cancelAndProcess's
// t.Fatalf check — so this test would fail loudly on that regression, not
// silently pass with a wrong balance.
func TestCancelOrder_PartialFillThenCancel_ReleasesOnlyRemainder(t *testing.T) {
	ctx := context.Background()
	c := setupCancelTestEnv(t, ctx)

	seller := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	buyer := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	seedWallet(t, c.env, seller, 1000000, 1000)
	seedWallet(t, c.env, buyer, 1000000, 1000)

	price := int64(1000)
	sellOrder := c.placeAndProcess(t, ctx, orders.PlaceOrderInput{UserID: seller, Side: order.SideSell, Type: order.TypeLimit, PriceCents: &price, Quantity: 10})
	c.placeAndProcess(t, ctx, orders.PlaceOrderInput{UserID: buyer, Side: order.SideBuy, Type: order.TypeLimit, PriceCents: &price, Quantity: 4})

	// Sanity-check the partial fill landed as expected before cancelling.
	afterFill, err := c.orderRepo.Get(ctx, sellOrder.ID)
	if err != nil {
		t.Fatalf("failed to fetch sell order after partial fill: %v", err)
	}
	if afterFill.Status != order.StatusPartiallyFilled || afterFill.FilledQuantity != 4 {
		t.Fatalf("expected PARTIALLY_FILLED with FilledQuantity=4, got status=%s filled=%d", afterFill.Status, afterFill.FilledQuantity)
	}
	sellerWalletAfterFill, err := c.walletRepo.Get(ctx, seller)
	if err != nil {
		t.Fatalf("failed to get seller wallet after fill: %v", err)
	}
	// 1000 - 10 (reserved at placement) = 990 available; +4*1000 BRL credited
	// by the fill; reserved Vibranium down to 10-4=6 remaining.
	if sellerWalletAfterFill.BalanceVibranium != 990 || sellerWalletAfterFill.ReservedVibranium != 6 {
		t.Fatalf("expected balance=990 reserved=6 Vibranium after partial fill, got %+v", sellerWalletAfterFill)
	}
	if sellerWalletAfterFill.BalanceBRLCents != 1004000 {
		t.Fatalf("expected BRL balance 1004000 (1000000 + 4*1000) after partial fill, got %d", sellerWalletAfterFill.BalanceBRLCents)
	}

	res := c.cancelAndProcess(t, ctx, sellOrder.ID, seller)
	if res.AlreadyTerminal {
		t.Fatalf("expected a real cancel of the PARTIALLY_FILLED order, not a no-op, got %+v", res)
	}

	// Only the remaining 6 units release: reserved goes 6 -> 0, balance
	// gains exactly 6 (never touching the 4 already-filled/settled units).
	sellerWallet, err := c.walletRepo.Get(ctx, seller)
	if err != nil {
		t.Fatalf("failed to get seller wallet after cancel: %v", err)
	}
	if sellerWallet.ReservedVibranium != 0 {
		t.Fatalf("expected reserved Vibranium fully drained to 0, got %d", sellerWallet.ReservedVibranium)
	}
	if sellerWallet.BalanceVibranium != 996 {
		t.Fatalf("expected balance Vibranium 996 (990 + 6 released remainder, NOT 990 + 10), got %d", sellerWallet.BalanceVibranium)
	}
	if sellerWallet.BalanceBRLCents != 1004000 {
		t.Fatalf("expected BRL balance unchanged by the cancel at 1004000, got %d", sellerWallet.BalanceBRLCents)
	}

	finalOrder, err := c.orderRepo.Get(ctx, sellOrder.ID)
	if err != nil {
		t.Fatalf("failed to fetch final sell order: %v", err)
	}
	if finalOrder.Status != order.StatusCancelled {
		t.Fatalf("expected CANCELLED, got %s", finalOrder.Status)
	}
	if finalOrder.FilledQuantity != 4 {
		t.Fatalf("expected FilledQuantity to remain 4 (the fill is never undone), got %d", finalOrder.FilledQuantity)
	}

	// The trade produced by the partial fill must be untouched by the cancel.
	trades, _, err := c.tradeRepo.ListPaginated(ctx, postgres.TradeFilter{Limit: 10})
	if err != nil {
		t.Fatalf("failed to list trades: %v", err)
	}
	if len(trades) != 1 || trades[0].Quantity != 4 || trades[0].PriceCents != 1000 {
		t.Fatalf("expected exactly one untouched trade qty=4 price=1000, got %+v", trades)
	}
}

// TestCancelOrder_AlreadyGoneFromBook_NoOp_NoDoubleRelease reproduces the
// race the applyCancel found==false branch guards against: a resting order
// gets fully matched away (removed from the in-memory book, order status
// -> FILLED) by an event that reaches the matcher BEFORE a CANCEL_ORDER
// event that was queued earlier (while the order was still OPEN) gets
// processed. This is engineered deterministically here by controlling
// publish/process ordering by hand: the filling NEW_ORDER event is
// enqueued (in outbox insertion order) before the CANCEL_ORDER event, so
// a single PublishOnce+ProcessOnce batch applies the fill first and the
// stale cancel second, hitting applyCancel's "no longer resting" no-op path.
func TestCancelOrder_AlreadyGoneFromBook_NoOp_NoDoubleRelease(t *testing.T) {
	ctx := context.Background()
	c := setupCancelTestEnv(t, ctx)

	seller := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	buyer := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	seedWallet(t, c.env, seller, 1000000, 1000)
	seedWallet(t, c.env, buyer, 1000000, 1000)

	price := int64(1000)
	sellOrder := c.placeAndProcess(t, ctx, orders.PlaceOrderInput{UserID: seller, Side: order.SideSell, Type: order.TypeLimit, PriceCents: &price, Quantity: 5})

	// Queue the fully-matching BUY order's NEW_ORDER outbox row FIRST...
	_, err := c.placeOrder.Place(ctx, orders.PlaceOrderInput{UserID: buyer, Side: order.SideBuy, Type: order.TypeLimit, PriceCents: &price, Quantity: 5})
	if err != nil {
		t.Fatalf("failed to place buy order: %v", err)
	}
	// ...then queue the cancel for the sell order, which is still OPEN in
	// Postgres at this exact moment (the matcher hasn't touched either
	// event yet) -> CancelOrderService legitimately enqueues it.
	res, err := c.cancelOrder.Cancel(ctx, sellOrder.ID, seller)
	if err != nil {
		t.Fatalf("failed to cancel sell order: %v", err)
	}
	if res.AlreadyTerminal {
		t.Fatalf("expected the sell order to still be OPEN at cancel time, got AlreadyTerminal=true")
	}

	// Publish both in outbox-id order (buy's NEW_ORDER, then the CANCEL_ORDER)
	// and process them as a single batch — the fill lands first, removing
	// the sell order from the book and marking it FILLED; the cancel then
	// finds nothing left to cancel.
	if n, err := c.publisher.PublishOnce(ctx); err != nil || n != 2 {
		t.Fatalf("expected to publish 2 events, got n=%d err=%v", n, err)
	}
	if n, err := c.loop.ProcessOnce(ctx); err != nil || n != 2 {
		t.Fatalf("expected to process 2 events without error (no panic/no double release), got n=%d err=%v", n, err)
	}

	finalOrder, err := c.orderRepo.Get(ctx, sellOrder.ID)
	if err != nil {
		t.Fatalf("failed to fetch final sell order: %v", err)
	}
	if finalOrder.Status != order.StatusFilled {
		t.Fatalf("expected the order to remain FILLED (the stale cancel must not revert it), got %s", finalOrder.Status)
	}
	if finalOrder.FilledQuantity != 5 {
		t.Fatalf("expected FilledQuantity=5, got %d", finalOrder.FilledQuantity)
	}

	// If applyCancel's no-op branch had instead tried to release Vibranium
	// again, ReleaseVibraniumReservation would have errored (reserved is
	// already 0 post-settlement) and the ProcessOnce check above would have
	// failed; asserting the exact final balances here additionally rules
	// out a double-credit that happened to not error.
	sellerWallet, err := c.walletRepo.Get(ctx, seller)
	if err != nil {
		t.Fatalf("failed to get seller wallet: %v", err)
	}
	if sellerWallet.ReservedVibranium != 0 {
		t.Fatalf("expected reserved Vibranium at 0 after full settlement, got %d", sellerWallet.ReservedVibranium)
	}
	if sellerWallet.BalanceVibranium != 995 {
		t.Fatalf("expected balance Vibranium 995 (1000 - 5 sold, no extra release), got %d", sellerWallet.BalanceVibranium)
	}
	if sellerWallet.BalanceBRLCents != 1005000 {
		t.Fatalf("expected BRL balance 1005000 (1000000 + 5*1000) after full settlement, got %d", sellerWallet.BalanceBRLCents)
	}
}
