-- +goose Up
-- Region hierarchy for district -> state -> global price fallback,
-- plus level/source for the offline-seeded AP+TG dataset.
ALTER TABLE pricing_regions
    ADD COLUMN IF NOT EXISTS level TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS parent_id UUID REFERENCES pricing_regions(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS pricing_regions_parent_idx ON pricing_regions(parent_id);
CREATE INDEX IF NOT EXISTS pricing_regions_level_idx ON pricing_regions(level);

-- +goose Down
DROP INDEX IF EXISTS pricing_regions_level_idx;
DROP INDEX IF EXISTS pricing_regions_parent_idx;
ALTER TABLE pricing_regions
    DROP COLUMN IF EXISTS source,
    DROP COLUMN IF EXISTS parent_id,
    DROP COLUMN IF EXISTS level;
