// internal/application/users/queries.go
package users

import (
	"context"

	"trade-market/internal/domain/user"
	"trade-market/internal/infra/postgres"

	"github.com/google/uuid"
)

type GetUserQuery struct {
	users *postgres.UserRepository
}

func NewGetUserQuery(users *postgres.UserRepository) *GetUserQuery {
	return &GetUserQuery{users: users}
}

func (q *GetUserQuery) Get(ctx context.Context, id uuid.UUID) (*user.User, error) {
	return q.users.Get(ctx, id)
}

type ListUsersQuery struct {
	users *postgres.UserRepository
}

func NewListUsersQuery(users *postgres.UserRepository) *ListUsersQuery {
	return &ListUsersQuery{users: users}
}

func (q *ListUsersQuery) List(ctx context.Context) ([]*user.User, error) {
	return q.users.List(ctx)
}
