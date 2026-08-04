-- 000001_init.down.sql
-- Rollback: drop all tables

DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS redemptions;
DROP TABLE IF EXISTS vouchers;
