// internal/infra/httpapi/wallet_handler.go
package httpapi

import (
	"errors"
	"net/http"

	"trade-market/internal/application/orders"
	"trade-market/internal/infra/postgres"
	"trade-market/internal/platform/logger"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type WalletHandler struct {
	getWallet *orders.GetWalletQuery
	logger    *zap.Logger
}

func NewWalletHandler(getWallet *orders.GetWalletQuery, logger *zap.Logger) *WalletHandler {
	return &WalletHandler{getWallet: getWallet, logger: logger}
}

func (h *WalletHandler) GetWallet(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(chi.URLParam(r, "user_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_user_id", "malformed user id")
		return
	}

	wal, err := h.getWallet.Get(r.Context(), userID)
	if errors.Is(err, postgres.ErrWalletNotFound) {
		writeError(w, http.StatusNotFound, "user_not_found", "no wallet for this user_id")
		return
	}
	if err != nil {
		fields := append(logger.TraceFields(r.Context()), zap.Error(err), zap.String("request_id", RequestIDFromContext(r.Context())))
		h.logger.Error("get wallet failed", fields...)
		writeError(w, http.StatusInternalServerError, "internal_error", "unexpected error fetching wallet")
		return
	}

	writeJSON(w, http.StatusOK, GetWalletResponse{
		UserID:             wal.UserID.String(),
		BalanceBRLCents:    wal.BalanceBRLCents,
		ReservedBRLCents:   wal.ReservedBRLCents,
		AvailableBRLCents:  wal.AvailableBRLCents(),
		BalanceVibranium:   wal.BalanceVibranium,
		ReservedVibranium:  wal.ReservedVibranium,
		AvailableVibranium: wal.AvailableVibranium(),
	})
}
