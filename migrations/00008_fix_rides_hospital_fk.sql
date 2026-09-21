-- +goose Up
-- Fix rides.hospital_id FK to allow hospital deletion when rides reference it.
-- Previous: NO ACTION blocked DELETE FROM hospitals when rides reference it.
-- New: SET NULL preserves ride history with hospital_id = NULL.

ALTER TABLE rides DROP CONSTRAINT IF EXISTS rides_hospital_id_fkey;
ALTER TABLE rides ADD CONSTRAINT rides_hospital_id_fkey
  FOREIGN KEY (hospital_id) REFERENCES hospitals(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE rides DROP CONSTRAINT IF EXISTS rides_hospital_id_fkey;
ALTER TABLE rides ADD CONSTRAINT rides_hospital_id_fkey
  FOREIGN KEY (hospital_id) REFERENCES hospitals(id);
