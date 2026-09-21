-- +goose Up
-- MD-managed ambulances: one hospital -> many ambulances -> many driver mobiles.
-- Mobile is the only join to drivers/unverified_drivers; driver rows are untouched.
-- Active is on the link, with one-active-per-number and one-active-per-ambulance.

CREATE TABLE IF NOT EXISTS md_ambulances (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hospital_id     UUID NOT NULL REFERENCES hospitals(id) ON DELETE CASCADE,
    created_by_md_id UUID REFERENCES hospital_mds(id) ON DELETE SET NULL,
    label           TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS md_ambulances_hospital_id_idx ON md_ambulances(hospital_id);

CREATE TABLE IF NOT EXISTS md_ambulance_drivers (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ambulance_id UUID NOT NULL REFERENCES md_ambulances(id) ON DELETE CASCADE,
    driver_mobile TEXT NOT NULL,
    active       BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ambulance_id, driver_mobile)
);
CREATE INDEX IF NOT EXISTS md_ambulance_drivers_ambulance_id_idx ON md_ambulance_drivers(ambulance_id);
CREATE INDEX IF NOT EXISTS md_ambulance_drivers_mobile_idx ON md_ambulance_drivers(driver_mobile);
-- One active link per driver number.
CREATE UNIQUE INDEX IF NOT EXISTS md_ambulance_drivers_one_active_per_mobile
    ON md_ambulance_drivers(driver_mobile) WHERE active = true;
-- One active number per ambulance.
CREATE UNIQUE INDEX IF NOT EXISTS md_ambulance_drivers_one_active_per_ambulance
    ON md_ambulance_drivers(ambulance_id) WHERE active = true;

-- Effective ambulance for verified drivers (set on approval/login from the active link).
ALTER TABLE drivers ADD COLUMN IF NOT EXISTS md_ambulance_id UUID REFERENCES md_ambulances(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS drivers_md_ambulance_id_idx ON drivers(md_ambulance_id) WHERE md_ambulance_id IS NOT NULL;
