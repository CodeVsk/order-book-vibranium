// internal/infra/postgres/outbox_repo.go
package postgres

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
)

type OutboxRepository struct {
	db *sqlx.DB
}

func NewOutboxRepository(db *sqlx.DB) *OutboxRepository {
	return &OutboxRepository{db: db}
}

// OutboxEvent mirrors one row of outbox_events.
type OutboxEvent struct {
	ID         int64  `db:"id"`
	StreamName string `db:"stream_name"`
	Payload    []byte `db:"payload"`
}

// Insert writes a new outbox event inside tx — this is what makes the
// wallet reservation and the "will eventually be published to Redis"
// guarantee atomic (transactional outbox pattern).
func (r *OutboxRepository) Insert(ctx context.Context, tx *sqlx.Tx, streamName string, payload []byte) error {
	const q = `INSERT INTO outbox_events (stream_name, payload) VALUES ($1, $2)`
	_, err := tx.ExecContext(ctx, q, streamName, payload)
	return err
}

// FetchUnpublishedBatch locks up to limit unpublished rows with SKIP LOCKED
// so multiple publisher replicas (if ever run) never double-publish the
// same row.
func (r *OutboxRepository) FetchUnpublishedBatch(ctx context.Context, tx *sqlx.Tx, limit int) ([]OutboxEvent, error) {
	var rows []OutboxEvent
	const q = `SELECT id, stream_name, payload FROM outbox_events
	           WHERE published = false ORDER BY id ASC LIMIT $1 FOR UPDATE SKIP LOCKED`
	if err := tx.SelectContext(ctx, &rows, q, limit); err != nil {
		return nil, err
	}
	return rows, nil
}

// MarkPublished flips published=true for the given ids inside tx.
func (r *OutboxRepository) MarkPublished(ctx context.Context, tx *sqlx.Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	const q = `UPDATE outbox_events SET published = true WHERE id = ANY($1)`
	_, err := tx.ExecContext(ctx, q, ids)
	return err
}

// LockIfUnpublished re-locks a single candidate row FOR UPDATE SKIP LOCKED,
// re-checking published=false at lock time. Returns false (no error) if the
// row is already published (e.g. a previous, since-committed publish of
// this same id) or is currently locked by a concurrent publisher — both
// are safe to skip, not errors. This is the per-event lock used by the
// publisher to commit each publish+mark atomically, so a mid-batch
// failure never re-publishes an event that already succeeded.
func (r *OutboxRepository) LockIfUnpublished(ctx context.Context, tx *sqlx.Tx, id int64) (bool, error) {
	var locked bool
	const q = `SELECT true FROM outbox_events WHERE id = $1 AND published = false FOR UPDATE SKIP LOCKED`
	if err := tx.GetContext(ctx, &locked, q, id); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return locked, nil
}
