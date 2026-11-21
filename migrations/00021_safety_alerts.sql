-- +goose Up
CREATE TABLE safety_alerts (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ride_id          UUID NOT NULL REFERENCES rides(id) ON DELETE CASCADE,
    user_id          UUID REFERENCES users(id) ON DELETE SET NULL,
    driver_id        UUID REFERENCES drivers(id) ON DELETE SET NULL,
    alert_type       TEXT NOT NULL CHECK (alert_type IN ('sos_booking','stopped_vehicle')),
    status           TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','acknowledged','resolved')),
    severity         TEXT NOT NULL DEFAULT 'critical' CHECK (severity IN ('high','critical')),
    driver_name      TEXT NOT NULL DEFAULT '',
    driver_mobile    TEXT NOT NULL DEFAULT '',
    location_lat     DOUBLE PRECISION,
    location_lng     DOUBLE PRECISION,
    location_label   TEXT NOT NULL DEFAULT '',
    raised_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    acknowledged_at TIMESTAMPTZ,
    acknowledged_by UUID REFERENCES admins(id) ON DELETE SET NULL,
    resolved_at      TIMESTAMPTZ,
    resolved_by      UUID REFERENCES admins(id) ON DELETE SET NULL,
    resolution       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX safety_alerts_status_raised_idx ON safety_alerts(status, raised_at DESC);
CREATE INDEX safety_alerts_ride_idx ON safety_alerts(ride_id, raised_at DESC);

INSERT INTO safety_alerts (ride_id, user_id, driver_id, alert_type, location_label, raised_at)
SELECT id, user_id, driver_id,
       CASE WHEN emergency_type = 'stopped_vehicle' THEN 'stopped_vehicle' ELSE 'sos_booking' END,
       pickup_address, created_at
FROM rides
WHERE emergency_priority > 0 AND status NOT IN ('COMPLETED','CANCELLED');

-- +goose Down
DROP TABLE IF EXISTS safety_alerts;
