-- +goose Up
-- Hospital triage readiness: per-ride acknowledge.
-- Written by desk staff (receptionist/MD) for their own hospital's incoming
-- rides; survives refresh and shift change. One row per ride.

CREATE TABLE IF NOT EXISTS ride_acknowledgements (
    ride_id     UUID PRIMARY KEY REFERENCES rides(id) ON DELETE CASCADE,
    hospital_id UUID NOT NULL REFERENCES hospitals(id) ON DELETE CASCADE,
    acked_by    TEXT NOT NULL DEFAULT '',
    acked_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ride_acknowledgements_hospital_id_idx ON ride_acknowledgements(hospital_id);

-- +goose Down
DROP TABLE IF EXISTS ride_acknowledgements;
