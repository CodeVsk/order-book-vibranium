// internal/infra/httpapi/order_handler.go
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"trade-market/internal/application/orders"
	"trade-market/internal/domain/order"
	"trade-market/internal/infra/postgres"
	"trade-market/internal/platform/logger"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const maxRequestBodyBytes = 16 * 1024 // 16KB — generous for these small JSON payloads, bounds memory use

type OrderHandler struct {
	placeOrder  *orders.PlaceOrderService
	cancelOrder *orders.CancelOrderService
	getOrder    *orders.GetOrderQuery
	validate    *validator.Validate
	logger      *zap.Logger
}

func NewOrderHandler(placeOrder *orders.PlaceOrderService, cancelOrder *orders.CancelOrderService, getOrder *orders.GetOrderQuery, logger *zap.Logger) *OrderHandler {
	return &OrderHandler{placeOrder: placeOrder, cancelOrder: cancelOrder, getOrder: getOrder, validate: validator.New(), logger: logger}
}

func (h *OrderHandler) PlaceOrder(w http.ResponseWriter, r *http.Request) {
	var req PlaceOrderRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_payload", "malformed JSON body")
		return
	}
	if err := h.validate.Struct(req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_payload", err.Error())
		return
	}

	userID, _ := uuid.Parse(req.UserID)
	input := orders.PlaceOrderInput{
		UserID:     userID,
		Side:       order.Side(req.Side),
		Type:       order.Type(req.Type),
		PriceCents: req.PriceCents,
		Quantity:   req.Quantity,
	}

	o, err := h.placeOrder.Place(r.Context(), input)
	switch {
	case errors.Is(err, orders.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "user_not_found", "user_id does not exist")
		return
	case errors.Is(err, orders.ErrWalletNotFound):
		writeError(w, http.StatusNotFound, "wallet_not_found", "no wallet provisioned for this user_id")
		return
	case errors.Is(err, orders.ErrInsufficientBalance):
		writeError(w, http.StatusConflict, "insufficient_balance", "not enough available balance to place this order")
		return
	case err != nil:
		fields := append(logger.TraceFields(r.Context()), zap.Error(err), zap.String("request_id", RequestIDFromContext(r.Context())))
		h.logger.Error("place order failed", fields...)
		writeError(w, http.StatusInternalServerError, "internal_error", "unexpected error placing order")
		return
	}

	writeJSON(w, http.StatusAccepted, PlaceOrderResponse{
		OrderID:   o.ID.String(),
		Status:    string(o.Status),
		CreatedAt: o.CreatedAt,
	})
}

func (h *OrderHandler) CancelOrder(w http.ResponseWriter, r *http.Request) {
	orderID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_order_id", "malformed order id")
		return
	}

	var req CancelOrderRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_payload", "malformed JSON body")
		return
	}
	if err := h.validate.Struct(req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_payload", err.Error())
		return
	}
	userID, _ := uuid.Parse(req.UserID)

	res, err := h.cancelOrder.Cancel(r.Context(), orderID, userID)
	switch {
	case errors.Is(err, orders.ErrOrderNotFound):
		writeError(w, http.StatusNotFound, "order_not_found", "no such order")
		return
	case errors.Is(err, orders.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "order belongs to a different user")
		return
	case err != nil:
		fields := append(logger.TraceFields(r.Context()), zap.Error(err), zap.String("request_id", RequestIDFromContext(r.Context())))
		h.logger.Error("cancel order failed", fields...)
		writeError(w, http.StatusInternalServerError, "internal_error", "unexpected error cancelling order")
		return
	}

	status := http.StatusAccepted
	if res.AlreadyTerminal {
		status = http.StatusOK
	}
	writeJSON(w, status, CancelOrderResponse{OrderID: res.OrderID.String(), Status: string(res.Status)})
}

func (h *OrderHandler) GetOrder(w http.ResponseWriter, r *http.Request) {
	orderID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_order_id", "malformed order id")
		return
	}

	o, err := h.getOrder.Get(r.Context(), orderID)
	if errors.Is(err, postgres.ErrOrderNotFound) {
		writeError(w, http.StatusNotFound, "order_not_found", "no such order")
		return
	}
	if err != nil {
		fields := append(logger.TraceFields(r.Context()), zap.Error(err), zap.String("request_id", RequestIDFromContext(r.Context())))
		h.logger.Error("get order failed", fields...)
		writeError(w, http.StatusInternalServerError, "internal_error", "unexpected error fetching order")
		return
	}

	writeJSON(w, http.StatusOK, GetOrderResponse{
		OrderID:        o.ID.String(),
		Status:         string(o.Status),
		FilledQuantity: o.FilledQuantity,
		Quantity:       o.Quantity,
		PriceCents:     o.PriceCents,
		Side:           string(o.Side),
		Type:           string(o.Type),
		CreatedAt:      o.CreatedAt,
		UpdatedAt:      o.UpdatedAt,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorResponse{Code: code, Message: message})
}
