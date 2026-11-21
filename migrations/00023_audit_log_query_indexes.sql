-- +goose Up
-- GIN index using jsonb_path_ops for fast payload containment queries (e.g. payload @> '{"admin_id":"..."}')
CREATE INDEX IF NOT EXISTS audit_log_payload_gin ON audit_log USING GIN (payload jsonb_path_ops);

-- +goose Down
DROP INDEX IF EXISTS audit_log_payload_gin;
