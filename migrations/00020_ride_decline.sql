-- +goose Up
-- Hospital decline + auto-reroute: records which desk declined a mid-ride
-- inbound patient and where the ride was rerouted. One row per ride.
-- The ride row itself is reassigned (hospital_id/drop/drop_address) so the
-- new hospital picks it up on its next incoming/list poll, while the old
-- desk drops it via RIDE_REASSIGNED_AWAY + list filtering.

CREATE TABLE IF NOT EXISTS ride_declines (
    ride_id          UUID PRIMARY KEY REFERENCES rides(id) ON DELETE CASCADE,
    from_hospital_id UUID NOT NULL REFERENCES hospitals(id) ON DELETE CASCADE,
    to_hospital_id   UUID REFERENCES hospitals(id) ON DELETE SET NULL,
    declined_by      TEXT NOT NULL DEFAULT '',
    reason           TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ride_declines_from_hospital_idx ON ride_declines(from_hospital_id);
CREATE INDEX IF NOT EXISTS ride_declines_to_hospital_idx ON ride_declines(to_hospital_id);

-- +goose Down
DROP TABLE IF EXISTS ride_declines;
