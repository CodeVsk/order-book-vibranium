# Architecture

## System overview

Vibranium Order Book is a market-exchange system for buying and selling a
fictitious asset ("Vibranium") denominated in BRL. It implements LIMIT and
MARKET orders with full price-time priority matching. Three independent Go
binaries share one Postgres database and one Redis Stream; each binary has a
distinct scaling profile (see table below), and correctness across the whole
system rests on the invariants in [PRINCIPLES.md](./PRINCIPLES.md).

## The three binaries

| Binary                 | Role                      | Replicas                              |
| ---------------------- | ------------------------- | ------------------------------------- |
| `cmd/api`              | Stateless HTTP API        | Many (2 in the default compose stack) |
| `cmd/outbox-publisher` | Postgres → Redis relay    | **Singleton**                         |
| `cmd/matcher`          | In-memory matching engine | **Singleton**                         |

**Request flow:**

1. `POST /orders` → reserves wallet funds in Postgres (`SELECT ... FOR
UPDATE`, skipped for BUY MARKET — see
   [PRINCIPLES.md § Wallet & settlement](./PRINCIPLES.md#wallet--settlement)),
   writes an `outbox_events` row in the **same transaction**, returns `202
Accepted`.
2. `outbox-publisher` polls `outbox_events`, `XADD`s each event to the
   `orders:incoming` Redis Stream, marks the row `published = true` — all in
   one transaction per event
   (`internal/application/outboxapp/publisher_loop.go`).
3. `matcher` reads micro-batches from the stream via a Redis consumer group,
   runs matching in-memory (`internal/domain/matching`), then settles all
   wallet changes and trade/order writes in **one Postgres transaction per
   batch**, and ACKs only after commit
   (`internal/application/matcherapp/consumer_loop.go`).

## Deployment topology

The local stack (`build/docker-compose.yml`) wires these services together:

| Service            | Image                                          | Notes                                                                                                                                                                 |
| ------------------ | ---------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `postgres`         | `postgres:16-alpine`                           | System of record. Healthcheck `pg_isready`.                                                                                                                           |
| `redis`            | `redis:7-alpine`                               | Stream transport for `orders:incoming`. Healthcheck `redis-cli ping`.                                                                                                 |
| `jaeger`           | `jaegertracing/all-in-one:1.60`                | **Local-dev-only** tracing backend — UI on `:16686`, OTLP/HTTP receiver on `:4318`. Not a production deployment artifact; see [OBSERVABILITY.md](./OBSERVABILITY.md). |
| `migrate`          | `migrate/migrate:v4.17.1`                      | Runs all `migrations/*.up.sql` to completion before any app service starts (`depends_on: service_completed_successfully`).                                            |
| `api`              | built from `build/Dockerfile.api`              | `deploy.replicas: 2`. Exposes `8080` internally only — not published to the host directly.                                                                            |
| `nginx`            | `nginx:1.27-alpine`                            | The actual host-facing entrypoint, `:8080` on the host → `:80` in the container. See below.                                                                           |
| `outbox-publisher` | built from `build/Dockerfile.outbox-publisher` | `deploy.replicas: 1` (singleton, see [PRINCIPLES.md](./PRINCIPLES.md)).                                                                                               |
| `matcher`          | built from `build/Dockerfile.matcher`          | `deploy.replicas: 1` (singleton).                                                                                                                                     |

**nginx as the load balancer** (`build/nginx/nginx.conf`):

- Uses Docker Compose's embedded DNS resolver (`resolver 127.0.0.11
valid=10s`) so `proxy_pass` re-resolves the `api` hostname on every
  request instead of caching one upstream IP — this is what makes
  round-robin across the 2 `api` replicas work, and lets `docker compose up
--scale api=N` change the replica count without restarting nginx.
- Only proxies known paths — `location ~ ^/(orders|wallets|trades|users)(/|$)`
  — everything else returns a plain `404` instead of being forwarded.
- Propagates the caller's `X-Request-Id` if present, otherwise generates one
  (`map $http_x_request_id $req_id`), so the API's `CorrelationID`
  middleware and trace correlation ([OBSERVABILITY.md](./OBSERVABILITY.md))
  keep working behind the proxy.
- Adds an `X-Upstream-Addr` response header showing which `api` replica
  served the request — useful for demonstrating the load balancing (e.g.
  `curl -i` in a loop).
- `client_max_body_size 1m` and `server_tokens off` bound payload size and
  avoid leaking the nginx version to clients.

**Dockerfiles** (`build/Dockerfile.{api,matcher,outbox-publisher}`) all
follow the same two-stage shape: a `golang:1.25-alpine` build stage
(`CGO_ENABLED=0 go build`)
producing a static binary, copied into a minimal `alpine:3.20` final stage
running as `USER nobody`. Only `Dockerfile.api` declares `EXPOSE 8080` — the
other two binaries have no listening port.

## Key invariants

- **Single-Writer Book** — `internal/domain/matching/book.go:64-68` is not
  thread-safe by design. Correctness relies on exactly one goroutine in one
  consumer instance processing stream entries.
- **Pure Engine** — `matching.Match()` (`internal/domain/matching/engine.go:20-38`)
  has zero I/O; it returns a `MatchResult`. All Postgres writes are owned by
  `applyBatch` in `matcherapp`.
- **Idempotency** — the `processed_stream_events` table uses `INSERT ON
CONFLICT DO NOTHING`. Entries already applied survive crash-between-
  commit-and-ACK safely.
- **Crash Recovery** — on restart, `RecoverBook`
  (`internal/application/matcherapp/book_recovery.go`) rebuilds the
  in-memory book from all `OPEN`/`PARTIALLY_FILLED` LIMIT orders in Postgres
  (oldest first), then `ReclaimPending` drains the PEL (pending entries
  list) before entering the main poll loop.
- **Wallet locking** — `applyBatch` does `SELECT FOR UPDATE` at most once
  per wallet per batch via a `map[uuid.UUID]*wallet.Wallet` cache; all
  updates flush at transaction end.

See [PRINCIPLES.md](./PRINCIPLES.md) for the _why_ behind each of these,
with the underlying source-comment citations.

## Package layout

```
cmd/
  api/                      main() for the HTTP API binary
  matcher/                  main() for the matching engine binary
  outbox-publisher/         main() for the outbox relay binary
internal/
  domain/
    order/        Order aggregate (Fill, Cancel, IsDone)
    trade/        Trade value object (immutable execution record)
    wallet/       Wallet aggregate (Reserve, Release, Settle)
    user/         User identity value object
    matching/     book.go (levelHeap, FIFO per price level), engine.go (Match)
  application/
    orders/       PlaceOrderService, CancelOrderService, query read models, events
    users/        User query read models
    matcherapp/   consumer_loop.go (Loop.Run, applyBatch), book_recovery.go
    outboxapp/    publisher_loop.go (Publisher.Run, PublishOnce)
  infra/
    httpapi/      chi router, handlers, dto.go (validated request/response structs), middleware
    postgres/     repos: order, wallet, trade, user, outbox, processed_events
    redisstream/  consumer.go (ReadBatch, ReadPending, Ack), producer.go (XAdd)
  platform/
    config/       Env-var loader
    db/           sqlx.Connect wrapper, wrapped with otelsql
    logger/       zap production logger (JSON) + trace_fields.go (trace/span-id correlation)
    redisclient/  go-redis client constructor, wrapped with redisotel
    telemetry/    OpenTelemetry tracer-provider bootstrap shared by all 3 binaries
```

## Database

Six tables (`migrations/`): `users`, `wallets`, `orders`, `trades`,
`processed_stream_events`, `outbox_events`.

- `wallets.user_id` has `FK -> users(id)` (added in `000004_users.up.sql`),
  so `users` is the root identity anchor the rest of the FK chain
  (`orders.user_id`, `trades.buyer_user_id`/`seller_user_id`) ultimately
  depends on. The FK was added _after_ backfilling `users` with the same
  fixed UUIDs already referenced by the pre-existing `wallets` seed rows —
  see the migration's own comment for why this had to be one migration, not
  split across two.
- `orders.price_cents` is nullable (`NULL` for MARKET orders).
  `orders.status` is one of `OPEN | PARTIALLY_FILLED | FILLED | CANCELLED`
  (`order_status` enum).
- `outbox_events.published` defaults to `false`; a partial index
  (`idx_outbox_unpublished`) keeps the publisher's poll query cheap as the
  table grows.
- `trades` uses keyset pagination — `idx_trades_executed_at_id ON
trades(executed_at DESC, id DESC)` (`000003_trade_history_indexes.up.sql`)
  — no `OFFSET`.

**Seed data:** `000002_seed_wallets.up.sql` seeds 5 wallets and
`000004_users.up.sql` seeds the matching `users` rows — `alice`–`eve`
(UUIDs `…0001`–`…0005`), each with R$ 10,000,000.00 (`10_000_000_000` BRL
cents) and 10,000,000 Vibranium units. Use these IDs in manual testing, or
`GET /users` to list them. `scripts/validate-post-test.sh` uses these exact
constants (`INITIAL_BRL_CENTS = 50000000000`, `INITIAL_VIBRANIUM =
50000000` — 5 wallets × the per-wallet seed) to assert conservation after a
load test; see [TESTS.md](./TESTS.md).

## Configuration

All configuration is environment variables, loaded by
`internal/platform/config/config.go`. Required: `DATABASE_URL`,
`REDIS_URL` (startup fails without either). Everything else has a default:

| Variable                      | Default                 | Purpose                                                                   |
| ----------------------------- | ----------------------- | ------------------------------------------------------------------------- |
| `APP_PORT`                    | `8080`                  | `cmd/api` listen port                                                     |
| `LOG_LEVEL`                   | `info`                  | zap log level                                                             |
| `ORDERS_STREAM_NAME`          | `orders:incoming`       | Redis Stream the matcher consumes                                         |
| `TRADES_STREAM_NAME`          | `trades:executed`       | (reserved for future use)                                                 |
| `CONSUMER_GROUP_NAME`         | `matcher-group`         | Redis consumer group name                                                 |
| `MATCHER_BATCH_SIZE`          | `200`                   | Max entries per matcher micro-batch                                       |
| `MATCHER_BATCH_TIMEOUT_MS`    | `50`                    | Max wait before flushing a partial batch                                  |
| `OUTBOX_BATCH_SIZE`           | `200`                   | Max rows per outbox-publisher poll                                        |
| `OUTBOX_POLL_INTERVAL_MS`     | `100`                   | Delay between outbox polls                                                |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://localhost:4318` | OTLP/HTTP collector endpoint — see [OBSERVABILITY.md](./OBSERVABILITY.md) |
| `OTEL_TRACES_SAMPLER_ARG`     | `1.0`                   | Trace sampling ratio (0.0–1.0)                                            |

## Further reading

- [`STACK.md`](./STACK.md) — pinned versions for everything named above.
- [`OBSERVABILITY.md`](./OBSERVABILITY.md) — full tracing/logging wiring.
