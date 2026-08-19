-- Introduces a real `users` table so the identities already referenced by
-- wallets/orders/trades (the fixed alice..eve UUIDs seeded in
-- 000002_seed_wallets) become queryable data instead of just SQL comments.
--
-- Order matters: users must exist (schema + seed rows) BEFORE the FK is
-- added to wallets, since wallets already has 5 rows for these exact UUIDs
-- from migration 000002 — adding the FK first would have nothing to
-- validate against and adding it after an empty users table would fail
-- immediately. Kept as one migration (not split like 000001/000002) so
-- there's never an intermediate state where wallets.user_id lacks a
-- matching users row.
CREATE TABLE users (
    id          UUID PRIMARY KEY,
    username    TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO users (id, username) VALUES
    ('00000000-0000-0000-0000-000000000001', 'alice'),
    ('00000000-0000-0000-0000-000000000002', 'bob'),
    ('00000000-0000-0000-0000-000000000003', 'carol'),
    ('00000000-0000-0000-0000-000000000004', 'dave'),
    ('00000000-0000-0000-0000-000000000005', 'eve');

ALTER TABLE wallets
    ADD CONSTRAINT fk_wallets_user FOREIGN KEY (user_id) REFERENCES users(id);
