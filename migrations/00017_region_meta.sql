-- +goose Up
-- Region identity for exact OSM re-fetch (refresh) + admin visibility.
ALTER TABLE pricing_regions
    ADD COLUMN IF NOT EXISTS osm_type TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS fetched_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE pricing_regions
    DROP COLUMN IF EXISTS fetched_at,
    DROP COLUMN IF EXISTS display_name,
    DROP COLUMN IF EXISTS osm_type;
