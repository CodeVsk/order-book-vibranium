// internal/infra/httpapi/user_handler.go
package httpapi

import (
	"errors"
	"net/http"

	"trade-market/internal/application/users"
	"trade-market/internal/infra/postgres"
	"trade-market/internal/platform/logger"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type UserHandler struct {
	getUser   *users.GetUserQuery
	listUsers *users.ListUsersQuery
	logger    *zap.Logger
}

func NewUserHandler(getUser *users.GetUserQuery, listUsers *users.ListUsersQuery, logger *zap.Logger) *UserHandler {
	return &UserHandler{getUser: getUser, listUsers: listUsers, logger: logger}
}

func (h *UserHandler) GetUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_user_id", "malformed user id")
		return
	}

	u, err := h.getUser.Get(r.Context(), id)
	if errors.Is(err, postgres.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, "user_not_found", "no user for this id")
		return
	}
	if err != nil {
		fields := append(logger.TraceFields(r.Context()), zap.Error(err), zap.String("request_id", RequestIDFromContext(r.Context())))
		h.logger.Error("get user failed", fields...)
		writeError(w, http.StatusInternalServerError, "internal_error", "unexpected error fetching user")
		return
	}

	writeJSON(w, http.StatusOK, UserDTO{
		ID:        u.ID.String(),
		Username:  u.Username,
		CreatedAt: u.CreatedAt,
	})
}

func (h *UserHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	list, err := h.listUsers.List(r.Context())
	if err != nil {
		fields := append(logger.TraceFields(r.Context()), zap.Error(err), zap.String("request_id", RequestIDFromContext(r.Context())))
		h.logger.Error("list users failed", fields...)
		writeError(w, http.StatusInternalServerError, "internal_error", "unexpected error listing users")
		return
	}

	dtos := make([]UserDTO, 0, len(list))
	for _, u := range list {
		dtos = append(dtos, UserDTO{
			ID:        u.ID.String(),
			Username:  u.Username,
			CreatedAt: u.CreatedAt,
		})
	}

	writeJSON(w, http.StatusOK, ListUsersResponse{Users: dtos})
}
