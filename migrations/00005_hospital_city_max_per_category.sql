-- +goose Up
-- Add per-category cap for hospital seeding (supports Option A split: Emergency vs Non-Emergency, each paginated).
-- Default 40 (2 pages x20) matches plan; max 60 matches Places Text Search limit (3 pages).
ALTER TABLE hospital_cities ADD COLUMN IF NOT EXISTS max_per_category INT NOT NULL DEFAULT 40 CHECK (max_per_category >= 5 AND max_per_category <= 60);

-- +goose Down
ALTER TABLE hospital_cities DROP COLUMN IF EXISTS max_per_category;
