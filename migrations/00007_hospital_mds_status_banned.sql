-- +goose Up
-- Fix hospital_mds.status CHECK to include 'banned' (used by BanHospitalMD store.go:1479).
-- Original 00001_init.sql:207 had CHECK IN ('pending','active','rejected') but code writes 'banned'.
ALTER TABLE hospital_mds DROP CONSTRAINT IF EXISTS hospital_mds_status_check;
ALTER TABLE hospital_mds ADD CONSTRAINT hospital_mds_status_check CHECK (status IN ('pending','active','rejected','banned'));

-- +goose Down
ALTER TABLE hospital_mds DROP CONSTRAINT IF EXISTS hospital_mds_status_check;
ALTER TABLE hospital_mds ADD CONSTRAINT hospital_mds_status_check CHECK (status IN ('pending','active','rejected'));
