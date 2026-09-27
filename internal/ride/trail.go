package ride

import (
	"context"
	"math"
	"time"

	"ambigo-backend/internal/ids"
)

// TrailPoint is one GPS fix during a trip.
type TrailPoint struct {
	Lat        float64
	Lng        float64
	RecordedAt time.Time
}

// Gated recalc thresholds (Uber-style, simplified for Ambigo).
const (
	// DropGateMeters: final drop within this of planned drop = same place.
	DropGateMeters = 500.0
	// DetourPct: trail must exceed estimate by this fraction...
	DetourPct = 0.20
	// ...AND by this absolute km (protects short trips).
	DetourMinKm = 0.5
	// Trail filters.
	TrailMinMoveM = 10.0
	TrailMaxSpeedKmh = 150.0
	// Fraud ceiling: trail beyond this vs estimate flags review instead of auto-charge.
	DetourCapMult = 2.0
	DetourCapAddKm = 15.0
)

// HaversineKm returns great-circle distance in km.
func HaversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const R = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180.0
	dLng := (lng2 - lng1) * math.Pi / 180.0
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180.0)*math.Cos(lat2*math.Pi/180.0)*
			math.Sin(dLng/2)*math.Sin(dLng/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// InsertTrailPoint stores one fix. Fire-and-forget from WS; errors ignored by caller.
func (s *Store) InsertTrailPoint(ctx context.Context, rideID string, lat, lng float64) error {
	if !ids.IsValid(rideID) {
		return nil
	}
	_, err := s.db.Exec(ctx,
		`INSERT INTO ride_location_trail (id, ride_id, lat, lng, recorded_at) VALUES ($1::uuid, $2::uuid, $3, $4, now())`,
		ids.New(), rideID, lat, lng)
	return err
}

// ListTrail returns ordered fixes for a ride.
func (s *Store) ListTrail(ctx context.Context, rideID string) ([]TrailPoint, error) {
	rows, err := s.db.Query(ctx,
		`SELECT lat, lng, recorded_at FROM ride_location_trail WHERE ride_id=$1::uuid ORDER BY recorded_at ASC`,
		rideID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrailPoint
	for rows.Next() {
		var p TrailPoint
		if err := rows.Scan(&p.Lat, &p.Lng, &p.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if out == nil {
		out = []TrailPoint{}
	}
	return out, rows.Err()
}

// TrailDistanceKm sums filtered haversine segments: drops <10m jitter and
// >150km/h jumps (tunnel exit / spoof).
func TrailDistanceKm(points []TrailPoint) float64 {
	if len(points) < 2 {
		return 0
	}
	total := 0.0
	for i := 1; i < len(points); i++ {
		prev, cur := points[i-1], points[i]
		segM := HaversineKm(prev.Lat, prev.Lng, cur.Lat, cur.Lng) * 1000.0
		if segM < TrailMinMoveM {
			continue
		}
		dt := cur.RecordedAt.Sub(prev.RecordedAt).Hours()
		if dt > 0 {
			if segM/1000.0/dt > TrailMaxSpeedKmh {
				continue
			}
		}
		total += segM / 1000.0
	}
	return total
}

// ShouldRecalc reports whether actual differs enough to drop the upfront fare.
// dropMovedM: haversine(final, planned drop). trailKm: filtered odometer.
// estimateKm: locked route distance.
func ShouldRecalc(dropMovedM, trailKm, estimateKm float64) (bool, string) {
	if dropMovedM >= DropGateMeters {
		return true, "drop_moved"
	}
	if estimateKm <= 0 {
		return trailKm > DetourMinKm, "no_estimate"
	}
	extra := trailKm - estimateKm
	if extra >= DetourMinKm && extra/estimateKm >= DetourPct {
		return true, "detour"
	}
	return false, ""
}

// ExceedsCap reports fraud-level divergence needing manual review.
func ExceedsCap(trailKm, estimateKm float64) bool {
	if estimateKm <= 0 {
		return trailKm > DetourCapAddKm
	}
	return trailKm > estimateKm*DetourCapMult+DetourCapAddKm
}
