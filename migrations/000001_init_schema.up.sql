CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE wallets (
    user_id             UUID PRIMARY KEY,
    balance_brl_cents   BIGINT NOT NULL CHECK (balance_brl_cents >= 0),
    reserved_brl_cents  BIGINT NOT NULL DEFAULT 0 CHECK (reserved_brl_cents >= 0),
    balance_vibranium   BIGINT NOT NULL CHECK (balance_vibranium >= 0),
    reserved_vibranium  BIGINT NOT NULL DEFAULT 0 CHECK (reserved_vibranium >= 0),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TYPE order_side   AS ENUM ('BUY', 'SELL');
CREATE TYPE order_type   AS ENUM ('LIMIT', 'MARKET');
CREATE TYPE order_status AS ENUM ('OPEN', 'PARTIALLY_FILLED', 'FILLED', 'CANCELLED');

CREATE TABLE orders (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES wallets(user_id),
    side                order_side NOT NULL,
    type                order_type NOT NULL,
    price_cents         BIGINT,
    quantity            BIGINT NOT NULL CHECK (quantity > 0),
    filled_quantity     BIGINT NOT NULL DEFAULT 0,
    status              order_status NOT NULL DEFAULT 'OPEN',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_orders_user ON orders(user_id);
CREATE INDEX idx_orders_open ON orders(status) WHERE status IN ('OPEN', 'PARTIALLY_FILLED');

CREATE TABLE trades (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    buy_order_id        UUID NOT NULL REFERENCES orders(id),
    sell_order_id       UUID NOT NULL REFERENCES orders(id),
    buyer_user_id       UUID NOT NULL REFERENCES wallets(user_id),
    seller_user_id      UUID NOT NULL REFERENCES wallets(user_id),
    price_cents         BIGINT NOT NULL,
    quantity            BIGINT NOT NULL CHECK (quantity > 0),
    executed_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_trades_buy_order   ON trades(buy_order_id);
CREATE INDEX idx_trades_sell_order  ON trades(sell_order_id);
CREATE INDEX idx_trades_executed_at ON trades(executed_at);

CREATE TABLE processed_stream_events (
    stream_entry_id     TEXT PRIMARY KEY,
    stream_name         TEXT NOT NULL,
    processed_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE outbox_events (
    id           BIGSERIAL PRIMARY KEY,
    stream_name  TEXT NOT NULL,
    payload      JSONB NOT NULL,
    published    BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_outbox_unpublished ON outbox_events(id) WHERE published = false;
