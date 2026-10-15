package ride

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// DeclineRecord is the audit row for a hospital desk declining a mid-ride
// inbound patient. The ride itself is reassigned to the nearest other
// hospital; this row records where it went and who declined it.
type DeclineRecord struct {
	RideID         string     `json:"ride_id"`
	FromHospitalID string     `json:"from_hospital_id"`
	ToHospitalID   *string    `json:"to_hospital_id,omitempty"`
	DeclinedBy     string     `json:"declined_by,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// RecordDecline upserts the decline audit row (idempotent re-declines just
// refresh the target/timestamp). toHospitalID may be nil when no reroute
// target was found.
func (s *Store) RecordDecline(ctx context.Context, rideID, fromHospitalID string, toHospitalID *string, declinedBy, reason string) (*DeclineRecord, error) {
	var toHID interface{}
	if toHospitalID != nil && *toHospitalID != "" {
		toHID = *toHospitalID
	}
	const q = `INSERT INTO ride_declines (ride_id, from_hospital_id, to_hospital_id, declined_by, reason, created_at)
	           VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, now())
	           ON CONFLICT (ride_id) DO UPDATE SET from_hospital_id=EXCLUDED.from_hospital_id,
	             to_hospital_id=EXCLUDED.to_hospital_id, declined_by=EXCLUDED.declined_by,
	             reason=EXCLUDED.reason, created_at=now()
	           RETURNING ride_id::text, from_hospital_id::text, to_hospital_id::text, declined_by, reason, created_at`
	row := s.db.QueryRow(ctx, q, rideID, fromHospitalID, toHID, declinedBy, reason)
	var rec DeclineRecord
	var toIDNS *string
	if err := row.Scan(&rec.RideID, &rec.FromHospitalID, &toIDNS, &rec.DeclinedBy, &rec.Reason, &rec.CreatedAt); err != nil {
		return nil, err
	}
	rec.ToHospitalID = toIDNS
	return &rec, nil
}

// DeleteReadiness clears the old desk's ack row so a stale ack never leaks
// onto the new hospital's board (readiness is keyed by ride_id only).
func (s *Store) DeleteReadiness(ctx context.Context, rideID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM ride_acknowledgements WHERE ride_id=$1::uuid`, rideID)
	return err
}

// ReassignRideHospital atomically moves a ride to another hospital and
// rewrites its destination to that hospital's location. Guarded by the
// expected current hospital so concurrent declines/reroutes can't
// double-move the ride: 0 rows affected means it already moved.
func (s *Store) ReassignRideHospital(ctx context.Context, rideID, expectedFromHospitalID, newHospitalID string, newDrop GeoJSONPoint, newDropAddress string) error {
	dropJSON, err := json.Marshal(newDrop)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx,
		`UPDATE rides SET hospital_id=$2::uuid, drop=$3::jsonb, drop_address=$4 WHERE id=$1::uuid AND hospital_id=$5::uuid`,
		rideID, newHospitalID, dropJSON, newDropAddress, expectedFromHospitalID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("ride already moved or not owned by this hospital")
	}
	return nil
}
