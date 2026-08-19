// internal/application/matcherapp/consumer_loop.go
package matcherapp

import (
	"context"
	"encoding/json"
	"time"

	"trade-market/internal/application/orders"
	"trade-market/internal/domain/matching"
	"trade-market/internal/domain/order"
	"trade-market/internal/domain/trade"
	"trade-market/internal/domain/wallet"
	"trade-market/internal/infra/postgres"
	"trade-market/internal/infra/redisstream"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

var tracer = otel.Tracer("trade-market/matcherapp")

type Loop struct {
	db              *sqlx.DB
	consumer        *redisstream.Consumer
	book            *matching.Book
	wallets         *postgres.WalletRepository
	orderRepo       *postgres.OrderRepository
	tradeRepo       *postgres.TradeRepository
	processedEvents *postgres.ProcessedEventsRepository
	streamName      string
	batchSize       int64
	batchTimeout    time.Duration
	logger          *zap.Logger
	bookDirty       bool // set true only when applyBatch (which mutates l.book) fails; cleared once resyncBook succeeds
}

func NewLoop(
	db *sqlx.DB,
	consumer *redisstream.Consumer,
	book *matching.Book,
	wallets *postgres.WalletRepository,
	orderRepo *postgres.OrderRepository,
	tradeRepo *postgres.TradeRepository,
	processedEvents *postgres.ProcessedEventsRepository,
	streamName string,
	batchSize int64,
	batchTimeout time.Duration,
	logger *zap.Logger,
) *Loop {
	return &Loop{
		db: db, consumer: consumer, book: book, wallets: wallets, orderRepo: orderRepo,
		tradeRepo: tradeRepo, processedEvents: processedEvents, streamName: streamName,
		batchSize: batchSize, batchTimeout: batchTimeout, logger: logger,
	}
}

// ProcessOnce reads one micro-batch of never-delivered entries, applies it
// transactionally, and ACKs only after commit succeeds. Returns the number
// of stream entries read (0 on a read timeout with nothing pending).
func (l *Loop) ProcessOnce(ctx context.Context) (int, error) {
	msgs, err := l.consumer.ReadBatch(ctx, l.batchSize, l.batchTimeout)
	if err != nil {
		return 0, err
	}
	return l.processMessages(ctx, msgs)
}

// ReclaimPending drains this consumer's Pending Entries List (PEL) — stream
// entries that were previously delivered to this exact consumer name but
// never ACKed, e.g. because the process crashed after XREADGROUP but
// before XAck, or because a prior applyBatch failed and left them pending
// (see Run's retry path). Call this once at boot, BEFORE the main
// ProcessOnce/">" polling loop starts, AND again after any process-cycle
// failure, so a stuck entry gets reprocessed through the same
// idempotency-guarded path (processedEvents.TryMark) instead of sitting in
// the PEL forever. Loops until ReadPending returns an empty batch, meaning
// the PEL for this consumer is fully drained.
func (l *Loop) ReclaimPending(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		msgs, err := l.consumer.ReadPending(ctx, l.batchSize)
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			return nil
		}
		n, err := l.processMessages(ctx, msgs)
		if err != nil {
			return err
		}
		l.logger.Info("reclaimed pending entries", zap.Int("count", n))
	}
}

// processMessages applies a batch (already read, by whatever means) and
// ACKs it only after the transaction commits. Shared by ProcessOnce (the
// steady-state ">" polling path) and ReclaimPending (the PEL drain path,
// run at boot and after any failure) — both need identical apply-then-ack
// semantics.
func (l *Loop) processMessages(ctx context.Context, msgs []redis.XMessage) (int, error) {
	if len(msgs) == 0 {
		return 0, nil
	}

	if err := l.applyBatch(ctx, msgs); err != nil {
		l.bookDirty = true
		return 0, err // NOT acked: stays in this consumer's PEL, retried by ReclaimPending
	}

	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	if err := l.consumer.Ack(ctx, ids...); err != nil {
		return len(msgs), err
	}
	return len(msgs), nil
}

// resyncBook rebuilds the in-memory Book from Postgres, discarding any
// in-memory mutations left over from a batch whose surrounding transaction
// was rolled back. Book.AddResting/Cancel mutate state directly and are
// NOT part of the Postgres transaction, so a failed applyBatch can leave
// the in-memory Book diverged from the (rolled-back-to-consistent)
// Postgres state. Must be called after any ProcessOnce/applyBatch failure
// to restore the invariant that the Book is always a consistent derived
// view of Postgres before processing resumes.
func (l *Loop) resyncBook(ctx context.Context) error {
	book, err := RecoverBook(ctx, l.orderRepo)
	if err != nil {
		return err
	}
	l.book = book
	return nil
}

// Run first reclaims any crash-orphaned pending entries (see ReclaimPending),
// then calls ProcessOnce forever until ctx is cancelled. Any failure is
// handled by recoverAfterFailure before the loop continues — see that
// function's doc comment for why the ordering there matters.
func (l *Loop) Run(ctx context.Context) error {
	if err := l.ReclaimPending(ctx); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if _, err := l.ProcessOnce(ctx); err != nil {
			l.logger.Error("process cycle failed", zap.Error(err))
			if err := l.recoverAfterFailure(ctx); err != nil {
				return err
			}
		}
	}
}

// recoverAfterFailure restores the invariants needed before processing may
// resume after a ProcessOnce failure. If l.bookDirty (set only when
// applyBatch itself failed), it blocks retrying resyncBook until the Book is
// confirmed consistent again — only then does it reclaim entries left
// pending in the PEL. This order is critical: ReclaimPending re-runs
// matching and commits to Postgres, and must never do so against a
// known-diverged Book. Returns non-nil only if ctx was cancelled while
// waiting on a resync retry, so Run can exit cleanly during shutdown.
func (l *Loop) recoverAfterFailure(ctx context.Context) error {
	for l.bookDirty {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := l.resyncBook(ctx); err != nil {
			l.logger.Error("failed to resync book after prior batch failure; will retry", zap.Error(err))
			time.Sleep(500 * time.Millisecond)
			continue
		}
		l.bookDirty = false
		l.logger.Info("book resynced from postgres after prior batch failure")
	}
	if err := l.ReclaimPending(ctx); err != nil {
		l.logger.Error("failed to reclaim pending entries during retry", zap.Error(err))
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

// applyBatch runs the whole micro-batch inside one Postgres transaction,
// per spec §6's throughput lever: every wallet touched during the batch
// (whether as the event's own user, a trade buyer, or a trade seller) is
// SELECT FOR UPDATE'd only once (walletCache) and flushed with a single
// UPDATE per wallet at the end.
func (l *Loop) applyBatch(ctx context.Context, msgs []redis.XMessage) (err error) {
	// True parent of each per-entry span below; reconnection to each
	// message's own originating trace uses a Link instead (see applyEntry).
	ctx, span := tracer.Start(ctx, "matcher.apply_batch", trace.WithAttributes(
		attribute.Int("matcher.batch_size", len(msgs)),
		attribute.String("matcher.stream_name", l.streamName),
	))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	tx, err := l.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	walletCache := map[uuid.UUID]*wallet.Wallet{}

	loadWallet := func(userID uuid.UUID) (*wallet.Wallet, error) {
		if w, ok := walletCache[userID]; ok {
			return w, nil
		}
		w, err := l.wallets.GetForUpdate(ctx, tx, userID)
		if err != nil {
			return nil, err
		}
		walletCache[userID] = w
		return w, nil
	}

	for _, m := range msgs {
		firstTime, err := l.processedEvents.TryMark(ctx, tx, m.ID, l.streamName)
		if err != nil {
			return err
		}
		if !firstTime {
			l.logger.Info("duplicate stream entry skipped (idempotency guard)", zap.String("entry_id", m.ID))
			continue
		}

		raw, _ := m.Values["payload"].(string)
		var event orders.StreamEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			l.logger.Error("malformed stream payload, skipping", zap.String("entry_id", m.ID), zap.Error(err))
			continue
		}

		if entryErr := l.applyEntry(ctx, tx, m.ID, event, loadWallet); entryErr != nil {
			return entryErr
		}
	}

	for _, w := range walletCache {
		if err := l.wallets.Update(ctx, tx, w); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// applyEntry processes one stream entry within the batch's transaction,
// under its own child span of the batch span (matcher.apply_batch),
// additionally linked back to the trace of the request that placed/
// cancelled this specific order — see StreamEvent's doc comment for why a
// link, not a parent, is used here.
func (l *Loop) applyEntry(ctx context.Context, tx *sqlx.Tx, entryID string, event orders.StreamEvent, loadWallet func(uuid.UUID) (*wallet.Wallet, error)) (err error) {
	var links []trace.Link
	if sc := trace.SpanContextFromContext(orders.ExtractTraceContext(ctx, event)); sc.IsValid() {
		links = append(links, trace.Link{SpanContext: sc})
	}
	ctx, span := tracer.Start(ctx, "matcher.process_order_event",
		trace.WithLinks(links...),
		trace.WithAttributes(
			attribute.String("order_id", event.OrderID.String()),
			attribute.String("event_type", string(event.Type)),
		),
	)
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	switch event.Type {
	case orders.EventTypeNewOrder:
		return l.applyNewOrder(ctx, tx, event, loadWallet)
	case orders.EventTypeCancelOrder:
		return l.applyCancel(ctx, tx, event, loadWallet)
	default:
		l.logger.Error("unknown event type, skipping", zap.String("type", string(event.Type)), zap.String("entry_id", entryID))
		return nil
	}
}

func (l *Loop) applyNewOrder(ctx context.Context, tx *sqlx.Tx, event orders.StreamEvent, loadWallet func(uuid.UUID) (*wallet.Wallet, error)) error {
	now := time.Now().UTC()
	o := &order.Order{
		ID:         event.OrderID,
		UserID:     event.UserID,
		Side:       event.Side,
		Type:       event.OrderType,
		PriceCents: event.PriceCents,
		Quantity:   event.Quantity,
		Status:     order.StatusOpen,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	var availableFunds *int64
	if o.Side == order.SideBuy && o.Type == order.TypeMarket {
		buyerWallet, err := loadWallet(o.UserID)
		if err != nil {
			return err
		}
		avail := buyerWallet.AvailableBRLCents()
		availableFunds = &avail
	}

	// matching.Match is a pure, zero-I/O function by design (no
	// context.Context parameter) — tracing wraps the call site here rather
	// than threading ctx into the domain engine, so the "Pure Engine"
	// invariant stays intact.
	_, matchSpan := tracer.Start(ctx, "matcher.match_engine")
	result := matching.Match(l.book, o, availableFunds)
	matchSpan.SetAttributes(
		attribute.Int("matcher.trades_produced", len(result.Trades)),
		attribute.Bool("matcher.rested_on_book", result.RestedOnBook),
	)
	matchSpan.End()

	l.logger.Info("event processed by matching engine",
		zap.String("order_id", o.ID.String()), zap.Int("trades_produced", len(result.Trades)),
		zap.Bool("rested", result.RestedOnBook), zap.Int64("cancelled_quantity", result.CancelledQuantity))

	for _, tr := range result.Trades {
		buyerWallet, err := loadWallet(tr.BuyerUserID)
		if err != nil {
			return err
		}
		sellerWallet, err := loadWallet(tr.SellerUserID)
		if err != nil {
			return err
		}

		if o.Side == order.SideBuy && o.Type == order.TypeMarket {
			if err := buyerWallet.SettleBuyMarketFill(tr.PriceCents, tr.Quantity); err != nil {
				return err
			}
		} else {
			if err := buyerWallet.SettleBuyLimitFill(tr.PriceCents, tr.Quantity); err != nil {
				return err
			}
			// Price improvement: when the INCOMING order itself is the BUY
			// LIMIT side of this trade, its reservation (made at placement
			// time by PlaceOrderService) was calculated at the order's own
			// limit price, but the trade executed at the resting maker's
			// (better/lower) price. SettleBuyLimitFill only releases the
			// reservation for the actual matched cost; the difference must
			// be released back to available balance separately, or it
			// stays stuck in ReservedBRLCents forever once the order
			// reaches FILLED (a resting BUY LIMIT maker never has this
			// issue, since a maker always trades at exactly its own
			// posted price by construction).
			if o.Side == order.SideBuy && o.Type == order.TypeLimit {
				improvement := (*o.PriceCents - tr.PriceCents) * tr.Quantity
				if improvement > 0 {
					if err := buyerWallet.ReleaseBRLReservation(improvement); err != nil {
						return err
					}
				}
			}
		}
		if err := sellerWallet.SettleSellFill(tr.PriceCents, tr.Quantity); err != nil {
			return err
		}
		l.logger.Info("wallet liquidated for trade",
			zap.String("buyer_user_id", tr.BuyerUserID.String()), zap.String("seller_user_id", tr.SellerUserID.String()),
			zap.Int64("price_cents", tr.PriceCents), zap.Int64("quantity", tr.Quantity))

		t, err := trade.New(uuid.New(), tr.BuyOrderID, tr.SellOrderID, tr.BuyerUserID, tr.SellerUserID, tr.PriceCents, tr.Quantity, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := l.tradeRepo.Insert(ctx, tx, t); err != nil {
			return err
		}
		l.logger.Info("trade executed",
			zap.String("trade_id", t.ID.String()), zap.String("buy_order_id", tr.BuyOrderID.String()),
			zap.String("sell_order_id", tr.SellOrderID.String()), zap.Int64("price_cents", tr.PriceCents), zap.Int64("quantity", tr.Quantity))
	}

	for _, maker := range result.TouchedMakers {
		if err := l.orderRepo.Update(ctx, tx, maker); err != nil {
			return err
		}
	}

	if result.CancelledQuantity > 0 && o.Side == order.SideSell {
		sellerWallet, err := loadWallet(o.UserID)
		if err != nil {
			return err
		}
		if err := sellerWallet.ReleaseVibraniumReservation(result.CancelledQuantity); err != nil {
			return err
		}
	}
	// BUY MARKET's cancelled remainder needs no release: nothing was ever
	// reserved for it (design decision #1).

	// NOTE: this is Update, not Insert — PlaceOrderService already inserted
	// this exact order row (same primary key o.ID) synchronously when the
	// order was placed, before this event was ever queued. The matcher's
	// job is only to update it to reflect what Match() just computed.
	return l.orderRepo.Update(ctx, tx, o)
}

func (l *Loop) applyCancel(ctx context.Context, tx *sqlx.Tx, event orders.StreamEvent, loadWallet func(uuid.UUID) (*wallet.Wallet, error)) error {
	removed, found := l.book.Cancel(event.OrderID)
	if !found {
		// Already fully matched away, or was a MARKET order that never
		// rested: nothing to release, nothing to update on the book side.
		// The order's terminal status in Postgres (FILLED) is untouched;
		// CancelOrderService already handled the "already terminal" case
		// at the API layer before this event was ever queued.
		l.logger.Info("cancellation processed: order no longer resting (no-op)", zap.String("order_id", event.OrderID.String()))
		return nil
	}

	remaining := removed.Remaining()
	removed.Cancel()

	if removed.Side == order.SideBuy {
		buyerWallet, err := loadWallet(removed.UserID)
		if err != nil {
			return err
		}
		cost := *removed.PriceCents * remaining
		if err := buyerWallet.ReleaseBRLReservation(cost); err != nil {
			return err
		}
	} else {
		sellerWallet, err := loadWallet(removed.UserID)
		if err != nil {
			return err
		}
		if err := sellerWallet.ReleaseVibraniumReservation(remaining); err != nil {
			return err
		}
	}

	l.logger.Info("cancellation processed", zap.String("order_id", removed.ID.String()), zap.Int64("released_quantity", remaining))
	return l.orderRepo.Update(ctx, tx, removed)
}
