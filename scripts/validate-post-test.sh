#!/usr/bin/env bash
# scripts/validate-post-test.sh
#
# Validates system consistency after a k6 load test.
# Runs against the local docker-compose stack.
#
# Usage:
#   make validate          # after make k6
#   ./scripts/validate-post-test.sh
#
set -euo pipefail

POSTGRES_CONTAINER="build-postgres-1"
REDIS_CONTAINER="build-redis-1"

# Seed constants (from 000002_seed_wallets.up.sql)
INITIAL_BRL_CENTS=50000000000   # 5 wallets × 10_000_000_000
INITIAL_VIBRANIUM=50000000      # 5 wallets × 10_000_000

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
RESET='\033[0m'

PASS=0
FAIL=0

pass() { echo -e "  ${GREEN}✓${RESET} $1"; ((PASS++)) || true; }
fail() { echo -e "  ${RED}✗${RESET} $1"; ((FAIL++)) || true; }
warn() { echo -e "  ${YELLOW}⚠${RESET} $1"; }
section() { echo -e "\n${BOLD}${BLUE}── $1${RESET}"; }

psql() {
  docker exec "$POSTGRES_CONTAINER" \
    psql -U orderbook -d orderbook -t -A -c "$1" 2>/dev/null
}

redis() {
  docker exec "$REDIS_CONTAINER" redis-cli "$@" 2>/dev/null
}

echo -e "${BOLD}Vibranium Order Book — Post-Test Consistency Report${RESET}"
echo "$(date)"

# ── 1. CONSERVATION INVARIANTS ─────────────────────────────────────────────
section "1. Conservation invariants (no money created or destroyed)"

total_brl=$(psql "SELECT SUM(balance_brl_cents + reserved_brl_cents) FROM wallets;")
drift_brl=$((total_brl - INITIAL_BRL_CENTS))
if [ "$drift_brl" -eq 0 ]; then
  pass "BRL total conserved: ${total_brl} cents (no drift)"
else
  fail "BRL drift detected: expected ${INITIAL_BRL_CENTS}, got ${total_brl} (drift=${drift_brl})"
fi

total_vib=$(psql "SELECT SUM(balance_vibranium + reserved_vibranium) FROM wallets;")
drift_vib=$((total_vib - INITIAL_VIBRANIUM))
if [ "$drift_vib" -eq 0 ]; then
  pass "Vibranium total conserved: ${total_vib} units (no drift)"
else
  fail "Vibranium drift detected: expected ${INITIAL_VIBRANIUM}, got ${total_vib} (drift=${drift_vib})"
fi

# ── 2. WALLET INVARIANTS ────────────────────────────────────────────────────
section "2. Wallet invariants (no negative balances)"

neg_brl=$(psql "SELECT COUNT(*) FROM wallets WHERE balance_brl_cents < 0 OR reserved_brl_cents < 0;")
if [ "$neg_brl" -eq 0 ]; then
  pass "No negative BRL balances"
else
  fail "${neg_brl} wallet(s) with negative BRL balance/reserved"
fi

neg_vib=$(psql "SELECT COUNT(*) FROM wallets WHERE balance_vibranium < 0 OR reserved_vibranium < 0;")
if [ "$neg_vib" -eq 0 ]; then
  pass "No negative Vibranium balances"
else
  fail "${neg_vib} wallet(s) with negative Vibranium balance/reserved"
fi

# ── 3. ORDER FILL CONSISTENCY ───────────────────────────────────────────────
section "3. Order fill consistency (filled_quantity matches trade history)"

over_filled=$(psql "SELECT COUNT(*) FROM orders WHERE filled_quantity > quantity;")
if [ "$over_filled" -eq 0 ]; then
  pass "No over-filled orders"
else
  fail "${over_filled} order(s) with filled_quantity > quantity"
fi

bad_filled=$(psql "SELECT COUNT(*) FROM orders WHERE status = 'FILLED' AND filled_quantity != quantity;")
if [ "$bad_filled" -eq 0 ]; then
  pass "All FILLED orders are fully filled"
else
  fail "${bad_filled} FILLED order(s) with filled_quantity != quantity"
fi

bad_open=$(psql "SELECT COUNT(*) FROM orders WHERE status IN ('OPEN','PARTIALLY_FILLED') AND filled_quantity >= quantity;")
if [ "$bad_open" -eq 0 ]; then
  pass "No OPEN/PARTIALLY_FILLED orders are fully consumed"
else
  fail "${bad_open} OPEN/PARTIALLY_FILLED order(s) with filled_quantity >= quantity"
fi

fill_mismatch=$(psql "
  SELECT COUNT(*) FROM (
    SELECT o.id
    FROM orders o
    LEFT JOIN trades t
      ON (o.side = 'BUY'  AND t.buy_order_id  = o.id)
      OR (o.side = 'SELL' AND t.sell_order_id = o.id)
    GROUP BY o.id, o.filled_quantity
    HAVING o.filled_quantity != COALESCE(SUM(t.quantity), 0)
  ) x;
")
if [ "$fill_mismatch" -eq 0 ]; then
  pass "filled_quantity matches trade records for all orders"
else
  fail "${fill_mismatch} order(s) where filled_quantity diverges from trade history"
fi

# ── 4. TRADE SETTLEMENT ─────────────────────────────────────────────────────
section "4. Trade settlement (BRL + Vibranium flow per trade)"

# For each trade: buyer pays price_cents×quantity BRL, receives quantity VIB
# seller receives price_cents×quantity BRL, delivers quantity VIB.
# Net effect on total BRL and VIB must be zero.
brl_from_trades=$(psql "SELECT COALESCE(SUM(price_cents * quantity), 0) FROM trades;")
vib_from_trades=$(psql "SELECT COALESCE(SUM(quantity), 0) FROM trades;")
echo -e "  Total BRL volume settled : ${brl_from_trades} cents"
echo -e "  Total Vibranium traded   : ${vib_from_trades} units"

orphan_trades=$(psql "
  SELECT COUNT(*) FROM trades t
  WHERE NOT EXISTS (SELECT 1 FROM orders WHERE id = t.buy_order_id)
     OR NOT EXISTS (SELECT 1 FROM orders WHERE id = t.sell_order_id);
")
if [ "$orphan_trades" -eq 0 ]; then
  pass "All trades reference valid orders (no orphans)"
else
  fail "${orphan_trades} trade(s) referencing non-existent orders"
fi

# ── 5. OUTBOX PIPELINE ──────────────────────────────────────────────────────
section "5. Outbox pipeline (events published to Redis)"

outbox_total=$(psql "SELECT COUNT(*) FROM outbox_events;")
outbox_published=$(psql "SELECT COUNT(*) FROM outbox_events WHERE published = true;")
outbox_pending=$(psql "SELECT COUNT(*) FROM outbox_events WHERE published = false;")

echo -e "  Total outbox events : ${outbox_total}"
echo -e "  Published           : ${outbox_published}"
echo -e "  Pending             : ${outbox_pending}"

if [ "$outbox_pending" -eq 0 ]; then
  pass "Outbox fully drained (0 pending events)"
elif [ "$outbox_pending" -le 10 ]; then
  warn "Outbox nearly drained (${outbox_pending} pending — likely in-flight)"
else
  fail "Outbox backlog: ${outbox_pending} unpublished events"
fi

# ── 6. REDIS STREAM ─────────────────────────────────────────────────────────
section "6. Redis stream (matcher consumer group lag)"

lag=$(redis XINFO GROUPS orders:incoming 2>/dev/null | awk '/^lag$/{getline; print}')
pending=$(redis XPENDING orders:incoming matcher-group - + 1 2>/dev/null | wc -l | tr -d ' ')

if [ -z "$lag" ]; then
  warn "Could not read stream lag (stream may not exist yet)"
else
  echo -e "  Stream lag              : ${lag}"
  if [ "$lag" -eq 0 ]; then
    pass "Matcher consumer group fully caught up (lag=0)"
  elif [ "$lag" -le 100 ]; then
    warn "Matcher lag is ${lag} entries — still draining"
  else
    fail "Matcher lag is ${lag} entries — matcher may be stuck"
  fi
fi

# ── 7. SUMMARY STATS ────────────────────────────────────────────────────────
section "7. Order & trade summary"

echo ""
psql "
  SELECT
    status,
    COUNT(*)           AS orders,
    SUM(quantity)      AS total_qty,
    SUM(filled_quantity) AS filled_qty
  FROM orders
  GROUP BY status
  ORDER BY status;
" | column -t -s '|' | sed 's/^/  /'

echo ""
psql "
  SELECT
    COUNT(*)                          AS total_trades,
    SUM(quantity)                     AS vibranium_traded,
    SUM(price_cents * quantity)       AS brl_volume_cents,
    MIN(price_cents)                  AS min_price_cents,
    MAX(price_cents)                  AS max_price_cents,
    AVG(price_cents)::BIGINT          AS avg_price_cents
  FROM trades;
" | column -t -s '|' | sed 's/^/  /'

# ── RESULT ──────────────────────────────────────────────────────────────────
echo ""
echo -e "${BOLD}──────────────────────────────────────────${RESET}"
if [ "$FAIL" -eq 0 ]; then
  echo -e "${GREEN}${BOLD}PASSED${RESET} — ${PASS} checks passed, 0 failures"
else
  echo -e "${RED}${BOLD}FAILED${RESET} — ${PASS} passed, ${FAIL} failed"
  exit 1
fi
