// internal/infra/httpapi/trade_handler.go
package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"trade-market/internal/application/orders"
	"trade-market/internal/infra/postgres"
	"trade-market/internal/platform/logger"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type TradeHandler struct {
	listTrades *orders.ListTradesQuery
	logger     *zap.Logger
}

func NewTradeHandler(listTrades *orders.ListTradesQuery, logger *zap.Logger) *TradeHandler {
	return &TradeHandler{listTrades: listTrades, logger: logger}
}

func (h *TradeHandler) ListTrades(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := postgres.TradeFilter{Cursor: q.Get("cursor")}

	if v := q.Get("user_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_user_id", "malformed user_id")
			return
		}
		filter.UserID = &id
	}
	if v := q.Get("order_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_order_id", "malformed order_id")
			return
		}
		filter.OrderID = &id
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
			return
		}
		filter.Limit = n // ListPaginated already clamps anything >200 or <=0 down to the default of 50
	}

	trades, nextCursor, err := h.listTrades.List(r.Context(), filter)
	if errors.Is(err, postgres.ErrInvalidCursor) {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "malformed or expired cursor")
		return
	}
	if err != nil {
		fields := append(logger.TraceFields(r.Context()), zap.Error(err), zap.String("request_id", RequestIDFromContext(r.Context())))
		h.logger.Error("list trades failed", fields...)
		writeError(w, http.StatusInternalServerError, "internal_error", "unexpected error listing trades")
		return
	}

	dtos := make([]TradeDTO, 0, len(trades))
	for _, t := range trades {
		dtos = append(dtos, TradeDTO{
			TradeID:     t.ID.String(),
			BuyOrderID:  t.BuyOrderID.String(),
			SellOrderID: t.SellOrderID.String(),
			PriceCents:  t.PriceCents,
			Quantity:    t.Quantity,
			ExecutedAt:  t.ExecutedAt,
		})
	}

	writeJSON(w, http.StatusOK, ListTradesResponse{Trades: dtos, NextCursor: nextCursor})
}
