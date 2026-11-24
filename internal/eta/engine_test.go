package eta

import (
	"math"
	"testing"
	"time"

	"ambigo-backend/internal/ride"
)

func TestDistToSegment(t *testing.T) {
	// A: (12.0, 77.0), B: (12.0, 78.0)
	// P: (12.1, 77.5) -> projection is (12.0, 77.5)
	d := distToSegmentKm(12.1, 77.5, 12.0, 77.0, 12.0, 78.0)
	expected := HaversineKm(12.1, 77.5, 12.0, 77.5)
	if math.Abs(d-expected) > 0.01 {
		t.Fatalf("distToSegment mismatch: got %f, want ~%f", d, expected)
	}
}

func TestRemainingDistanceAlongPolyline(t *testing.T) {
	// 3 points in a line: P0 -> P1 -> P2
	p0 := [2]float64{12.90, 77.60}
	p1 := [2]float64{12.95, 77.60}
	p2 := [2]float64{13.00, 77.60}
	pts := [][2]float64{p0, p1, p2}

	total := HaversineKm(p0[0], p0[1], p1[0], p1[1]) + HaversineKm(p1[0], p1[1], p2[0], p2[1])

	// Driver at p0
	rem0 := remainingDistanceAlongPolylineKm(p0[0], p0[1], pts)
	if math.Abs(rem0-total) > 0.1 {
		t.Errorf("at p0: got %f, want %f", rem0, total)
	}

	// Driver at p1
	rem1 := remainingDistanceAlongPolylineKm(p1[0], p1[1], pts)
	expected1 := HaversineKm(p1[0], p1[1], p2[0], p2[1])
	if math.Abs(rem1-expected1) > 0.1 {
		t.Errorf("at p1: got %f, want %f", rem1, expected1)
	}

	// Driver at p2 (end)
	rem2 := remainingDistanceAlongPolylineKm(p2[0], p2[1], pts)
	if rem2 > 0.1 {
		t.Errorf("at p2: got %f, want ~0", rem2)
	}
}

func TestDeriveSnapshotAssignedHasPickupETA(t *testing.T) {
	rd := &ride.Ride{
		ID:     "ride1",
		Status: ride.StatusAssigned,
		Pickup: ride.GeoJSONPoint{Type: "Point", Coordinates: []float64{77.60, 12.90}},
		Drop:   ride.GeoJSONPoint{Type: "Point", Coordinates: []float64{77.70, 13.00}},
		Route:  &ride.Route{DistanceKm: 10, DurationSeconds: 900},
	}
	// Driver ~1.5 km north-east of pickup.
	p := deriveSnapshot(rd, "drv1", 12.91, 77.61)
	if p == nil {
		t.Fatal("expected a snapshot for an ASSIGNED ride")
	}
	if p.Stage != "en_route_pickup" {
		t.Fatalf("stage = %q, want en_route_pickup", p.Stage)
	}
	if p.RemainingSecs <= 0 {
		t.Fatalf("remaining_seconds = %d, want > 0", p.RemainingSecs)
	}
	// Hospital ETA must include the pickup leg plus on-scene time and the drop leg.
	wantHospital := p.RemainingSecs + 300 + rd.Route.DurationSeconds
	if p.HospitalETASecs != wantHospital {
		t.Fatalf("hospital_eta_seconds = %d, want %d", p.HospitalETASecs, wantHospital)
	}
}

func TestDeriveSnapshotArrivedIsZeroToPickup(t *testing.T) {
	rd := &ride.Ride{
		ID:     "ride1",
		Status: ride.StatusArrived,
		Pickup: ride.GeoJSONPoint{Type: "Point", Coordinates: []float64{77.60, 12.90}},
		Drop:   ride.GeoJSONPoint{Type: "Point", Coordinates: []float64{77.70, 13.00}},
		Route:  &ride.Route{DistanceKm: 10, DurationSeconds: 900},
	}
	p := deriveSnapshot(rd, "drv1", 12.90, 77.60)
	if p == nil || p.RemainingSecs != 0 {
		t.Fatalf("arrived snapshot = %+v, want remaining 0", p)
	}
	if p.HospitalETASecs != 300+900 {
		t.Fatalf("hospital_eta_seconds = %d, want %d", p.HospitalETASecs, 1200)
	}
}

func TestDeriveSnapshotInProgressTargetsDrop(t *testing.T) {
	rd := &ride.Ride{
		ID:     "ride1",
		Status: ride.StatusInProgress,
		Pickup: ride.GeoJSONPoint{Type: "Point", Coordinates: []float64{77.60, 12.90}},
		Drop:   ride.GeoJSONPoint{Type: "Point", Coordinates: []float64{77.70, 13.00}},
		Route:  &ride.Route{DistanceKm: 10, DurationSeconds: 900},
	}
	// Driver at the pickup: remaining must be the pickup->drop leg, not zero.
	p := deriveSnapshot(rd, "drv1", 12.90, 77.60)
	if p == nil {
		t.Fatal("expected a snapshot for an IN_PROGRESS ride")
	}
	if p.Stage != "en_route_hospital" {
		t.Fatalf("stage = %q, want en_route_hospital", p.Stage)
	}
	if p.RemainingSecs <= 0 || p.RemainingKm <= 0 {
		t.Fatalf("remaining = %ds/%fkm, want > 0", p.RemainingSecs, p.RemainingKm)
	}
}

func TestAntiJitterFilter(t *testing.T) {
	engine := NewETAEngine(nil, nil)

	// Set initial point
	p := engine.OnDriverLocation("ride1", "drv1", 12.90, 77.60, ride.StatusAssigned)
	if p == nil {
		t.Fatal("expected non-nil payload on first ping")
	}

	// Immediate jump > 500m (e.g. 5km jump in 100ms)
	time.Sleep(10 * time.Millisecond)
	pJump := engine.OnDriverLocation("ride1", "drv1", 12.95, 77.60, ride.StatusAssigned)
	if pJump != nil {
		t.Errorf("expected anti-jitter filter to drop 5km jump in 10ms, but got: %+v", pJump)
	}
}
