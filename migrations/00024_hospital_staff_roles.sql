-- +goose Up
-- Hospital Staff Roles & generalized staff management
CREATE TABLE IF NOT EXISTS hospital_staff_roles (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hospital_id         UUID NOT NULL REFERENCES hospitals(id) ON DELETE CASCADE,
    created_by_md_id    UUID NOT NULL REFERENCES hospital_mds(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    description         TEXT,
    active              BOOLEAN NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT hospital_staff_roles_unique_name UNIQUE (hospital_id, name)
);

CREATE INDEX IF NOT EXISTS hospital_staff_roles_hospital_id_idx ON hospital_staff_roles(hospital_id);
CREATE INDEX IF NOT EXISTS hospital_staff_roles_active_idx ON hospital_staff_roles(active);

-- Add role columns to hospital_receptionists (staff accounts)
ALTER TABLE hospital_receptionists 
    ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'Staff',
    ADD COLUMN IF NOT EXISTS role_id UUID REFERENCES hospital_staff_roles(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS hospital_receptionists_role_idx ON hospital_receptionists(role);
CREATE INDEX IF NOT EXISTS hospital_receptionists_role_id_idx ON hospital_receptionists(role_id);

-- +goose Down
DROP INDEX IF EXISTS hospital_receptionists_role_id_idx;
DROP INDEX IF EXISTS hospital_receptionists_role_idx;
ALTER TABLE hospital_receptionists DROP COLUMN IF EXISTS role_id;
ALTER TABLE hospital_receptionists DROP COLUMN IF EXISTS role;
DROP TABLE IF EXISTS hospital_staff_roles;
