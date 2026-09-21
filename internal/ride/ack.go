package ride

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Readiness is the desk staff's per-ride acknowledge: who marked the inbound
// patient as handled, and when. One row per ride, written only by the ride's
// own hospital staff. The ack silences that ride's board alarm and survives
// refresh and shift change.
type Readiness struct {
	RideID     string     `json:"ride_id"`
	HospitalID string     `json:"hospital_id"`
	AckedBy    string     `json:"acked_by,omitempty"`
	AckedAt    *time.Time `json:"acked_at,omitempty"`
}

func scanReadiness(row pgx.Row) (*Readiness, error) {
	var rd Readiness
	var ackedByNS *string
	var ackedAt *time.Time
	if err := row.Scan(&rd.RideID, &rd.HospitalID, &ackedByNS, &ackedAt); err != nil {
		return nil, err
	}
	if ackedByNS != nil {
		rd.AckedBy = *ackedByNS
	}
	rd.AckedAt = ackedAt
	return &rd, nil
}

// UpsertReadiness records the acknowledge for a ride (idempotent re-acks just
// refresh the timestamp).
func (s *Store) UpsertReadiness(ctx context.Context, rideID, hospitalID, ackedBy string) (*Readiness, error) {
	const q = `INSERT INTO ride_acknowledgements (ride_id, hospital_id, acked_by, acked_at, updated_at)
	           VALUES ($1::uuid, $2::uuid, $3, now(), now())
	           ON CONFLICT (ride_id) DO UPDATE SET hospital_id=EXCLUDED.hospital_id, acked_by=EXCLUDED.acked_by,
	             acked_at=EXCLUDED.acked_at, updated_at=now()
	           RETURNING ride_id::text, hospital_id::text, acked_by, acked_at`
	row := s.db.QueryRow(ctx, q, rideID, hospitalID, ackedBy)
	return scanReadiness(row)
}

// GetReadinessForRides batches readiness rows for incoming-list enrichment.
func (s *Store) GetReadinessForRides(ctx context.Context, rideIDs []string) (map[string]*Readiness, error) {
	out := make(map[string]*Readiness, len(rideIDs))
	if len(rideIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT ride_id::text, hospital_id::text, acked_by, acked_at FROM ride_acknowledgements WHERE ride_id = ANY($1::uuid[])`, rideIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		rd, err := scanReadiness(rows)
		if err != nil {
			return nil, err
		}
		out[rd.RideID] = rd
	}
	return out, rows.Err()
}
