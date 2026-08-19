# Our Services — Summary

> What each service does and how they exchange events with each other. Three
> independent processes, each with a single responsibility.

## API

**What it does:** it's the system's entry point. It receives client requests
to create a buy/sell order, cancel an order, and query orders, wallet balance
and trade history. When creating or canceling an order, it already reserves
the necessary balance on the spot (it doesn't wait for the trade to happen)
and responds quickly to the client, without waiting for the order to be
effectively processed.

**How it publishes/receives events:** it doesn't talk directly to Redis. When
creating or canceling an order, it just writes a "pending event" to the
database, together with the balance reservation itself, as part of the same
write. The one that actually delivers that event to the queue is the Outbox
Publisher. This guarantees that no order is lost: even if the queue is down
at the time, the event is already recorded and will be delivered as soon as
possible.

## Outbox Publisher

**What it does:** it's the system's "mail carrier." It periodically watches
the database for pending events (created or canceled orders that haven't
been delivered to the queue yet) and sends them, one by one, marking each one
as delivered as soon as it confirms the send.

**How it publishes/receives events:** it doesn't receive anything from
anyone — it only reads the pending events written by the API in the database
and publishes them to the queue that the Matcher listens to. If sending an
event fails, it retries on the next cycle, without duplicating or losing the
ones that were already delivered successfully.

## Matcher

**What it does:** it's the trading engine. It reads orders from the queue
and matches them against each other following the best-price-first rule,
and among orders at the same price, first-come-first-served. When two orders
match, it records the trade, updates the status of the orders involved and
adjusts the balance of both users' wallets — all at once, consistently.

**How it publishes/receives events:** it receives orders (new orders and
cancellations) from the queue fed by the Outbox Publisher, processing them in
small batches. It doesn't publish events to anyone else — it's the endpoint
of the flow: after processing a batch, it writes the result to the database
and only then confirms to the queue that that batch was processed.

## Full flow, in one sentence

A client creates an order on the **API** → the order becomes a pending event
in the database → the **Outbox Publisher** delivers that event to the queue
→ the **Matcher** reads the queue, matches the orders and settles the result
in the database.
