// test/integration/outbox_partial_batch_test.go
//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"trade-market/internal/application/outboxapp"
	"trade-market/internal/infra/postgres"
	"trade-market/internal/infra/redisstream"
	"trade-market/test/integration/testenv"
)

// failNthProducer wraps a real redisstream.Producer but returns an error on
// the Nth call (1-indexed), succeeding on every other call. Used to
// deterministically reproduce a mid-batch publish failure: some events in
// the batch succeed before the failure, some never get attempted after it.
type failNthProducer struct {
	real  *redisstream.Producer
	failN int
	calls int
}

func (f *failNthProducer) Publish(ctx context.Context, stream string, payload []byte) (string, error) {
	f.calls++
	if f.calls == f.failN {
		return "", errors.New("simulated transient redis failure")
	}
	return f.real.Publish(ctx, stream, payload)
}

// TestOutboxPublisher_MidBatchFailure_NeverDuplicatesAlreadyPublishedEvents
// reproduces the exact gap flagged by the final holistic review: a batch of
// 3 outbox events where the 2nd publish fails. Before the fix, the whole
// batch's MarkPublished was deferred to the end of the loop inside one
// wrapping transaction, so the rollback on failure would have made event 1
// (already durably XAdd'd) look unpublished again — the next retry would
// have sent it a second time as a distinct stream entry. This test asserts
// that never happens: after the failure and a subsequent successful retry,
// each of the 3 events appears in the stream exactly once.
func TestOutboxPublisher_MidBatchFailure_NeverDuplicatesAlreadyPublishedEvents(t *testing.T) {
	ctx := context.Background()
	env := testenv.Setup(t, ctx)

	const streamName = "orders:incoming"
	outboxRepo := postgres.NewOutboxRepository(env.DB)

	// Seed 3 outbox rows directly — this test is about publisher/repo
	// batch semantics, not order placement, so bypass PlaceOrderService.
	tx, err := env.DB.Beginx()
	if err != nil {
		t.Fatalf("failed to begin seed tx: %v", err)
	}
	payloads := [][]byte{[]byte(`{"seq":1}`), []byte(`{"seq":2}`), []byte(`{"seq":3}`)}
	for _, p := range payloads {
		if err := outboxRepo.Insert(ctx, tx, streamName, p); err != nil {
			t.Fatalf("failed to seed outbox event: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("failed to commit seed tx: %v", err)
	}

	realProducer := redisstream.NewProducer(env.Redis)
	flaky := &failNthProducer{real: realProducer, failN: 2} // event 1 succeeds, event 2 fails, event 3 never attempted
	publisher := outboxapp.NewPublisher(env.DB, outboxRepo, flaky, 100, testLogger(t))

	n, err := publisher.PublishOnce(ctx)
	if err == nil {
		t.Fatalf("expected PublishOnce to report the simulated failure")
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 event published before the failure, got %d", n)
	}

	var publishedCount, unpublishedCount int
	if err := env.DB.Get(&publishedCount, `SELECT count(*) FROM outbox_events WHERE published = true`); err != nil {
		t.Fatalf("failed to count published events: %v", err)
	}
	if err := env.DB.Get(&unpublishedCount, `SELECT count(*) FROM outbox_events WHERE published = false`); err != nil {
		t.Fatalf("failed to count unpublished events: %v", err)
	}
	if publishedCount != 1 {
		t.Fatalf("expected event 1 to be durably marked published despite the batch failing, got published count=%d", publishedCount)
	}
	if unpublishedCount != 2 {
		t.Fatalf("expected events 2 and 3 to remain unpublished for retry, got unpublished count=%d", unpublishedCount)
	}

	// Retry with a working producer — simulates the next tick, after
	// whatever transient Redis problem caused the first failure clears up.
	retryPublisher := outboxapp.NewPublisher(env.DB, outboxRepo, realProducer, 100, testLogger(t))
	n, err = retryPublisher.PublishOnce(ctx)
	if err != nil {
		t.Fatalf("expected retry to succeed, got err: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected exactly 2 events published on retry (events 2 and 3), got %d", n)
	}

	if err := env.DB.Get(&unpublishedCount, `SELECT count(*) FROM outbox_events WHERE published = false`); err != nil {
		t.Fatalf("failed to count unpublished events: %v", err)
	}
	if unpublishedCount != 0 {
		t.Fatalf("expected zero unpublished events after retry, got %d", unpublishedCount)
	}

	// The crux of the regression: the stream must have exactly 3 entries,
	// one per outbox row — never 4 (which a duplicate re-send of event 1
	// would produce) and never fewer than 3 (which lost work would produce).
	streamLen, err := env.Redis.XLen(ctx, streamName).Result()
	if err != nil {
		t.Fatalf("failed to get stream length: %v", err)
	}
	if streamLen != 3 {
		t.Fatalf("expected exactly 3 stream entries (no duplicates, no loss), got %d", streamLen)
	}

	// Belt-and-suspenders: confirm each seeded payload appears exactly once
	// in the stream, not just that the total count happens to be 3. Compare
	// by decoded "seq" field rather than raw bytes: the payload column is
	// JSONB, so Postgres normalizes formatting on the way back out (e.g.
	// adds a space after ':') — that's expected, unrelated to the fix under
	// test, so decode both sides instead of doing a brittle byte comparison.
	entries, err := env.Redis.XRange(ctx, streamName, "-", "+").Result()
	if err != nil {
		t.Fatalf("failed to read stream entries: %v", err)
	}
	seen := map[int]int{}
	for _, e := range entries {
		payload, _ := e.Values["payload"].(string)
		var decoded struct {
			Seq int `json:"seq"`
		}
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			t.Fatalf("failed to decode stream entry payload %q: %v", payload, err)
		}
		seen[decoded.Seq]++
	}
	for seq := 1; seq <= len(payloads); seq++ {
		if seen[seq] != 1 {
			t.Fatalf("expected seq=%d to appear exactly once in the stream, got %d occurrences", seq, seen[seq])
		}
	}
}
