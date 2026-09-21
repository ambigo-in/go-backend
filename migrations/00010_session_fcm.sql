-- +goose Up
-- Session-scoped push tokens for the single-session kill-switch.
-- Per-account fcm_token columns are overwritten by each device's report, so a
-- logout push to superseded devices needs the token captured per session at
-- login/refresh time.

ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS fcm_token TEXT;
CREATE INDEX IF NOT EXISTS refresh_tokens_user_revoked_session_idx
    ON refresh_tokens(user_id, revoked) WHERE revoked = true;

-- +goose Down
DROP INDEX IF EXISTS refresh_tokens_user_revoked_session_idx;
ALTER TABLE refresh_tokens DROP COLUMN IF EXISTS fcm_token;
