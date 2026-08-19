# Vibranium Order Book

Vibranium isn't a real asset; it's a made-up commodity, priced in Brazilian reais, that this
service lets you buy and sell against other traders. What *is* real is everything underneath
it: a proper limit order book with price-time priority, LIMIT and MARKET orders, and a set of
concurrency and consistency guarantees you'd actually want from an exchange, not a toy CRUD
app that happens to have "orders" in the name.

That's really the point of this project. It's built around the hard problems an exchange
runs into: how do you accept order placements from many API replicas at once without losing
one, how do you guarantee a single order book is only ever mutated by one writer at a time,
how do you make sure a crash mid-settlement doesn't double-charge anyone, and how do you keep
all of that observable when a trade is the result of five different components talking to
each other. If you're here to see transactional outboxes, Redis Streams consumer groups, and
single-writer in-memory data structures used for real instead of just described in a blog
post, you're in the right place.

## Architecture

The system is three independent Go binaries that never talk to each other directly: they
only communicate through Postgres and a Redis Stream. That separation is deliberate: it means
each piece can fail, restart, or scale independently without the others needing to know.

- **`cmd/api`**: the stateless HTTP layer, and the only one of the three you'd ever run more
  than one copy of. When a request comes in to place an order, it reserves the buyer's BRL or
  the seller's Vibranium in Postgres and writes an `outbox_events` row **in the same
  transaction**, then immediately returns `202 Accepted`. It never touches the order book or
  Redis directly, and that's the whole trick behind the outbox pattern: the write that matters
  (the reservation) and the write that says "tell everyone about this" happen atomically, so
  there's no window where one succeeds and the other doesn't.
- **`cmd/outbox-publisher`**: a singleton that polls `outbox_events` for unpublished rows,
  `XADD`s each one to the `orders:incoming` Redis Stream, and marks it published, all in one
  transaction per event. This is the relay between "durably recorded intent" and "the matcher
  will see it."
- **`cmd/matcher`**: also a singleton, and the actual order book. A single goroutine owns an
  in-memory book, reads micro-batches off `orders:incoming` through a Redis consumer group,
  runs matching entirely in memory (zero I/O, see `matching.Match()`), and then writes every
  resulting trade, order update, and wallet change for that whole batch in **one Postgres
  transaction**, only acknowledging the stream entries after that commit succeeds.

Two invariants fall out of that design and are worth knowing before you go digging through the
code:

- **The book is a single-writer data structure, on purpose.** `internal/domain/matching/book.go`
  is not thread-safe, and it doesn't need to be: correctness relies entirely on there being
  exactly one goroutine, in one running `matcher` instance, ever touching it. Don't be tempted
  to "fix" the lack of locking; the lack of locking *is* the design.
- **Every stream entry is idempotent to apply twice.** The `processed_stream_events` table uses
  `INSERT ... ON CONFLICT DO NOTHING`, so if the matcher crashes after committing a batch but
  before acking it in Redis, the batch gets redelivered, reprocessed, and silently no-ops the
  second time instead of double-settling anyone.

In the local Docker stack, an `nginx` container sits in front of the two `api` replicas and is
the thing actually listening on `localhost:8080`. It re-resolves the `api` hostname against
Docker's embedded DNS every 10 seconds and round-robins across whichever replicas are up,
enough to prove requests really are being load-balanced, not a production-grade LB.

## Tech stack

| Component | Choice | Why |
|---|---|---|
| Language | Go 1.25 | |
| HTTP routing | [chi](https://github.com/go-chi/chi) v5.1.0 | small, idiomatic `net/http`-compatible router |
| Database | PostgreSQL 16 via [pgx/v5](https://github.com/jackc/pgx) + [sqlx](https://github.com/jmoiron/sqlx) | pgx for driver performance, sqlx for ergonomic struct scanning |
| Messaging | Redis 7 Streams via [go-redis/v9](https://github.com/redis/go-redis) | consumer groups give at-least-once delivery plus a pending-entries list for crash recovery, without needing a heavier broker |
| Logging | [zap](https://github.com/uber-go/zap) | structured JSON logs everywhere; there's no `fmt.Printf` anywhere else in the codebase |
| Tracing | OpenTelemetry (`otelchi`, `otelsql`) exporting OTLP/HTTP to Jaeger | correlates a request end-to-end across all three binaries |
| Migrations | [golang-migrate](https://github.com/golang-migrate/migrate) v4.17.1 | |
| Testing | [testcontainers-go](https://github.com/testcontainers/testcontainers-go) | spins up real Postgres/Redis for integration tests instead of mocking them away |
| Load testing | [k6](https://k6.io/) | |

## Getting started

You only need Docker and `make` to run the full stack. Go itself is only needed if you want
to run tests or work on the code outside a container.

**1. Configure your environment**

```bash
cp .env.example .env
```

The defaults in `.env.example` already match the docker-compose service names, so you don't
need to change anything to run locally.

**2. Bring the stack up**

```bash
make run
```

This runs `docker compose -f build/docker-compose.yml up --build`, which brings up, in order:

- `postgres` (16-alpine) and `redis` (7-alpine), with healthchecks gating everything downstream;
- `jaeger` for local tracing;
- a one-shot `migrate` container that applies all four migrations and exits;
- two replicas of `api`, sitting behind `nginx` on `localhost:8080`;
- one `outbox-publisher` and one `matcher`.

**3. Confirm it's alive**

```bash
curl localhost:8080/ping
curl localhost:8080/users
```

The `GET /users` call should return five seeded users (more on them below).

**4. Place an order**

```bash
curl -X POST localhost:8080/orders \
  -d '{"user_id":"00000000-0000-0000-0000-000000000001","side":"SELL","type":"LIMIT","price_cents":1000,"quantity":5}'
```

That's alice offering to sell 5 units of Vibranium at R$ 10.00 each. You'll get back a `202`
with an `order_id` immediately: remember, the API only reserves funds and enqueues the event;
the actual matching happens asynchronously once the outbox-publisher and matcher pick it up.
Check the result with:

```bash
curl localhost:8080/wallets/00000000-0000-0000-0000-000000000001
curl "localhost:8080/orders/<order_id>"
```

**Seeded users.** Migrations `000002_seed_wallets` and `000004_users` seed five users, alice
through eve, each starting with R$ 100,000,000.00 and 10,000,000 Vibranium, so you have plenty
of room to experiment. `GET /users` is the live source of truth for their IDs; for
convenience, they run from `00000000-0000-0000-0000-000000000001` (alice) through `...0005`
(eve).

## Configuration

Every configuration knob is an environment variable, read in `internal/platform/config`. Only
the first two are required; everything else has a sane default for local development.

| Variable | Default | Notes |
|---|---|---|
| `DATABASE_URL` | *(required)* | Postgres connection string |
| `REDIS_URL` | *(required)* | Redis connection string |
| `APP_PORT` | `8080` | port the `api` binary listens on |
| `LOG_LEVEL` | `info` | zap log level |
| `ORDERS_STREAM_NAME` | `orders:incoming` | stream the matcher consumes |
| `TRADES_STREAM_NAME` | `trades:executed` | reserved for future consumers of executed trades |
| `CONSUMER_GROUP_NAME` | `matcher-group` | the matcher's Redis consumer group |
| `MATCHER_BATCH_SIZE` | `200` | max stream entries pulled into one matching + settlement batch |
| `MATCHER_BATCH_TIMEOUT_MS` | `50` | how long the matcher waits for a batch to fill before processing what it has |
| `OUTBOX_BATCH_SIZE` | `200` | max outbox rows published per poll |
| `OUTBOX_POLL_INTERVAL_MS` | `100` | how often the outbox-publisher polls for unpublished rows |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://localhost:4318` | where traces are shipped |
| `OTEL_TRACES_SAMPLER_ARG` | `1.0` | trace sampling ratio, 0.0 to 1.0 |

## HTTP API

A quick note before the endpoint-by-endpoint breakdown: **there is no authentication here.**
Every `user_id` in a request body or path is trusted as-is. That's a deliberate scope decision
for a project centered on the matching engine, not an oversight; it's the first thing to know
if you're thinking about pointing this at anything beyond a demo. A couple of other
cross-cutting details apply to every endpoint: request bodies are capped at 16KB, every
response gets an `X-Request-Id` header (echoing the one you sent, or a fresh UUID if you
didn't send one) that's also attached to the server-side logs for that request, and every
error comes back as `{"code": "...", "message": "..."}`.

### `POST /orders`

Place a new order. Funds or Vibranium are reserved synchronously before this returns;
matching itself happens asynchronously.

```json
{
  "user_id": "00000000-0000-0000-0000-000000000001",
  "side": "BUY",          // BUY or SELL
  "type": "LIMIT",        // LIMIT or MARKET
  "price_cents": 1000,    // required for LIMIT, omitted/ignored for MARKET
  "quantity": 5
}
```

`price_cents` and `quantity` are both bounded at `3,000,000,000` (chosen so their product
never overflows an int64). On success you get:

```
202 Accepted
{"order_id": "...", "status": "OPEN", "created_at": "..."}
```

Possible errors: `404 user_not_found`, `404 wallet_not_found`, `409 insufficient_balance` (not
enough available BRL for a BUY LIMIT, or not enough available Vibranium for a SELL), `400
invalid_payload` for anything that fails validation.

### `DELETE /orders/{id}`

Cancel an open order. Body: `{"user_id": "..."}`, which must match the order's owner.

Returns `202 Accepted` with `{"order_id": "...", "status": "CANCEL_QUEUED"}` normally (the
actual cancellation is applied asynchronously by the matcher, same as a fill would be), or
`200 OK` with the order's real terminal status if it was already filled or cancelled
(cancelling twice is a no-op, not an error). Errors: `404 order_not_found`, `403 forbidden` if
you're not the owner.

### `GET /orders/{id}`

Full order state: `order_id`, `status`, `filled_quantity`, `quantity`, `price_cents`, `side`,
`type`, `created_at`, `updated_at`. `404 order_not_found` if it doesn't exist.

### `GET /wallets/{user_id}`

```
200 OK
{
  "user_id": "...",
  "balance_brl_cents": 10000000000,
  "reserved_brl_cents": 500000,
  "available_brl_cents": 9999500000,
  "balance_vibranium": 10000000,
  "reserved_vibranium": 5,
  "available_vibranium": 9999995
}
```

`available_*` is just balance minus reserved, computed for you. `404 user_not_found` if the
user doesn't exist.

### `GET /trades`

Trade history with keyset pagination (no `OFFSET`), so it stays fast no matter how deep you
page. Optional query params: `user_id`, `order_id` (filter to trades touching that user/order),
`limit` (clamped between 1 and 200, defaults to 50), and `cursor` (the opaque value from a
previous response's `next_cursor`). Ordered by `executed_at DESC, id DESC`.

```
200 OK
{"trades": [{"trade_id": "...", "buy_order_id": "...", "sell_order_id": "...", "price_cents": 1000, "quantity": 5, "executed_at": "..."}], "next_cursor": "..."}
```

### `GET /users` / `GET /users/{id}`

List or fetch the seeded users (`{"id": "...", "username": "...", "created_at": "..."}` each).
Handy for grabbing valid `user_id`s without reading migration files.

## How matching actually works

An order is either `LIMIT` (has a price, can rest on the book if it doesn't fully fill) or
`MARKET` (no price, takes whatever liquidity is available right now and never rests: any
unfilled remainder is cancelled outright rather than left open). Orders move through
`OPEN` → `PARTIALLY_FILLED` → `FILLED`, or get `CANCELLED` at any point before they're done.

The book itself (`internal/domain/matching/book.go`) is two price heaps: bids ordered so the
highest price is always on top, asks ordered so the lowest price is always on top, with a FIFO
list of orders at each price level. That's price-time priority in a nutshell: best price wins,
and among orders at the same price, whoever got there first gets matched first.

The more interesting part is how wallets get touched, because it's not symmetric between buy
and sell:

- A **BUY LIMIT** reserves BRL upfront, at order-placement time, for `price × quantity`.
- A **SELL** (LIMIT or MARKET) reserves Vibranium upfront, since the quantity being sold is
  known regardless of price.
- A **BUY MARKET** reserves *nothing* upfront. Since a market buy doesn't know its price ahead
  of time, the matcher instead checks the buyer's live available balance as it walks the book,
  and will partially fill, or simply stop, a market buy the moment the buyer can no longer
  afford the next unit at the next price level. This is a deliberate choice, not a gap: it
  avoids over-reserving funds against a price that hasn't been decided yet.

Whenever a trade executes, its price is always the resting order's price: the maker sets the
price, the taker accepts it.

## Running the tests

```bash
make test               # unit tests only, no Docker required (-short -count=1)
make test-integration   # unit + integration + idempotency (needs Docker)
make test-concurrency    # 200-goroutine race-detector test (needs Docker)
```

To run one specific test instead of a whole suite:

```bash
# unit
go test -run TestEngine_BuyLimit_MatchesSellResting ./internal/domain/matching/...

# integration
go test -run TestRoundTrip_PlaceOrder_ThroughMatch_ToWalletSettlement \
  -tags=integration ./test/integration/...
```

## Observability

`make run` also brings up a local Jaeger instance at `http://localhost:16686`. All three
binaries export OpenTelemetry traces over OTLP/HTTP (configurable via
`OTEL_EXPORTER_OTLP_ENDPOINT`), tagged with service names `trade-market-api`,
`trade-market-outbox-publisher`, and `trade-market-matcher`; search by any of those in the
Jaeger UI to follow one request across the whole pipeline: HTTP handling → wallet reservation →
outbox insert → outbox publish → matcher batch → trade/wallet settlement.

One wrinkle worth knowing: because the matcher processes micro-batches that bundle together
many orders' worth of independent traces, its spans can't be a normal parent/child of any
single request's trace. Instead they use OTel span **links**: in Jaeger, follow the "Linked
Spans" reference from a `matcher.process_order_event` span back to the trace of the request
that placed the order. Every log line also carries `trace_id`/`span_id` alongside the
`request_id` whenever a trace is live, so logs and traces cross-reference directly.

## Load testing

```bash
make k6            # local profile: 300 req/s for 1 minute
make k6-moderate    # 2,500 req/s for 1 minute
make k6-full         # 5,000 req/s for 2 minutes
make validate        # post-run Postgres + Redis consistency checks
```

The script (`scripts/k6/load-test.js`) mixes 80% LIMIT / 20% MARKET orders across the five
seeded users, with a 5% chance per iteration of cancelling a previously placed order, and
enforces `p(99) < 500ms` with zero 5xx responses as pass/fail thresholds. See
`scripts/k6/README.md` for the full breakdown.

## Project layout

```
internal/
  domain/        pure business rules, no I/O: order.go (Fill/Cancel), wallet.go (Reserve/
                 Release/Settle), matching/ (the book + the Match() engine), trade.go
  application/   orchestration: matcherapp (the matcher's consume-match-settle loop),
                 outboxapp (the publisher's poll loop), orders (place/cancel services)
  infra/         adapters to the outside world: httpapi (chi router, handlers, DTOs,
                 middleware), postgres (repositories), redisstream (stream consumer/producer)
  platform/      cross-cutting: config loading, DB/Redis client construction, the zap logger,
                 OpenTelemetry setup
```

## Known limitations

These are documented scope decisions, not bugs:

- **No metrics.** Traces and structured logs are exported today; a Prometheus/OTel metrics
  pipeline is a natural next addition, just not built yet.
- **No UI, no authentication.** As covered above, `user_id` is trusted as-is on every request.
- **One symbol, one unsharded book.** The architecture would shard naturally by symbol if
  Vibranium ever had company, but the current book is a single in-memory structure owned by a
  single matcher instance.
