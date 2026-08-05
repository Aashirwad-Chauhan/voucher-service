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
CREATE INDEX IF NOT EXISTS idx_idempotency_keys_key_created ON idempotency_keys(key, created_at);

-- High-concurrency 1-shot redemption function (Redis Lua style)
DROP FUNCTION IF EXISTS redeem_voucher(TEXT, TEXT, TEXT, TEXT);
CREATE OR REPLACE FUNCTION redeem_voucher(
    p_code TEXT,
    p_user_id TEXT,
    p_idempotency_key TEXT,
    p_fingerprint TEXT
) RETURNS TABLE (
    out_result_status TEXT,
    out_redemption_id UUID,
    out_remaining INT,
    out_response_body BYTEA
) AS $$
DECLARE
    v_idempotency RECORD;
    v_voucher_id UUID;
    v_remaining INT;
    v_redemption_id UUID;
    v_resp_json JSONB;
BEGIN
    -- 1. Idempotency Check (24-hour window)
    SELECT ik.fingerprint, ik.response_body
    INTO v_idempotency
    FROM idempotency_keys ik
    WHERE ik.key = p_idempotency_key AND ik.created_at > (NOW() - INTERVAL '24 hours');

    IF FOUND THEN
        IF v_idempotency.fingerprint = p_fingerprint THEN
            RETURN QUERY SELECT 'replay'::TEXT, NULL::UUID, 0, (v_idempotency.response_body::TEXT)::BYTEA;
            RETURN;
        ELSE
            RETURN QUERY SELECT 'conflict'::TEXT, NULL::UUID, 0, NULL::BYTEA;
            RETURN;
        END IF;
    END IF;

    -- 2. Atomic CAS Decrement
    UPDATE vouchers v
    SET remaining = v.remaining - 1
    WHERE v.code = p_code AND v.remaining > 0
    RETURNING v.id, v.remaining INTO v_voucher_id, v_remaining;

    IF NOT FOUND THEN
        SELECT v.remaining INTO v_remaining FROM vouchers v WHERE v.code = p_code;
        IF NOT FOUND THEN
            RETURN QUERY SELECT 'not_found'::TEXT, NULL::UUID, 0, NULL::BYTEA;
            RETURN;
        ELSE
            RETURN QUERY SELECT 'exhausted'::TEXT, NULL::UUID, 0, NULL::BYTEA;
            RETURN;
        END IF;
    END IF;

    -- 3. Record Redemption & Save Idempotency Record
    v_redemption_id := gen_random_uuid();
    INSERT INTO redemptions (id, voucher_id, user_id, idempotency_key)
    VALUES (v_redemption_id, v_voucher_id, p_user_id, p_idempotency_key);

    v_resp_json := jsonb_build_object(
        'redemption_id', v_redemption_id,
        'remaining', v_remaining
    );

    INSERT INTO idempotency_keys (key, fingerprint, voucher_code, response_code, response_body)
    VALUES (p_idempotency_key, p_fingerprint, p_code, 200, v_resp_json);

    RETURN QUERY SELECT 'granted'::TEXT, v_redemption_id, v_remaining, (v_resp_json::TEXT)::BYTEA;
    RETURN;
END;
$$ LANGUAGE plpgsql;
