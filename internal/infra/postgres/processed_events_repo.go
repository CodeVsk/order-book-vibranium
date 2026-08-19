// internal/infra/postgres/processed_events_repo.go
package postgres

import (
	"context"

	"github.com/jmoiron/sqlx"
)

type ProcessedEventsRepository struct {
	db *sqlx.DB
}

func NewProcessedEventsRepository(db *sqlx.DB) *ProcessedEventsRepository {
	return &ProcessedEventsRepository{db: db}
}

// TryMark attempts to record streamEntryID as processed inside tx. Returns
// true if this is the first time it has been seen (the caller should apply
// its effects), false if it was already recorded (the caller must skip
// re-applying effects — this is the idempotency guard against redelivery
// after a crash between commit and XACK).
func (r *ProcessedEventsRepository) TryMark(ctx context.Context, tx *sqlx.Tx, streamEntryID, streamName string) (bool, error) {
	const q = `INSERT INTO processed_stream_events (stream_entry_id, stream_name)
	           VALUES ($1, $2) ON CONFLICT (stream_entry_id) DO NOTHING`
	res, err := tx.ExecContext(ctx, q, streamEntryID, streamName)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}
