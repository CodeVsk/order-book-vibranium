// test/integration/order_handler_test.go
//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"trade-market/internal/application/matcherapp"
	"trade-market/internal/application/orders"
	"trade-market/internal/application/outboxapp"
	"trade-market/internal/application/users"
	"trade-market/internal/domain/matching"
	"trade-market/internal/infra/httpapi"
	"trade-market/internal/infra/postgres"
	"trade-market/internal/infra/redisstream"
	"trade-market/test/integration/testenv"

	"github.com/google/uuid"
)

// httpTestEnv wires a real httpapi.NewRouter backed by the same
// DB/Redis-connected services cancelTestEnv uses (see cancel_order_test.go),
// plus a publisher+matcher Loop the test drives directly — there is no HTTP
// endpoint for the matcher — so a test can push an order into a FILLED or
// CANCELLED terminal state before exercising the HTTP layer's status-code
// mapping for DELETE /orders/{id}.
type httpTestEnv struct {
	env       *testenv.Env
	router    http.Handler
	publisher *outboxapp.Publisher
	loop      *matcherapp.Loop
}

func setupHTTPTestEnv(t *testing.T, ctx context.Context) *httpTestEnv {
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
	getOrderQuery := orders.NewGetOrderQuery(orderRepo)
	getWalletQuery := orders.NewGetWalletQuery(walletRepo)
	listTradesQuery := orders.NewListTradesQuery(tradeRepo)
	getUserQuery := users.NewGetUserQuery(userRepo)
	listUsersQuery := users.NewListUsersQuery(userRepo)

	orderH := httpapi.NewOrderHandler(placeOrderSvc, cancelOrderSvc, getOrderQuery, testLogger(t))
	walletH := httpapi.NewWalletHandler(getWalletQuery, testLogger(t))
	tradeH := httpapi.NewTradeHandler(listTradesQuery, testLogger(t))
	userH := httpapi.NewUserHandler(getUserQuery, listUsersQuery, testLogger(t))
	router := httpapi.NewRouter(orderH, walletH, tradeH, userH, testLogger(t))

	producer := redisstream.NewProducer(env.Redis)
	publisher := outboxapp.NewPublisher(env.DB, outboxRepo, producer, 100, testLogger(t))

	consumer, err := redisstream.NewConsumer(ctx, env.Redis, cancelTestStreamName, "matcher-group", "matcher-1")
	if err != nil {
		t.Fatalf("failed to create consumer: %v", err)
	}
	book := matching.NewBook()
	loop := matcherapp.NewLoop(env.DB, consumer, book, walletRepo, orderRepo, tradeRepo, processedRepo, cancelTestStreamName, 200, 50*time.Millisecond, testLogger(t))

	return &httpTestEnv{env: env, router: router, publisher: publisher, loop: loop}
}

// advanceOneEvent publishes and processes exactly one pending outbox event
// — used to drive an order placed/cancelled through the HTTP layer past the
// matcher without running a background goroutine, keeping each test
// deterministic and single-threaded.
func (h *httpTestEnv) advanceOneEvent(t *testing.T, ctx context.Context) {
	t.Helper()
	if n, err := h.publisher.PublishOnce(ctx); err != nil || n != 1 {
		t.Fatalf("expected to publish 1 event, got n=%d err=%v", n, err)
	}
	if n, err := h.loop.ProcessOnce(ctx); err != nil || n != 1 {
		t.Fatalf("expected to process 1 event, got n=%d err=%v", n, err)
	}
}

// doJSON drives router directly with net/http/httptest, per the task's
// "httptest.NewRecorder() + router.ServeHTTP" option — no real listening
// socket is needed since everything runs in-process against the real DB.
func doJSON(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("failed to marshal request body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestOrderHandler_PlaceOrder_Success_Returns202(t *testing.T) {
	ctx := context.Background()
	h := setupHTTPTestEnv(t, ctx)

	userID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	seedWallet(t, h.env, userID, 1000000, 1000)

	rec := doJSON(t, h.router, http.MethodPost, "/orders", map[string]any{
		"user_id": userID.String(), "side": "BUY", "type": "LIMIT", "price_cents": 1000, "quantity": 5,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp httpapi.PlaceOrderResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "OPEN" {
		t.Fatalf("expected status OPEN, got %s", resp.Status)
	}
	if _, err := uuid.Parse(resp.OrderID); err != nil {
		t.Fatalf("expected a valid order_id, got %q", resp.OrderID)
	}
}

func TestOrderHandler_PlaceOrder_UnknownUser_Returns404(t *testing.T) {
	ctx := context.Background()
	h := setupHTTPTestEnv(t, ctx)

	rec := doJSON(t, h.router, http.MethodPost, "/orders", map[string]any{
		"user_id": uuid.New().String(), "side": "BUY", "type": "LIMIT", "price_cents": 1000, "quantity": 5,
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a user_id with no seeded wallet, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestOrderHandler_PlaceOrder_UnknownUser_ReturnsUserNotFoundCode locks in
// the distinct error code for "user_id isn't in the users table at all" —
// added alongside the existing UnknownUser_Returns404 test (left unmodified,
// since it only asserts the status code and still passes: a fresh uuid.New()
// isn't in users either, so the pre-check now 404s it first).
func TestOrderHandler_PlaceOrder_UnknownUser_ReturnsUserNotFoundCode(t *testing.T) {
	ctx := context.Background()
	h := setupHTTPTestEnv(t, ctx)

	rec := doJSON(t, h.router, http.MethodPost, "/orders", map[string]any{
		"user_id": uuid.New().String(), "side": "BUY", "type": "LIMIT", "price_cents": 1000, "quantity": 5,
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown user_id, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp httpapi.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if resp.Code != "user_not_found" {
		t.Fatalf("expected code=user_not_found, got %q", resp.Code)
	}
}

// TestOrderHandler_PlaceOrder_UserExistsNoWallet_ReturnsWalletNotFoundCode
// covers the case a plain wallet-existence check couldn't distinguish
// before: a real users row with no matching wallet.
func TestOrderHandler_PlaceOrder_UserExistsNoWallet_ReturnsWalletNotFoundCode(t *testing.T) {
	ctx := context.Background()
	h := setupHTTPTestEnv(t, ctx)

	userID := uuid.New()
	seedUser(t, h.env, userID, "no-wallet-user")

	rec := doJSON(t, h.router, http.MethodPost, "/orders", map[string]any{
		"user_id": userID.String(), "side": "BUY", "type": "LIMIT", "price_cents": 1000, "quantity": 5,
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a user with no wallet, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp httpapi.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if resp.Code != "wallet_not_found" {
		t.Fatalf("expected code=wallet_not_found, got %q", resp.Code)
	}
}

func TestOrderHandler_GetOrder_NotFound_Returns404(t *testing.T) {
	ctx := context.Background()
	h := setupHTTPTestEnv(t, ctx)

	rec := doJSON(t, h.router, http.MethodGet, "/orders/"+uuid.New().String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOrderHandler_CancelOrder_WrongUser_Returns403(t *testing.T) {
	ctx := context.Background()
	h := setupHTTPTestEnv(t, ctx)

	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	stranger := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	seedWallet(t, h.env, owner, 1000000, 1000)
	seedWallet(t, h.env, stranger, 1000000, 1000)

	placeRec := doJSON(t, h.router, http.MethodPost, "/orders", map[string]any{
		"user_id": owner.String(), "side": "BUY", "type": "LIMIT", "price_cents": 1000, "quantity": 5,
	})
	var placed httpapi.PlaceOrderResponse
	if err := json.Unmarshal(placeRec.Body.Bytes(), &placed); err != nil {
		t.Fatalf("failed to decode place response: %v", err)
	}

	rec := doJSON(t, h.router, http.MethodDelete, "/orders/"+placed.OrderID, map[string]any{"user_id": stranger.String()})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 cancelling someone else's order, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOrderHandler_CancelOrder_NotFound_Returns404(t *testing.T) {
	ctx := context.Background()
	h := setupHTTPTestEnv(t, ctx)

	userID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	seedWallet(t, h.env, userID, 1000000, 1000)

	rec := doJSON(t, h.router, http.MethodDelete, "/orders/"+uuid.New().String(), map[string]any{"user_id": userID.String()})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a non-existent order id, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOrderHandler_CancelOrder_ValidOpenOrder_Returns202(t *testing.T) {
	ctx := context.Background()
	h := setupHTTPTestEnv(t, ctx)

	userID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	seedWallet(t, h.env, userID, 1000000, 1000)

	placeRec := doJSON(t, h.router, http.MethodPost, "/orders", map[string]any{
		"user_id": userID.String(), "side": "BUY", "type": "LIMIT", "price_cents": 1000, "quantity": 5,
	})
	var placed httpapi.PlaceOrderResponse
	if err := json.Unmarshal(placeRec.Body.Bytes(), &placed); err != nil {
		t.Fatalf("failed to decode place response: %v", err)
	}

	rec := doJSON(t, h.router, http.MethodDelete, "/orders/"+placed.OrderID, map[string]any{"user_id": userID.String()})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 cancelling a valid OPEN order, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp httpapi.CancelOrderResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.OrderID != placed.OrderID {
		t.Fatalf("expected order_id %s, got %s", placed.OrderID, resp.OrderID)
	}
}

func TestOrderHandler_CancelOrder_AlreadyTerminal_Returns200(t *testing.T) {
	ctx := context.Background()
	h := setupHTTPTestEnv(t, ctx)

	seller := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	buyer := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	seedWallet(t, h.env, seller, 1000000, 1000)
	seedWallet(t, h.env, buyer, 1000000, 1000)

	sellRec := doJSON(t, h.router, http.MethodPost, "/orders", map[string]any{
		"user_id": seller.String(), "side": "SELL", "type": "LIMIT", "price_cents": 1000, "quantity": 5,
	})
	var sellPlaced httpapi.PlaceOrderResponse
	if err := json.Unmarshal(sellRec.Body.Bytes(), &sellPlaced); err != nil {
		t.Fatalf("failed to decode sell place response: %v", err)
	}
	h.advanceOneEvent(t, ctx) // rest the sell order on the book

	buyRec := doJSON(t, h.router, http.MethodPost, "/orders", map[string]any{
		"user_id": buyer.String(), "side": "BUY", "type": "LIMIT", "price_cents": 1000, "quantity": 5,
	})
	if buyRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 placing the matching buy order, got %d body=%s", buyRec.Code, buyRec.Body.String())
	}
	h.advanceOneEvent(t, ctx) // fully fill the sell order -> FILLED

	rec := doJSON(t, h.router, http.MethodDelete, "/orders/"+sellPlaced.OrderID, map[string]any{"user_id": seller.String()})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 cancelling an already-FILLED order, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp httpapi.CancelOrderResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "FILLED" {
		t.Fatalf("expected status FILLED echoed back, got %s", resp.Status)
	}
}
