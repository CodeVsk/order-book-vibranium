# k6 Load Test — Vibranium Order Book

Sustains configurable req/s of `POST /orders` (mixed LIMIT/MARKET, with occasional
`DELETE /orders/{id}` cancellations) against the 5 seeded wallets, per spec §8.6.

## Prerequisites

- `k6` installed (`brew install k6`).
- The full stack running: `docker compose -f build/docker-compose.yml up --build`.
- Migrations applied (`make migrate-up`) — the 5 seeded wallets must exist.

## Profiles

| Profile | Rate | Duration | VUs (max) | When to use |
|---------|------|----------|-----------|-------------|
| `local` (default) | 300 req/s | 1 min | 150 | Desenvolvimento local |
| `full` | 5000 req/s | 2 min | 2000 | Máquina separada ou ambiente CI |

## Run

```bash
# Perfil local — seguro para rodar no mesmo laptop do stack
make k6

# Perfil completo — k6 em máquina separada apontando para a stack
BASE_URL=http://<ip-do-servidor>:8080 make k6-full

# Ou diretamente com k6:
k6 run scripts/k6/load-test.js                        # local
PROFILE=full k6 run scripts/k6/load-test.js           # full
```

## What to observe

1. **k6's own summary at the end of the run:**
   - `http_req_duration` p(99) — the threshold in the script fails the run
     if it exceeds 500ms; watch the actual number, not just pass/fail.
   - `errors_5xx` — must stay at `count==0` (the script fails the run
     otherwise). Any 5xx here is a bug, not expected backpressure — 409s
     from exhausted wallet balance are expected and are not counted as
     errors.
2. **Redis consumer group lag** (the matcher is a single consumer — this is
   the number that tells you if match throughput is keeping up with
   arrival rate):

       docker exec -it <redis-container> redis-cli XINFO GROUPS orders:incoming
       docker exec -it <redis-container> redis-cli XPENDING orders:incoming matcher-group

   `lag` in `XINFO GROUPS` growing without bound during the run means the
   matcher's micro-batch size/timeout (`MATCHER_BATCH_SIZE`,
   `MATCHER_BATCH_TIMEOUT_MS`) needs tuning, or Postgres write latency is
   the bottleneck.
3. **Outbox backlog** — should stay near zero if the publisher keeps up:

       docker exec -it <postgres-container> psql -U orderbook -d orderbook \
         -c "SELECT count(*) FROM outbox_events WHERE published = false;"

## Known limitation — perfil `full` em máquina única

Rodar o perfil `full` (5000 req/s, 2000 VUs) com k6 **na mesma máquina** que hospeda
o stack Docker local trava o daemon do Docker — não por bug na aplicação, mas por
saturação de CPU/memória do host. A aplicação degrada corretamente (`context canceled`,
shutdown gracioso) e se recupera após reiniciar o Docker.

**Regra:** use `make k6` (perfil `local`) para desenvolvimento local; reserve
`make k6-full` para rodar o k6 em uma máquina dedicada separada do stack.

## Known limitation — cardinalidade de wallets

`POST /orders` reserva saldo via `SELECT ... FOR UPDATE` na linha da wallet do usuário,
mantido durante toda a transação. Com apenas 5 wallets absorvendo todo o req/s, a
contenção por linha é muito maior do que em um deployment real. Se o threshold
`p(99)<500ms` falhar, compare contra um `--rate` menor antes de investigar regressões.
