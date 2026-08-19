# Critical Analysis — Is the `outbox-publisher` Mandatory?

> Does the `outbox-publisher` need to be the only write path into Redis, or
> could it exist only as a post-crash recovery mechanism? Context:
> [`platform-summary.md`](platform-summary.md). See also
> [`matcher-scalability-analysis.md`](matcher-scalability-analysis.md) for
> the analysis of the system's other singleton.

## TL;DR

The `outbox-publisher` is mandatory as the only write path into Redis.
Downgrading it to "recovery only" doesn't eliminate the component — it still
needs to exist to sweep pending `outbox_events` — it only adds a second
write path (direct dual-write from the API) that reintroduces exactly the
failure the outbox pattern was designed to eliminate.

---

## What exists today

```
POST /orders → BEGIN TX
                 reserve balance (wallets)
                 INSERT outbox_events (published=false)
               COMMIT
               → 202 Accepted

outbox-publisher (singleton, polling every 100ms):
  fetchBatch()          -- SELECT ... FOR UPDATE SKIP LOCKED
  for each event:
    BEGIN TX
      re-lock FOR UPDATE SKIP LOCKED (re-check published=false)
      XADD orders:incoming
      UPDATE outbox_events SET published=true
    COMMIT
```

(`internal/application/outboxapp/publisher_loop.go:60-132`,
`internal/infra/postgres/outbox_repo.go:38-75`)

This is the **transactional outbox pattern** in its canonical form: the API
never talks to Redis; the only guarantee it needs to provide is "if the
commit to Postgres happened, the event *will* be published, eventually, even
if Redis is down right now." The per-event commit (not a commit for the
whole batch) was specifically hardened against duplicate publication on a
mid-batch failure — see the `PublishOnce` comment (lines 44-56), which
documents a real bug already fixed (`364caab`) caused by the previous
version (a single transaction for the entire batch).

## Why "recovery only" doesn't work

The implicit proposal in the question is: the API writes to Postgres **and**
already does an `XADD` directly in the handler (synchronous or
fire-and-forget), and `outbox_events` becomes just an audit/replay table
that a background process sweeps **only when something went wrong**.

The problem is that this reintroduces the **dual-write hazard** the pattern
exists to eliminate, without removing the need for the component:

1. **Concrete failure scenario**: the API commits the balance reservation to
   Postgres, attempts the direct `XADD`, and the `XADD` fails (network
   timeout, Redis restarting, saturated connection under the challenge's
   5000 req/s). The response has already been (or is about to be) sent as
   `202 Accepted` — the client believes the order is queued. It never
   reaches the matcher. The user's balance stays reserved
   (`ReservedBRLCents`/`ReservedVibraniumUnits` debited from the available
   amount) **indefinitely**, until a reconciliation process detects and
   resends it. That reconciliation process **is the outbox-publisher** —
   just running at lower priority, which makes the component worse, not
   unnecessary.
2. **You don't eliminate code, you duplicate it**: now there are two paths
   writing to Redis (direct handler + background reconciler), both need the
   same idempotency (the reconciler might resend something the direct path
   already published successfully — exactly the `LockIfUnpublished` that
   already exists today) and the same error handling. The component doesn't
   disappear; it just gains a more fragile concurrent path.
3. **Availability coupling**: if the `XADD` is synchronous in the handler
   (before the `202`), the API stops responding whenever Redis is degraded,
   even with a healthy Postgres — that's worse, not better, for the
   challenge's 5000 req/s goal, because it adds a network round-trip to a
   stateful external system on every request's critical path.
4. **Publication ordering**: today a single publisher processes `ORDER BY id
   ASC` (`outbox_repo.go:41`), so the arrival order in the Redis Stream is
   deterministic and matches the commit order in Postgres. With multiple API
   replicas publishing directly, arrival order in the stream would depend on
   each replica's network latency, not commit order — a price-time-priority
   ordering that's implicit today (whoever committed first enters the queue
   first) would no longer be guaranteed.

**Conclusion**: keeping the outbox-publisher as the sole write path is the
correct decision — it's simpler, not more complex, than the alternative.

## What can (and should) evolve here

This doesn't mean the component is at its optimization ceiling. Two
low-risk improvement points, without giving up the guarantee:

- **`FOR UPDATE SKIP LOCKED` was already designed for multiple publisher
  replicas** — the code comments are explicit about this
  (`outbox_repo.go:35-37`, `:58-64`; `publisher_loop.go:86-91`). In other
  words, if the bottleneck ever becomes the **publication rate** (not the
  case today: a 100ms poll + a batch of 200 gives far more throughput than
  the matcher can absorb downstream — see
  [`matcher-scalability-analysis.md`](matcher-scalability-analysis.md)),
  just scale up `deploy.replicas` for the `outbox-publisher` in
  compose/k8s. No code change is needed.
- **Replacing polling with Postgres `LISTEN/NOTIFY`** (the API fires
  `NOTIFY` after the commit; the publisher listens and wakes up
  immediately) eliminates the average ~50ms latency (half of
  `OUTBOX_POLL_INTERVAL_MS`) without touching the transactional guarantee —
  polling keeps existing as a safety net (`NOTIFY` can be missed if no one
  is listening at the moment), it just stops being the only trigger.

## Warning sign to monitor

Sustained growth of `outbox_events` with `published=false` (a backing-up
queue) is not, in practice, a symptom of the publisher being slow — it's a
symptom of the **matcher** (the downstream consumer of the Redis Stream) not
absorbing events at the same rate the API produces them. In other words:
before investing in scaling the outbox-publisher, measure the matcher's
consumer group lag. The details on how to scale that side are in
[`matcher-scalability-analysis.md`](matcher-scalability-analysis.md).

## Suggested roadmap

| Timeframe | Action | Why now |
|---|---|---|
| Short | `LISTEN/NOTIFY` instead of pure polling | Reduces end-to-end latency without giving up the transactional guarantee |
| Short | Alert on pending `outbox_events` (count/age of the oldest row) | Detects backlog before it turns into balance reserved for too long |
| Medium | Multiple `outbox-publisher` replicas | Only if the lag is proven to be the publisher's fault, not the matcher's — the code already supports it (`SKIP LOCKED`), it's just configuration |
