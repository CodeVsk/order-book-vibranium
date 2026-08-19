// internal/infra/httpapi/router.go
package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/riandyrn/otelchi"
	"go.uber.org/zap"
)

func NewRouter(orderH *OrderHandler, walletH *WalletHandler, tradeH *TradeHandler, userH *UserHandler, logger *zap.Logger) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	// otelchi opens the root span for every request, named by the matched
	// chi route pattern (e.g. "POST /orders/{id}"), and extracts an inbound
	// W3C traceparent header if present via the global propagator. This
	// inbound trace context is attacker-controllable by design (W3C Trace
	// Context) — it is used only for observability correlation, never for
	// auth/identity/business-logic decisions.
	r.Use(otelchi.Middleware("trade-market-api", otelchi.WithChiRoutes(r)))
	r.Use(CorrelationID)
	r.Use(RequestLogging(logger))

	r.Get("/ping", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	r.Post("/orders", orderH.PlaceOrder)
	r.Delete("/orders/{id}", orderH.CancelOrder)
	r.Get("/orders/{id}", orderH.GetOrder)

	r.Get("/wallets/{user_id}", walletH.GetWallet)

	r.Get("/trades", tradeH.ListTrades)

	r.Get("/users", userH.ListUsers)
	r.Get("/users/{id}", userH.GetUser)

	return r
}
