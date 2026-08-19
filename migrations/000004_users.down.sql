-- Reverse order: drop the constraint before the table it references.
ALTER TABLE wallets DROP CONSTRAINT fk_wallets_user;
DROP TABLE users;
