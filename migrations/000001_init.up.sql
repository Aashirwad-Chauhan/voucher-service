-- 000001_init.up.sql
-- Voucher Redemption Service Schema

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Vouchers: the core entity
CREATE TABLE IF NOT EXISTS vouchers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code            TEXT NOT NULL,
    max_redemptions INT  NOT NULL DEFAULT 1,
    remaining       INT  NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Invariant: code is unique
    CONSTRAINT uq_vouchers_code UNIQUE (code),

    -- Invariant: remaining can never go below 0
    CONSTRAINT chk_remaining_non_negative CHECK (remaining >= 0),

    -- Invariant: remaining can never exceed max_redemptions
    CONSTRAINT chk_remaining_lte_max CHECK (remaining <= max_redemptions),

    -- Invariant: max_redemptions must be positive
    CONSTRAINT chk_max_redemptions_positive CHECK (max_redemptions > 0)
);

-- Redemptions: append-only audit log of every successful redeem
CREATE TABLE IF NOT EXISTS redemptions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    voucher_id      UUID        NOT NULL REFERENCES vouchers(id),
    user_id         TEXT        NOT NULL,
    idempotency_key TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Invariant: each idempotency_key can only produce one redemption
    CONSTRAINT uq_redemptions_idempotency_key UNIQUE (idempotency_key)
);

-- Idempotency keys: stores the response for replay / conflict detection
CREATE TABLE IF NOT EXISTS idempotency_keys (
    key             TEXT PRIMARY KEY,
    fingerprint     TEXT        NOT NULL,
    voucher_code    TEXT        NOT NULL,
    response_code   INT         NOT NULL,
    response_body   JSONB       NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes for query performance
CREATE INDEX IF NOT EXISTS idx_redemptions_voucher_id ON redemptions(voucher_id);
CREATE INDEX IF NOT EXISTS idx_idempotency_keys_created_at ON idempotency_keys(created_at);
