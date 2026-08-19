# Principles

*Why* the domain code (`internal/domain/*`) is shaped the way it is, with
verbatim source-comment citations. For the operational consequences of
these invariants (what breaks if violated, where they're enforced), see
[ARCHITECTURE.md § Key invariants](./ARCHITECTURE.md#key-invariants).

## Source of truth

Several comments in the domain code reference "design decision #1/#2" —
e.g. `internal/domain/wallet/wallet.go:102-103` ("design decision #1") and
`internal/domain/matching/engine.go:36` ("design decision #2"). Those two
decisions are unpacked below, in their own sections, with the domain code's
own citations.

## Order book invariants

**Single-writer.** `internal/domain/matching/book.go:64-68`:

> "Book is the single in-memory order book for one symbol (Vibranium). It is
> NOT safe for concurrent use — the matcher consumer guarantees a single
> writer goroutine, per design (this is the invariant that resolves the
> challenge's core contradiction between "the book admits no concurrency"
> and "thousands of orders may arrive in the same millisecond")."

**FIFO price-time priority.** `internal/domain/matching/book.go:12-13`:

> "level holds every resting order at one price point, in FIFO (price-time
> priority) order."

**Level ordering.** `internal/domain/matching/book.go:20-22`:

> "levelHeap is a container/heap of price levels. less decides ordering:
> bids use price descending (best bid = highest price), asks use price
> ascending (best ask = lowest price)."

## Matching engine purity

**Zero I/O.** `internal/domain/matching/engine.go:20-23`:

> "MatchResult is the pure output of processing one incoming order. No I/O
> happens here; the caller (internal/application/matcherapp) persists
> Trades/Incoming/TouchedMakers and settles wallets inside a single
> Postgres transaction."

**BUY MARKET funds-check contract — "design decision #2".**
`internal/domain/matching/engine.go:32-38`:

> "Match runs price-time priority matching for an incoming order against the
> book. For BUY MARKET orders, availableBuyerFundsCents must be a non-nil
> pointer to the buyer's currently available BRL balance (balance minus
> reserved), read once under SELECT ... FOR UPDATE by the caller before
> calling Match — see design decision #2 at the top of the plan for why a
> single upfront read is sufficient."

In practice, `Match()` decrements the pointer's value in place as it
simulates fills (`internal/domain/matching/engine.go:88-90`), so a BUY
MARKET order can never be matched past the funds the caller confirmed were
available at the start of the batch.

## Order lifecycle

**Shared representation.** `internal/domain/order/order.go:38-39`:

> "Order is the domain representation of a buy/sell order, shared by the
> matching engine's in-memory book and the persistence layer."

**Idempotent cancel.** `internal/domain/order/order.go:81-82`:

> "Cancel marks the order CANCELLED. It is a no-op (idempotent) if the order
> is already FILLED or CANCELLED."

## Wallet & settlement

**Pure representation, integer money.** `internal/domain/wallet/wallet.go:16-18`:

> "Wallet is the pure domain representation of one user's balances. All
> money is integer BRL cents; Vibranium is integer units. Persistence
> (infra/postgres) maps this 1:1 to the wallets table."

**BUY MARKET never reserves upfront — "design decision #1".**
`internal/domain/wallet/wallet.go:101-105`:

> "SettleBuyMarketFill applies a trade execution to a buyer that had NO
> reservation up front (BUY MARKET never reserves — see design decision #1
> at the top of this plan). BRL is debited directly from available balance,
> which the matching engine already guaranteed was sufficient before this
> trade was produced."

This is the counterpart to design decision #2 above: because a BUY MARKET
order never reserves funds when placed, the matching engine itself must be
the thing that guarantees affordability at match time — hence the
`availableBuyerFundsCents` contract on `Match()`.

By contrast, BUY LIMIT and both SELL order types *do* reserve upfront
(`ReserveBRL`/`ReserveVibranium`, called by the placement path in
`internal/application/orders`), which is why `SettleBuyLimitFill` and
`SettleSellFill` consume a pre-existing reservation instead of debiting
available balance directly.

## Trade immutability

`internal/domain/trade/trade.go:12-13`:

> "Trade is an immutable record of one execution between a buy and a sell
> order. Price is always the resting ("maker") order's price."

## Glossary

| Term | Meaning |
|---|---|
| LIMIT | Order with a specified price; rests on the book if not immediately (fully) matched. |
| MARKET | Order with no price; matches against the best available price(s) immediately, cancelling any unfillable remainder instead of resting. |
| Maker | The resting order in a trade — its price is what the trade executes at. |
| Taker | The incoming order in a trade. |
| Reserve / Release | Moving funds/Vibranium between `balance` and `reserved` on order placement/cancellation, before a trade executes. |
| Settle | Applying the effect of an executed trade to a wallet's `balance`/`reserved`. |
| PEL | Redis Streams "Pending Entries List" — entries a consumer read but hasn't yet ACKed; drained by `ReclaimPending` on matcher restart. |
| Outbox | The `outbox_events` table + publisher pattern used to atomically couple a Postgres write with a guaranteed-eventual Redis publish. |
| Idempotency key | Here, the Redis Stream entry ID, deduplicated via `processed_stream_events`. |

## Cross-links

- [TESTS.md § Concurrency test](./TESTS.md#concurrency-test--testconcurrencyconcurrency_testgo) —
  the test that exercises the single-writer/conservation invariants under
  real contention.
- [ARCHITECTURE.md § Key invariants](./ARCHITECTURE.md#key-invariants) —
  the same invariants from an operational/architecture lens.
