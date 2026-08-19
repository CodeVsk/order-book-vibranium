CREATE INDEX idx_trades_buyer_user ON trades(buyer_user_id);
CREATE INDEX idx_trades_seller_user ON trades(seller_user_id);
CREATE INDEX idx_trades_executed_at_id ON trades(executed_at DESC, id DESC);
