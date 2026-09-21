-- +goose Up
-- Money hardening: webhook idempotency, wallet ledger columns, bank verification flag.
-- Every row here supports exactly-once money handling and a complete audit trail.

-- Dedupe table for inbound events (Razorpay webhooks, retries).
-- event_id is the provider's unique id (e.g. x-razorpay-event-id).
CREATE TABLE IF NOT EXISTS processed_events (
    event_id    TEXT PRIMARY KEY,
    event_type  TEXT NOT NULL DEFAULT '',
    payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS processed_events_received_at_idx ON processed_events(received_at);

-- Ledger columns on wallet_transactions so EVERY balance mutation leaves a row.
-- direction: 'credit' | 'debit'. txn_type: withdrawal | ride_credit |
-- commission_debit | referral_credit | refund | admin_adjust | fee.
ALTER TABLE wallet_transactions
    ADD COLUMN IF NOT EXISTS direction TEXT NOT NULL DEFAULT 'debit',
    ADD COLUMN IF NOT EXISTS txn_type TEXT NOT NULL DEFAULT 'withdrawal',
    ADD COLUMN IF NOT EXISTS reference_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS balance_after NUMERIC(12,2);
CREATE INDEX IF NOT EXISTS wallet_transactions_reference_id_idx ON wallet_transactions(reference_id) WHERE reference_id <> '';
CREATE INDEX IF NOT EXISTS wallet_transactions_pending_sweep_idx ON wallet_transactions(status, txn_type, created_at) WHERE status = 'pending';

-- Bank-account verification gate for first payout (penny-drop / name-match).
ALTER TABLE drivers
    ADD COLUMN IF NOT EXISTS wallet_verified BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS wallet_verified_at TIMESTAMPTZ;

-- Backfill: drivers with a previously settled payout (bank reference present
-- on a non-failed row) have proven their account works — grandfather them so
-- the new gate only challenges new or unproven accounts.
-- (Failed rows can carry a bank ref too, hence the status filter.)
UPDATE drivers SET wallet_verified = true, wallet_verified_at = now()
WHERE id::text IN (SELECT DISTINCT driver_id FROM wallet_transactions WHERE bank_reference_no <> '' AND status <> 'failed');

-- +goose Down
ALTER TABLE drivers
    DROP COLUMN IF EXISTS wallet_verified_at,
    DROP COLUMN IF EXISTS wallet_verified;
DROP INDEX IF EXISTS wallet_transactions_pending_sweep_idx;
DROP INDEX IF EXISTS wallet_transactions_reference_id_idx;
ALTER TABLE wallet_transactions
    DROP COLUMN IF EXISTS balance_after,
    DROP COLUMN IF EXISTS reference_id,
    DROP COLUMN IF EXISTS txn_type,
    DROP COLUMN IF EXISTS direction;
DROP INDEX IF EXISTS processed_events_received_at_idx;
DROP TABLE IF EXISTS processed_events;
