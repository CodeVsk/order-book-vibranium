// internal/infra/postgres/user_repo.go
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"trade-market/internal/domain/user"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

var ErrUserNotFound = errors.New("postgres: user not found")

type UserRepository struct {
	db *sqlx.DB
}

func NewUserRepository(db *sqlx.DB) *UserRepository {
	return &UserRepository{db: db}
}

type userRow struct {
	ID        uuid.UUID `db:"id"`
	Username  string    `db:"username"`
	CreatedAt time.Time `db:"created_at"`
}

func toUser(r userRow) *user.User {
	return &user.User{ID: r.ID, Username: r.Username, CreatedAt: r.CreatedAt}
}

// Get performs a plain (non-locking) read, used by GET /users/{id}.
func (r *UserRepository) Get(ctx context.Context, id uuid.UUID) (*user.User, error) {
	var row userRow
	const q = `SELECT id, username, created_at FROM users WHERE id = $1`
	if err := r.db.GetContext(ctx, &row, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return toUser(row), nil
}

// Exists reports whether a user_id is present in the users table — a cheap
// pre-check used by PlaceOrderService.Place to reject unknown user_ids before
// touching wallets/orders, without paying for the extra columns Get fetches.
func (r *UserRepository) Exists(ctx context.Context, id uuid.UUID) (bool, error) {
	var exists bool
	const q = `SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)`
	if err := r.db.GetContext(ctx, &exists, q, id); err != nil {
		return false, err
	}
	return exists, nil
}

// List returns every user, ordered by username — used by GET /users. No
// pagination: the seed set is small and fixed for this MVP.
func (r *UserRepository) List(ctx context.Context) ([]*user.User, error) {
	var rows []userRow
	const q = `SELECT id, username, created_at FROM users ORDER BY username`
	if err := r.db.SelectContext(ctx, &rows, q); err != nil {
		return nil, err
	}
	result := make([]*user.User, 0, len(rows))
	for _, row := range rows {
		result = append(result, toUser(row))
	}
	return result, nil
}
