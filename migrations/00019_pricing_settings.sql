-- +goose Up
-- Global surcharge settings (single-row): SOS emergency % and night % applied
-- on top of base+distance fare. Seeded to match the previously hardcoded
-- engine values (SOS +50%, night +20%) so behavior is unchanged until edited.
CREATE TABLE IF NOT EXISTS pricing_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    emergency_pct DOUBLE PRECISION NOT NULL DEFAULT 50,
    night_pct DOUBLE PRECISION NOT NULL DEFAULT 20,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO pricing_settings (id, emergency_pct, night_pct)
    VALUES (1, 50, 20)
    ON CONFLICT (id) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS pricing_settings;
