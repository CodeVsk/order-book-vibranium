// test/integration/user_handler_test.go
//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"trade-market/internal/application/orders"
	"trade-market/internal/application/users"
	"trade-market/internal/infra/httpapi"
	"trade-market/internal/infra/postgres"
	"trade-market/test/integration/testenv"

	"github.com/google/uuid"
)

// setupUserTestEnv wires a real httpapi.NewRouter backed by the migrated
// testenv Postgres — only the users endpoints are exercised, but NewRouter
// takes every handler, so the other three are built the same minimal way
// httpTestEnv does.
func setupUserTestEnv(t *testing.T, ctx context.Context) (*testenv.Env, http.Handler) {
	t.Helper()
	env := testenv.Setup(t, ctx)

	walletRepo := postgres.NewWalletRepository(env.DB)
	orderRepo := postgres.NewOrderRepository(env.DB)
	tradeRepo := postgres.NewTradeRepository(env.DB)
	outboxRepo := postgres.NewOutboxRepository(env.DB)
	userRepo := postgres.NewUserRepository(env.DB)

	placeOrderSvc := orders.NewPlaceOrderService(env.DB, walletRepo, userRepo, orderRepo, outboxRepo, "orders:incoming", testLogger(t))
	cancelOrderSvc := orders.NewCancelOrderService(env.DB, orderRepo, outboxRepo, "orders:incoming", testLogger(t))
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

	return env, router
}

func TestUserHandler_ListUsers_ReturnsSeededUsers(t *testing.T) {
	ctx := context.Background()
	_, router := setupUserTestEnv(t, ctx)

	rec := doJSON(t, router, http.MethodGet, "/users", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp httpapi.ListUsersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Users) != 5 {
		t.Fatalf("expected 5 seeded users, got %d (%+v)", len(resp.Users), resp.Users)
	}

	wantUsernames := map[string]bool{"alice": true, "bob": true, "carol": true, "dave": true, "eve": true}
	for _, u := range resp.Users {
		if !wantUsernames[u.Username] {
			t.Fatalf("unexpected username in response: %q", u.Username)
		}
		delete(wantUsernames, u.Username)
	}
	if len(wantUsernames) != 0 {
		t.Fatalf("missing expected usernames: %+v", wantUsernames)
	}
}

func TestUserHandler_GetUser_SeededID_ReturnsMatchingUsername(t *testing.T) {
	ctx := context.Background()
	_, router := setupUserTestEnv(t, ctx)

	rec := doJSON(t, router, http.MethodGet, "/users/00000000-0000-0000-0000-000000000001", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp httpapi.UserDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Username != "alice" {
		t.Fatalf("expected username alice, got %q", resp.Username)
	}
	if resp.ID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("expected id echoed back, got %q", resp.ID)
	}
}

func TestUserHandler_GetUser_UnknownID_Returns404(t *testing.T) {
	ctx := context.Background()
	_, router := setupUserTestEnv(t, ctx)

	rec := doJSON(t, router, http.MethodGet, "/users/"+uuid.New().String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUserHandler_GetUser_MalformedID_Returns400(t *testing.T) {
	ctx := context.Background()
	_, router := setupUserTestEnv(t, ctx)

	rec := doJSON(t, router, http.MethodGet, "/users/not-a-uuid", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}
