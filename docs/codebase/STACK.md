# Stack

Exact versions matter here — this file is the single place to check them
before touching a dependency or debugging a version-specific behavior. See
[ARCHITECTURE.md](./ARCHITECTURE.md) for how these pieces are wired
together.

## Language & runtime

- **Go 1.25.0** (`go.mod:3`). Module name: `trade-market`.
- Build tags in use: `//go:build integration` gates every integration,
  concurrency, and idempotency test file — see [TESTS.md](./TESTS.md).

## Direct dependencies (`go.mod`)

| Package                                                           | Version  | Concern                                                                                |
| ----------------------------------------------------------------- | -------- | -------------------------------------------------------------------------------------- |
| `github.com/go-chi/chi/v5`                                        | v5.1.0   | HTTP router                                                                            |
| `github.com/go-playground/validator/v10`                          | v10.22.1 | Struct validation (request DTOs)                                                       |
| `github.com/jackc/pgx/v5`                                         | v5.6.0   | Postgres driver                                                                        |
| `github.com/jmoiron/sqlx`                                         | v1.4.0   | SQL convenience layer over `database/sql`                                              |
| `github.com/golang-migrate/migrate/v4`                            | v4.17.1  | DB migrations (also the version of the standalone `migrate` CLI image used in compose) |
| `github.com/redis/go-redis/v9`                                    | v9.22.0  | Redis client (Streams)                                                                 |
| `github.com/redis/go-redis/extra/redisotel/v9`                    | v9.22.0  | OTel instrumentation for go-redis                                                      |
| `github.com/google/uuid`                                          | v1.6.0   | UUID generation                                                                        |
| `go.uber.org/zap`                                                 | v1.27.0  | Structured JSON logging                                                                |
| `github.com/XSAM/otelsql`                                         | v0.43.0  | OTel instrumentation wrapper for `database/sql`                                        |
| `github.com/riandyrn/otelchi`                                     | v0.12.3  | OTel middleware for chi                                                                |
| `go.opentelemetry.io/otel`                                        | v1.44.0  | OTel API                                                                               |
| `go.opentelemetry.io/otel/sdk`                                    | v1.44.0  | OTel SDK (tracer provider, sampler)                                                    |
| `go.opentelemetry.io/otel/trace`                                  | v1.44.0  | OTel trace API                                                                         |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp` | v1.19.0  | OTLP/HTTP span exporter                                                                |
| `github.com/testcontainers/testcontainers-go/modules/postgres`    | v0.33.0  | Integration test harness (real Postgres)                                               |
| `github.com/testcontainers/testcontainers-go/modules/redis`       | v0.33.0  | Integration test harness (real Redis)                                                  |

Notable indirect dependencies worth knowing about: `github.com/docker/docker`
v27.1.1 (pulled in transitively by testcontainers-go, not used by app code
directly), `google.golang.org/grpc` v1.64.1, `go.opentelemetry.io/contrib/
instrumentation/net/http/otelhttp` v0.49.0.

## Datastores

| Datastore | Image (compose)      | Role                                                                                                  |
| --------- | -------------------- | ----------------------------------------------------------------------------------------------------- |
| Postgres  | `postgres:16-alpine` | System of record: wallets, orders, trades, outbox, processed-events.                                  |
| Redis     | `redis:7-alpine`     | Transport only — one Stream (`orders:incoming`) with a single consumer group; not a system of record. |

## Observability stack (pointer)

Jaeger (`jaegertracing/all-in-one:1.60`) is the local-dev-only tracing
backend, wired via OpenTelemetry. Full wiring, instrumentation call sites,
and two security-relevant caveats live in
[OBSERVABILITY.md](./OBSERVABILITY.md) — not repeated here.

## Build & container tooling

- **Dockerfiles** (`build/Dockerfile.{api,matcher,outbox-publisher}`):
  two-stage builds, `golang:1.25-alpine` → `alpine:3.20`, `CGO_ENABLED=0`, `USER nobody`. See
  [ARCHITECTURE.md](./ARCHITECTURE.md) for the full deployment topology.
- **docker-compose** (`build/docker-compose.yml`): 7 services — postgres,
  redis, jaeger, migrate (one-shot), api (×2), nginx, outbox-publisher,
  matcher.
- **nginx** `nginx:1.27-alpine` as the host-facing reverse proxy/load
  balancer.
- **golang-migrate** — both as a Go library dependency (used by
  `test/integration/testenv`) and as the standalone `migrate/migrate:v4.17.1`
  image in compose.

## Load testing (k6)

`scripts/k6/load-test.js`, driven by the `PROFILE` env var
(`scripts/k6/README.md`):

| Profile           | Rate       | Duration | Pre-allocated / max VUs | `make` target      |
| ----------------- | ---------- | -------- | ----------------------- | ------------------ |
| `local` (default) | 300 req/s  | 1m       | 50 / 150                | `make k6`          |
| `moderate`        | 2500 req/s | 1m       | 100 / 1000              | `make k6-moderate` |
| `full`            | 5000 req/s | 2m       | 500 / 2000              | `make k6-full`     |

- Thresholds: `http_req_duration p(99) < 500ms`; `errors_5xx count == 0`
  (only real 5xx responses count — expected `409`s from wallet-balance
  exhaustion or a resolved race do not).
- Traffic mix: 80% LIMIT / 20% MARKET orders, random BUY/SELL, and a 5%
  chance per iteration of cancelling a previously-placed order from the
  same VU (tracked in a per-VU in-memory list, bounded to 200 entries).
- **Known limitation — single-machine saturation:** running the `full`
  profile on the same machine hosting the local Docker stack can saturate
  host CPU/memory and hang the Docker daemon. Not an application bug — the
  app degrades gracefully (`context canceled`, graceful shutdown) and
  recovers after restarting the Docker daemon. Rule: use `make k6` locally;
  reserve `make k6-full` for a separate dedicated machine.
- **Known limitation — wallet cardinality:** `POST /orders` reserves funds
  via `SELECT ... FOR UPDATE` on the user's wallet row, held for the whole
  transaction. With only 5 seeded wallets absorbing all traffic, row
  contention is far higher than in a real deployment with many users — if
  the `p(99)<500ms` threshold fails, compare against a lower `--rate`
  before investigating it as a regression.

## Post-load-test validation

`make validate` runs `scripts/validate-post-test.sh` — see
[TESTS.md](./TESTS.md) for what it checks.
