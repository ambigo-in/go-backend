-- +goose Up
-- V2 H3 region pricing: polygons from OSM stored as H3 cell sets (res 7),
-- per-region per-type full price rows. Global ambulance_types stays fallback.
CREATE TABLE IF NOT EXISTS pricing_regions (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    osm_id BIGINT NOT NULL DEFAULT 0,
    polygon JSONB NOT NULL DEFAULT '{}'::jsonb,
    cells TEXT[] NOT NULL DEFAULT '{}',
    cell_res INTEGER NOT NULL DEFAULT 7,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS pricing_regions_cells_idx ON pricing_regions USING GIN (cells);

CREATE TABLE IF NOT EXISTS region_prices (
    region_id UUID NOT NULL REFERENCES pricing_regions(id) ON DELETE CASCADE,
    amb_type_id UUID NOT NULL REFERENCES ambulance_types(id) ON DELETE CASCADE,
    base_fare DOUBLE PRECISION NOT NULL DEFAULT 0,
    pricing_tier JSONB NOT NULL DEFAULT '[]'::jsonb,
    driver_share DOUBLE PRECISION NOT NULL DEFAULT 50,
    listing_threshold DOUBLE PRECISION NOT NULL DEFAULT 10,
    helper_included BOOLEAN NOT NULL DEFAULT false,
    otp_required BOOLEAN NOT NULL DEFAULT false,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (region_id, amb_type_id)
);

ALTER TABLE rides ADD COLUMN IF NOT EXISTS region_id UUID REFERENCES pricing_regions(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE rides DROP COLUMN IF EXISTS region_id;
DROP TABLE IF EXISTS region_prices;
DROP TABLE IF EXISTS pricing_regions;
