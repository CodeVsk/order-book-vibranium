// internal/domain/user/user.go
package user

import (
	"time"

	"github.com/google/uuid"
)

// User is a plain identity record — read-only from this system's
// perspective (seeded via migration, never created/mutated through the
// API), so unlike order.Order or wallet.Wallet it carries no behavior.
type User struct {
	ID        uuid.UUID
	Username  string
	CreatedAt time.Time
}
