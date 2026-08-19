# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this service is

**Vibranium Order Book** — a market-exchange system for buying and selling a fictitious asset ("Vibranium") denominated in BRL. Implements LIMIT and MARKET orders with full price-time priority matching. Three independent Go binaries share a Postgres database and a Redis Stream.

## Commands

```bash
make build               # go build ./cmd/...
make test                # unit tests only, no Docker (-short -count=1)
make test-integration    # unit + integration + idempotency (requires Docker)
make test-concurrency    # 200-goroutine race-detector test (requires Docker)
make lint                # golangci-lint run
make run                 # docker compose up --build (full local stack)
make migrate-up          # apply all pending migrations
make migrate-down        # roll back one migration
make k6                  # load test (300 req/s local profile)
make k6-full             # load test (full profile)
make validate            # post-k6 Postgres + Redis consistency checks
```

**Running a single test:**

```bash
# Unit (no Docker):
go test -run TestEngine_BuyLimit_MatchesSellResting ./internal/domain/matching/...

# Integration (Docker required):
go test -run TestRoundTrip_PlaceOrder_ThroughMatch_ToWalletSettlement \
  -tags=integration ./test/integration/...

# Idempotency:
go test -run TestApplyBatch_ReprocessingSameEntryID_HasSingleEffect \
  -tags=integration ./internal/application/matcherapp/...
```

## Architecture

### Three binaries

| Binary | Role | Replicas |
|---|---|---|
| `cmd/api` | Stateless HTTP API | Many |
| `cmd/outbox-publisher` | Postgres → Redis relay | **Singleton** |
| `cmd/matcher` | In-memory matching engine | **Singleton** |

**Request flow:**
1. `POST /orders` → reserves wallet funds in Postgres, writes an `outbox_events` row in the **same transaction**, returns `202 Accepted`.
2. `outbox-publisher` polls `outbox_events`, XADDs each event to the `orders:incoming` Redis Stream, marks row `published=true` — all in one transaction per event.
3. `matcher` reads micro-batches from the stream via Consumer Group, runs matching in-memory, then settles all wallet changes and trade/order writes in **one Postgres transaction per batch**, ACKs only after commit.

### Key invariants

- **Single-Writer Book** — `internal/domain/matching/book.go` is not thread-safe by design. Correctness relies on exactly one goroutine in one consumer instance processing stream entries.
- **Pure Engine** — `matching.Match()` has zero I/O; it returns a `MatchResult`. All Postgres writes are owned by `applyBatch` in `matcherapp`.
- **Idempotency** — `processed_stream_events` table uses `INSERT ON CONFLICT DO NOTHING`. Entries already applied survive crash-between-commit-and-ACK safely.
- **Crash Recovery** — on restart, `RecoverBook` rebuilds the in-memory book from all `OPEN`/`PARTIALLY_FILLED` LIMIT orders in Postgres (oldest first), then `ReclaimPending` drains the PEL before entering the main poll loop.
- **Wallet locking** — `applyBatch` does `SELECT FOR UPDATE` at most once per wallet per batch via a `map[uuid.UUID]*wallet.Wallet` cache; all updates flush at transaction end.

### Package layout

```
internal/
  domain/
    order/        Order aggregate (Fill, Cancel, IsDone)
    trade/        Trade value object
    wallet/       Wallet aggregate (Reserve, Release, Settle)
    matching/     book.go (levelHeap, FIFO per price level), engine.go (Match)
  application/
    orders/       PlaceOrderService, CancelOrderService, query read models
    matcherapp/   consumer_loop.go (Loop.Run, applyBatch), book_recovery.go
    outboxapp/    publisher_loop.go (Publisher.Run, PublishOnce)
  infra/
    httpapi/      chi router, handlers, dto.go (validated request/response structs), middleware
    postgres/     repos: order, wallet, trade, outbox, processed_events
    redisstream/  consumer.go (ReadBatch, ReadPending, Ack), producer.go (XAdd)
  platform/
    config/       Env-var loader
    db/           sqlx.Connect wrapper
    logger/       zap production logger (JSON; no fmt.Printf anywhere else)
    redisclient/  go-redis client constructor
```

### Database

Six tables (see `migrations/`): `users`, `wallets`, `orders`, `trades`, `processed_stream_events`, `outbox_events`. `wallets.user_id` has a `FK -> users(id)` (added in `000004_users.up.sql`), so `users` is the root identity anchor the rest of the FK chain (`orders.user_id`, `trades.buyer_user_id`/`seller_user_id`) ultimately depends on.

Seed data: `000002_seed_wallets.up.sql` seeds 5 wallets and `000004_users.up.sql` seeds the matching `users` rows — `alice`–`eve` (UUIDs `…0001`–`…0005`), each with R$ 10,000,000.00 and 10,000,000 Vibranium units. Use these IDs in manual testing, or `GET /users` to list them.

`trades` uses keyset pagination (`executed_at DESC, id DESC`) — no OFFSET.

### Configuration

All configuration is environment variables. See `.env.example` for the full list. Required: `DATABASE_URL`, `REDIS_URL`. Notable tunables: `MATCHER_BATCH_SIZE` (default 200), `MATCHER_BATCH_TIMEOUT_MS` (default 50), `OUTBOX_BATCH_SIZE` (default 200), `OUTBOX_POLL_INTERVAL_MS` (default 100).

## Tech stack

Go 1.25 · chi v5 · PostgreSQL 16 (pgx/v5 + sqlx) · Redis 7 Streams (go-redis/v9) · zap · testcontainers-go · golang-migrate · k6

## HTTP API

| Method | Path | Notes |
|---|---|---|
| `POST` | `/orders` | Place order; returns `202 Accepted` |
| `DELETE` | `/orders/{id}` | Cancel open order |
| `GET` | `/orders/{id}` | Get order by ID |
| `GET` | `/wallets/{user_id}` | Get wallet balance |
| `GET` | `/trades` | List trades (keyset pagination) |
| `GET` | `/users` | List seeded users |
| `GET` | `/users/{id}` | Get user by ID |

`X-Request-Id` header is propagated as `request_id` in all log lines (set by `CorrelationID` middleware).

## Detailed docs

For deeper reference beyond this file's quick orientation, see `docs/codebase/`:

- `ARCHITECTURE.md` — deployment topology, invariants with citations, DB/config detail
- `STACK.md` — pinned dependency versions, k6 profiles, container tooling
- `TESTS.md` — full test-suite inventory, harness internals
- `PRINCIPLES.md` — domain invariants with source-comment citations and rationale
- `OBSERVABILITY.md` — OpenTelemetry/Jaeger tracing wiring and security caveats
