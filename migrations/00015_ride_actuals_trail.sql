-- +goose Up
-- Actuals for gated re-rate: preserve estimate in route_*, store actual drop/route
-- plus GPS trail to detect same-drop detours.
ALTER TABLE rides
    ADD COLUMN IF NOT EXISTS actual_drop JSONB,
    ADD COLUMN IF NOT EXISTS actual_distance_km DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS actual_duration_seconds INTEGER,
    ADD COLUMN IF NOT EXISTS actual_polyline TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS fare_recalc_reason TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS ride_location_trail (
    id UUID PRIMARY KEY,
    ride_id UUID NOT NULL REFERENCES rides(id) ON DELETE CASCADE,
    lat DOUBLE PRECISION NOT NULL,
    lng DOUBLE PRECISION NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ride_trail_ride_at_idx ON ride_location_trail(ride_id, recorded_at);

-- +goose Down
DROP TABLE IF EXISTS ride_location_trail;
ALTER TABLE rides
    DROP COLUMN IF EXISTS fare_recalc_reason,
    DROP COLUMN IF EXISTS actual_polyline,
    DROP COLUMN IF EXISTS actual_duration_seconds,
    DROP COLUMN IF EXISTS actual_distance_km,
    DROP COLUMN IF EXISTS actual_drop;
