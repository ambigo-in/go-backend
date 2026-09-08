-- +goose Up
-- Receptionist email invite: auto-generated username, temp password, status tracking.
ALTER TABLE hospital_receptionists ADD COLUMN IF NOT EXISTS email TEXT;
ALTER TABLE hospital_receptionists ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'invited' CHECK (status IN ('invited','active','disabled'));
ALTER TABLE hospital_receptionists ADD COLUMN IF NOT EXISTS invited_at TIMESTAMPTZ DEFAULT now();
ALTER TABLE hospital_receptionists ADD COLUMN IF NOT EXISTS must_change_password BOOLEAN NOT NULL DEFAULT true;
-- Backfill existing rows: set status=active where active=true, else disabled; must_change false for already active.
UPDATE hospital_receptionists SET status='active', must_change_password=false WHERE active=true AND status='invited';
UPDATE hospital_receptionists SET status='disabled' WHERE active=false;
-- Unique email where not null
CREATE UNIQUE INDEX IF NOT EXISTS hospital_receptionists_email_unique ON hospital_receptionists(email) WHERE email IS NOT NULL;
CREATE INDEX IF NOT EXISTS hospital_receptionists_status_idx ON hospital_receptionists(status);
CREATE INDEX IF NOT EXISTS hospital_receptionists_active_idx ON hospital_receptionists(active);

-- +goose Down
DROP INDEX IF EXISTS hospital_receptionists_active_idx;
DROP INDEX IF EXISTS hospital_receptionists_status_idx;
DROP INDEX IF EXISTS hospital_receptionists_email_unique;
ALTER TABLE hospital_receptionists DROP COLUMN IF EXISTS must_change_password;
ALTER TABLE hospital_receptionists DROP COLUMN IF EXISTS invited_at;
ALTER TABLE hospital_receptionists DROP COLUMN IF EXISTS status;
ALTER TABLE hospital_receptionists DROP COLUMN IF EXISTS email;
