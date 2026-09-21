-- +goose Up
-- Scope withdrawal idempotency per driver: the old global UNIQUE on
-- merchant_reference_id leaked idempotency across drivers (same client key
-- from two drivers collided) and misreported other-driver outcomes.
-- Composite key keeps retries idempotent without cross-driver interference.
ALTER TABLE wallet_transactions
    DROP CONSTRAINT IF EXISTS wallet_transactions_merchant_reference_id_key;
ALTER TABLE wallet_transactions
    ADD CONSTRAINT wallet_transactions_driver_ref_unique UNIQUE (driver_id, merchant_reference_id);

-- +goose Down
ALTER TABLE wallet_transactions
    DROP CONSTRAINT IF EXISTS wallet_transactions_driver_ref_unique;
ALTER TABLE wallet_transactions
    ADD CONSTRAINT wallet_transactions_merchant_reference_id_key UNIQUE (merchant_reference_id);
