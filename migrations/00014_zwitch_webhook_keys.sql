-- +goose Up
-- Correlate async bank-verification webhooks (verifications.bank_account.created)
-- to drivers: store the merchant reference sent with VerifyBankAccount so the
-- webhook can flip wallet_verified without trusting client-supplied identity.
ALTER TABLE drivers
    ADD COLUMN IF NOT EXISTS wallet_verify_ref TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS drivers_verify_ref_idx ON drivers(wallet_verify_ref) WHERE wallet_verify_ref <> '';

-- Lookup index for transfer webhooks (transfers.updated arrives with the
-- Zwitch transfer id, not our merchant reference).
CREATE INDEX IF NOT EXISTS wallet_tx_transfer_id_idx ON wallet_transactions(zwitch_transfer_id) WHERE zwitch_transfer_id <> '';

-- +goose Down
DROP INDEX IF EXISTS wallet_tx_transfer_id_idx;
DROP INDEX IF EXISTS drivers_verify_ref_idx;
ALTER TABLE drivers
    DROP COLUMN IF EXISTS wallet_verify_ref;
