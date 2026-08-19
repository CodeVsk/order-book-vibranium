# Tests

## Test tiers

| Tier | `make` target | Command | Build tag | Docker? | What it proves |
|---|---|---|---|---|---|
| Unit | `test` | `go test ./... -short -count=1` | (none) | No | Domain logic and pure functions in isolation. |
| Integration | `test-integration` | `go test ./... -tags=integration -count=1` | `integration` | Yes | Full pipeline through real Postgres + Redis. |
| Concurrency | `test-concurrency` | `go test ./test/concurrency/... -tags=integration -race -count=1 -v` | `integration` | Yes | No negative balances / conservation under 200 concurrent goroutines, run with the race detector. |
| Idempotency | (part of `test-integration`) | `go test -run TestApplyBatch_ReprocessingSameEntryID_HasSingleEffect -tags=integration ./internal/application/matcherapp/...` | `integration` | Yes | Reprocessing the same stream entry has a single effect. |

## Unit tests (no build tag — run by `make test`)

| Package | File | Covers |
|---|---|---|
| `internal/domain/matching` | `book_test.go` | `AddResting`/best-opposite-price lookup, `Cancel` removes/no-ops, empty-level pruning after all orders at a price are removed. |
| `internal/domain/matching` | `engine_test.go` | Full match at same price, partial-fill price-time priority, no-match on incompatible price, MARKET consuming multiple levels, BUY MARKET stopping on insufficient funds, SELL MARKET with no liquidity cancelling immediately, cancel-then-rematch producing no trade, cancel-already-filled no-op. |
| `internal/domain/order` | `order_test.go` | `Fill` partial-then-full, `Fill` exceeding remaining (error), `Cancel` no-op when already filled, `Cancel` open→cancelled. |
| `internal/domain/trade` | `trade_test.go` | `New` rejects non-positive price/quantity; valid trade construction. |
| `internal/domain/wallet` | `wallet_test.go` | `ReserveBRL` insufficient/success, `SettleBuyMarketFill` direct debit, `SettleSellFill` releases reservation + credits BRL, `ReleaseBRLReservation` on cancel, table-driven "never goes negative" test. |
| `internal/application/orders` | `place_order_service_test.go` | `TestValidatePlaceOrderInput`. |
| `internal/infra/httpapi` | `dto_test.go` | `PlaceOrderRequest`/`CancelOrderRequest` validation. |
| `internal/infra/httpapi` | `middleware_test.go` | `CorrelationID` header present/absent/exceeds-max-length, `RequestLogging` explicit vs implicit status write, `RequestIDFromContext` missing/present. |
| `internal/infra/postgres` | `trade_repo_test.go` | Keyset-cursor encode/decode round-trip and error cases (invalid base64, missing separator, invalid UUID, invalid timestamp). |
| `internal/platform/config` | `config_test.go` | Defaults/overrides, missing `DATABASE_URL` error. |

## Integration tests (`//go:build integration`, package `integration`)

| File | Covers |
|---|---|
| `roundtrip_test.go` | `TestRoundTrip_PlaceOrder_ThroughMatch_ToWalletSettlement` — full pipeline place → publish → match → settle for alice/bob. |
| `cancel_order_test.go` | Ownership check (forbidden if not the owner), not-found, already-terminal (filled/cancelled — verifies no duplicate outbox event), success writes a cancel outbox event, wallet-reservation-release for resting buy/sell limits and partial fills. |
| `order_handler_test.go` | HTTP status-code mapping for `POST/GET/DELETE /orders` (202, 404, 403, user/wallet-not-found error codes). |
| `user_handler_test.go` | `/users` and `/users/{id}` — seeded users, 404, malformed ID → 400. |
| `outbox_resilience_test.go` | `TestOutboxResilience_SurvivesRedisUnavailability`. |
| `outbox_partial_batch_test.go` | `TestOutboxPublisher_MidBatchFailure_NeverDuplicatesAlreadyPublishedEvents` — uses a `failNthProducer` test double to deterministically fail the Nth publish call mid-batch; regression test for a previously fixed bug about per-event vs. deferred `MarkPublished` transactions. |
| `logger_helper_test.go` | Shared `testLogger` helper (trivial). |

## Shared harness — `test/integration/testenv/testenv.go`

Spins up a real `postgres:16-alpine` and `redis:7-alpine` via
testcontainers-go, applies every migration in `migrations/` via
golang-migrate, and exposes `Env{DB, Redis}` plus `Cleanup()`. Also imported
directly by `test/concurrency/concurrency_test.go` and
`internal/application/matcherapp/idempotency_test.go` — it's the one shared
fixture package in the repo (there is no separate `test/testutil`).

Notable design points:
- `Cleanup()` uses a fresh `context.Background()`, not the caller's — the
  caller's context may already be cancelled by the time cleanup runs.
- Redis container readiness can lag actual port availability in some Docker
  network setups, hence `connectRedisWithRetry` — a 10s-timeout / 200ms-interval
  retry loop.
- `migrationsDir()` anchors to its own source file via `runtime.Caller(0)`,
  so it resolves correctly regardless of which package (`test/integration`,
  `test/concurrency`, `internal/application/matcherapp`) calls `Setup`.

## Concurrency test — `test/concurrency/concurrency_test.go`

`TestConcurrentOrderPlacement_NoNegativeBalances_AndConservation` spawns 200
goroutines placing random LIMIT orders against the 5 seeded wallets
concurrently, then asserts (via a `sumWalletTotals` helper) that no wallet
ever goes negative and that total BRL/Vibranium (balance + reserved) is
conserved before and after. Run with `-race` — this is the test that
exercises the [single-writer book invariant](./PRINCIPLES.md#order-book-invariants)
under real contention.

## Idempotency test — `internal/application/matcherapp/idempotency_test.go`

`TestApplyBatch_ReprocessingSameEntryID_HasSingleEffect` verifies that
reprocessing the same stream entry ID through `applyBatch` has a single
effect, backing the `processed_stream_events` idempotency invariant (see
[ARCHITECTURE.md § Key invariants](./ARCHITECTURE.md#key-invariants)).

## Post-load-test validation — `scripts/validate-post-test.sh` / `make validate`

Runs against the local docker-compose stack after a k6 run and checks, in
order:

1. **Conservation invariants** — total `balance + reserved` for BRL and
   Vibranium across all wallets must equal the seed constants
   (`INITIAL_BRL_CENTS = 50000000000`, `INITIAL_VIBRANIUM = 50000000` — 5
   wallets × the per-wallet amount from `000002_seed_wallets.up.sql`).
2. **Wallet invariants** — no wallet has a negative balance or reservation.
3. **Order fill consistency** — no order is over-filled, every `FILLED`
   order has `filled_quantity == quantity`, no `OPEN`/`PARTIALLY_FILLED`
   order is already fully consumed, and each order's `filled_quantity`
   matches the sum of its linked `trades` rows.
4. **Trade settlement** — reports total BRL volume and Vibranium traded,
   and checks every trade references existing orders (no orphans).
5. **Outbox pipeline** — reports total/published/pending `outbox_events`;
   fails if more than 10 events are still pending (warns if ≤10, since
   that's likely just in-flight).
6. **Redis stream** — reads the matcher consumer group's lag via `XINFO
   GROUPS orders:incoming`; fails if lag exceeds 100 (matcher may be
   stuck), warns between 1–100.
7. **Summary stats** — order counts by status and trade volume/price
   stats, printed for human review (not pass/fail).

## Single-test run examples

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
