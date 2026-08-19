// scripts/k6/load-test.js
//
// Usage:
//   make k6               # local profile (300 req/s, 1 min) — safe on a laptop
//   make k6-full          # full profile  (5000 req/s, 2 min) — requires a separate load generator
//   PROFILE=full k6 run scripts/k6/load-test.js
//
import http from 'k6/http';
import { check } from 'k6';
import { Counter, Trend } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const USERS = [
  '00000000-0000-0000-0000-000000000001',
  '00000000-0000-0000-0000-000000000002',
  '00000000-0000-0000-0000-000000000003',
  '00000000-0000-0000-0000-000000000004',
  '00000000-0000-0000-0000-000000000005',
];

const errors5xx = new Counter('errors_5xx');
const placeLatency = new Trend('place_order_duration', true);

// Profiles ----------------------------------------------------------------
// local: conservative — exercises all code paths without saturating the host.
// full:  production-equivalent — must run k6 from a separate machine.
const PROFILES = {
  local: {
    rate: 300,
    duration: '1m',
    preAllocatedVUs: 50,
    maxVUs: 150,
  },
  moderate: {
    rate: 2500,
    duration: '1m',
    preAllocatedVUs: 100,
    maxVUs: 1000,
  },
  full: {
    rate: 5000,
    duration: '2m',
    preAllocatedVUs: 500,
    maxVUs: 2000,
  },
};

const profile = PROFILES[__ENV.PROFILE] || PROFILES.local;

export const options = {
  scenarios: {
    orders: {
      executor: 'constant-arrival-rate',
      rate: profile.rate,
      timeUnit: '1s',
      duration: profile.duration,
      preAllocatedVUs: profile.preAllocatedVUs,
      maxVUs: profile.maxVUs,
    },
  },
  thresholds: {
    http_req_duration: ['p(99)<500'],
    errors_5xx: ['count==0'],
  },
};

// -------------------------------------------------------------------------

// Per-VU local memory of orders this VU has placed, so it can occasionally
// cancel one of its own orders (mirrors real bot behavior). Each entry
// tracks the owning user_id alongside the order_id — DELETE /orders/{id}
// requires the request's user_id to match the order's owner (403
// otherwise), so a cancel must target the actual placing user, not a
// freshly re-rolled random one, or the real cancellation code path is
// almost never exercised under load.
let placedOrders = [];

function randomUser() {
  return USERS[Math.floor(Math.random() * USERS.length)];
}

export default function () {
  if (Math.random() < 0.05 && placedOrders.length > 0) {
    const { orderId, userId } = placedOrders.shift();
    const res = http.del(`${BASE_URL}/orders/${orderId}`, JSON.stringify({ user_id: userId }), {
      headers: { 'Content-Type': 'application/json' },
    });
    check(res, { 'cancel status is 200/202/403/404': (r) => [200, 202, 403, 404].includes(r.status) });
    if (res.status >= 500) errors5xx.add(1);
    return;
  }

  const side = Math.random() < 0.5 ? 'BUY' : 'SELL';
  const isMarket = Math.random() < 0.2; // 20% MARKET, 80% LIMIT
  const userId = randomUser();
  const body = isMarket
    ? { user_id: userId, side, type: 'MARKET', quantity: 1 + Math.floor(Math.random() * 10) }
    : {
        user_id: userId,
        side,
        type: 'LIMIT',
        price_cents: 900 + Math.floor(Math.random() * 200),
        quantity: 1 + Math.floor(Math.random() * 10),
      };

  const res = http.post(`${BASE_URL}/orders`, JSON.stringify(body), {
    headers: { 'Content-Type': 'application/json' },
  });
  placeLatency.add(res.timings.duration);

  check(res, { 'place order status is 202/409': (r) => [202, 409].includes(r.status) });
  if (res.status >= 500) {
    errors5xx.add(1);
  } else if (res.status === 202) {
    const orderId = JSON.parse(res.body).order_id;
    placedOrders.push({ orderId, userId });
    if (placedOrders.length > 200) placedOrders.shift(); // bound memory per VU
  }
}
