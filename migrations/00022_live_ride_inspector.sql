-- +goose Up
CREATE TABLE ride_safety_state (
    ride_id        UUID PRIMARY KEY REFERENCES rides(id) ON DELETE CASCADE,
    driver_id      UUID NOT NULL REFERENCES drivers(id) ON DELETE CASCADE,
    anchor_lat     DOUBLE PRECISION NOT NULL,
    anchor_lng     DOUBLE PRECISION NOT NULL,
    stopped_since  TIMESTAMPTZ NOT NULL,
    warned         BOOLEAN NOT NULL DEFAULT false,
    escalated      BOOLEAN NOT NULL DEFAULT false,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ride_safety_state_driver_idx ON ride_safety_state(driver_id);
CREATE INDEX audit_log_ride_created_idx
    ON audit_log ((payload->>'ride_id'), created_at)
    WHERE payload ? 'ride_id';

-- +goose Down
DROP INDEX IF EXISTS audit_log_ride_created_idx;
DROP TABLE IF EXISTS ride_safety_state;
