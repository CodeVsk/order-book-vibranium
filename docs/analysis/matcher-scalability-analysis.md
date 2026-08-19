# Critical Analysis — How to Scale the `matcher`

> Architect's analysis of the `matcher`'s scaling vectors as the
> customer/request base grows and the platform matures. Context:
> [`platform-summary.md`](platform-summary.md). See also
> [`outbox-publisher-analysis.md`](outbox-publisher-analysis.md) for the
> system's other singleton.

## TL;DR

Today's bottleneck **is not the matching engine** (`matching.Match()` — pure,
in-memory, microseconds) — it's the per-batch Postgres transaction in
`applyBatch`. Two solutions for **horizontal scaling** (adding instances) and
two for **vertical scaling** (making one instance handle more), in this order
of priority:

| Axis | Solution | Summary |
|---|---|---|
| Horizontal | 1. Sharding by symbol | One `matcher` (own Book + own goroutine) per symbol — scales with the catalog |
| Horizontal | 2. Postgres scale-out | Read replica now, partitioning by symbol group once the shared Postgres becomes the ceiling |
| Vertical | 1. Aggressive, adaptive batching | Fewer round-trips and fewer fsyncs per processed event — the biggest single lever |
| Vertical | 2. Matching/persistence pipeline | Overlap CPU (matching) and I/O (commit) within the same instance — advanced, last resort |

---

## Where today's ceiling is

The `matcher` is deliberately single-writer: `internal/domain/matching/book.go`
is not thread-safe by design (comment at `book.go:64-68`), and correctness
depends on exactly one goroutine, in a single instance, processing
`orders:incoming` via a single consumer name (`"matcher-1"`, hardcoded in
`cmd/matcher/main.go:57`) within a single consumer group.

Per batch, that goroutine (`consumer_loop.go:209-268`) does, in sequence:
`ReadBatch` → for each event, in-memory `Match()` (direct mutation of the
`Book`, **outside** the transaction) → `SELECT FOR UPDATE` per touched wallet
+ `UPDATE` per order + `INSERT` per trade → `COMMIT` (WAL fsync) → `XAck`.

**Steps 1, 4 and 5 are blocking, sequential I/O on the same goroutine.**
While batch N's commit waits on the fsync, nobody is matching batch N+1 —
even though `Match()` itself is trivially fast. It's Postgres, not the
matching algorithm, that limits the throughput of **a single symbol**.

**Before investing in any of the four solutions below**, it's worth
confirming this hypothesis with `pprof` on the `matcher` under `make
k6-full` (time spent in `applyBatch` vs. `Match()` vs. deserialization) and
correlating it with the consumer group lag that `scripts/validate-post-test.sh`
already measures — the solutions have quite different complexity costs, and
it's worth spending structural effort only after confirming where the time
actually goes.

---

## Horizontal scaling

### Solution 1 — Sharding by symbol (recommended, low risk)

The foundation for this **already exists in the schema**: `outbox_events.stream_name`
is a per-event column, and `OutboxRepository.Insert` already receives the
destination stream name as a parameter (`outbox_repo.go:29-33`). What's
missing is only the application layer: today `PlaceOrderService`/
`CancelOrderService` receive a **fixed** `streamName` at construction time
(`cfg.OrdersStreamName`, injected in `cmd/api/main.go:46-47`) — the same
value for every order, because there's only one symbol (Vibranium) so far.

```
orders:incoming:VBM   → matcher-VBM (own Book, own goroutine, shared Postgres)
orders:incoming:XYZ   → matcher-XYZ (own Book, own goroutine, shared Postgres)
```

- `PlaceOrderService.streamName` becomes resolved from `order.Symbol`
  (`"orders:incoming:" + symbol`) instead of a single value injected into
  the constructor — pure routing, no extra state, since the symbol is
  already present in the order payload.
- Each `matcher` instance comes up with `ORDERS_STREAM_NAME` pointing to its
  symbol's stream — consumer group + isolated `Book` already support this
  without changing any internal logic, just configuration.
- The consumer name (`"matcher-1"`, hardcoded today) needs to become
  parameterizable per symbol (`"matcher-VBM-1"`) so it doesn't collide
  across processes and keeps per-instance PEL auditing clear.

Scales **linearly with the number of active symbols** — solves growth in
catalog size, the most likely path for the platform to mature. Favorable
side effect: with N matchers concurrently writing to the same Postgres, the
database itself can group fsyncs from concurrent transactions (`group
commit`) — part of the gain from Vertical Solution 2 comes for free once
N > 1.

It doesn't, by itself, solve a single symbol whose volume saturates one
goroutine — for that, see the vertical solutions.

### Solution 2 — Postgres scale-out

Complementary to Solution 1: as the number of symbols and query volume grow,
the shared Postgres (not a single symbol) can become the new ceiling. Two
steps, in order of maturity:

1. **Read replica now** — `GET /trades`, `/orders/{id}`, `/wallets/{id}`
   compete for connections/I/O on the same primary where the `matcher` does
   `SELECT FOR UPDATE` and `COMMIT`. Moving these routes to a replica frees
   write capacity for the matcher without touching any invariant —
   eventually consistent reads are acceptable for them. Low risk, low
   effort.
2. **Partition Postgres by symbol group** (separate schemas or instances) —
   only once the shared primary, with all matchers for all symbols writing
   to it, proves saturated even after Solution 1. Much higher operational
   cost: cross-symbol routes like unfiltered `GET /trades` now require
   fan-out/aggregation across databases, and schema migrations need to run
   in N places. Only worth considering once symbol sharding (Solution 1) is
   saturated in practice — volume beyond what this system serves today.

---

## Vertical scaling

### Solution 1 — Aggressive, adaptive batching (biggest single lever)

Without changing the architecture, it's possible to drastically reduce the
I/O cost per processed event:

| Adjustment | Gain | Cost |
|---|---|---|
| **Multi-row INSERT/UPDATE** — replace N individual `UPDATE`/`INSERT` statements in `applyNewOrder`/`applyCancel` with `INSERT ... VALUES (...),(...)` and `UPDATE ... FROM (VALUES ...)` | High — fewer round-trips per batch, same fsync ceiling | Low — doesn't change transactional semantics |
| **Increase `MATCHER_BATCH_SIZE`** (currently 200) | Amortizes the fsync over more events = higher aggregate throughput | Medium — more confirmation latency per order and a larger PEL if the batch fails |
| **Adaptive batch size** — grow the batch size when consumer group lag increases, return to normal once the backlog clears | Low latency in steady state, high throughput under pressure | Medium — requires a reliable backlog signal and anti-oscillation logic |

This is the solution with the best return on effort: it directly attacks the
root cause (few fsyncs, more events per fsync) without introducing any new
failure mode.

### Solution 2 — Matching/persistence pipeline (advanced, last resort)

Only if profiling shows Vertical Solution 1 isn't enough: split the same
instance into two goroutines — one does `Match()` (CPU, in-memory), the
other persists to Postgres (I/O) — connected by a channel, so that matching
for batch N+1 is already running while batch N's commit is still waiting on
the fsync.

**Why it's the last resort, not the default:** `Match()` already mutates the
`Book` synchronously and outside the transaction — that's how `resyncBook`
is able today to fix the `Book` by reloading it from Postgres after a commit
failure (`consumer_loop.go:126-141`). With two goroutines in flight, a
commit failure for batch N leaves the `Book` with batch N+1's mutations
already applied in memory — undoing that requires a `Book`
checkpoint/rollback that doesn't exist today, considerably more complex than
the current "reload everything" approach. The gain is also limited: if
matching takes microseconds and fsync takes milliseconds, the overlap hides
only a fraction of the total time — and Vertical Solution 1 already attacks
the same root cause with much less risk.

If implemented, limit the pipeline depth to 1 batch in flight (never an
unbounded queue), to keep the blast radius of a commit failure restricted to
a single known batch, as it is today.

---

## What to avoid

- **Sub-partitioning a single symbol's book by price range** — breaks
  price-time priority (a match can cross bid/ask at any range; coordinating
  partitions reintroduces the concurrency that single-writer exists to
  avoid). Real exchanges solve this with one matching process per
  instrument, never by sub-partitioning a single instrument.
- **`synchronous_commit = off`** to "speed up" the commit — trades
  durability for latency in a system that moves money; only acceptable with
  a hardware guarantee and an explicit business decision, not as a silent
  optimization.
- **Decoupling trade/wallet writes from the order commit** — breaks the
  invariant that settlement is atomic with the trade write and order update;
  reintroduces the same dual-write hazard addressed in
  [`outbox-publisher-analysis.md`](outbox-publisher-analysis.md).
- **Pipeline with an unbounded queue** (Vertical Solution 2 without a depth
  limit) — turns a commit failure into a state reconciliation problem far
  more complex than the current `resyncBook`.

## Note — backpressure

The `matcher` has finite throughput (governed by Postgres); the API is "many
replicas, no limit." Without rate limiting/backpressure between the two,
sustained high volume becomes a self-inflicted DoS vector (unbounded queue
growth in `outbox_events`/PEL) before it becomes a matching problem per se.
It's worth addressing per-user/IP rate limiting and a monitored queue depth
ceiling alongside any of the solutions above, not as a separate item.

## Suggested roadmap

| Timeframe | Action |
|---|---|
| Short | Profiling (`pprof` + consumer group lag) to confirm the bottleneck |
| Short | Vertical 1 — multi-row INSERT/UPDATE in `applyBatch` |
| Short | Horizontal 2 (step 1) — read replica for GETs |
| Medium | Horizontal 1 — sharding by symbol (stream + matcher + Book per symbol) |
| Medium | Vertical 1 — adaptive batch size |
| Long | Vertical 2 — matching/persistence pipeline, only if profiling demands it |
| Very long | Horizontal 2 (step 2) — partition Postgres by symbol group |
