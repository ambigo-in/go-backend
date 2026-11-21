package safety

import (
	"testing"
	"time"
)

func TestIsExcludedVehicleType(t *testing.T) {
	excluded := []string{"Auto Riksha", "Car Cab", "Bike Ambulance", "AUTO", "cab", "bike", "Rickshaw"}
	for _, n := range excluded {
		if !IsExcludedVehicleType(n) {
			t.Errorf("expected excluded: %q", n)
		}
	}
	allowed := []string{"ALS Ambulance", "BLS Ambulance", "PT", "Patient Transport", "Mortuary Van", ""}
	_ = allowed
	for _, n := range []string{"ALS Ambulance", "BLS Ambulance", "PT", "Patient Transport"} {
		if IsExcludedVehicleType(n) {
			t.Errorf("expected allowed: %q", n)
		}
	}
}

func TestTrackerWarnThenEscalate(t *testing.T) {
	tr := NewTrackerWithThresholds(3*time.Minute, 5*time.Minute, 50)
	base := time.Now()
	if a, _ := tr.Check("d1", "r1", 17.0, 78.0, base); a != ActionNone {
		t.Fatalf("expected none on first ping, got %v", a)
	}
	if a, _ := tr.Check("d1", "r1", 17.0, 78.0, base.Add(2*time.Minute)); a != ActionNone {
		t.Fatalf("expected none before warn, got %v", a)
	}
	if a, _ := tr.Check("d1", "r1", 17.0, 78.0, base.Add(3*time.Minute)); a != ActionWarn {
		t.Fatalf("expected warn at 3min, got %v", a)
	}
	// Warn fires once.
	if a, _ := tr.Check("d1", "r1", 17.0, 78.0, base.Add(4*time.Minute)); a != ActionNone {
		t.Fatalf("expected none between warn and escalate, got %v", a)
	}
	if a, _ := tr.Check("d1", "r1", 17.0, 78.0, base.Add(5*time.Minute)); a != ActionEscalate {
		t.Fatalf("expected escalate at 5min, got %v", a)
	}
	// Escalate fires once.
	if a, _ := tr.Check("d1", "r1", 17.0, 78.0, base.Add(9*time.Minute)); a != ActionNone {
		t.Fatalf("expected none after escalate, got %v", a)
	}
}

func TestTrackerMovementResets(t *testing.T) {
	tr := NewTrackerWithThresholds(3*time.Minute, 5*time.Minute, 50)
	base := time.Now()
	tr.Check("d1", "r1", 17.0, 78.0, base)
	// ~111m north resets the anchor.
	if a, _ := tr.Check("d1", "r1", 17.001, 78.0, base.Add(4*time.Minute)); a != ActionNone {
		t.Fatalf("expected reset on movement, got %v", a)
	}
	if a, _ := tr.Check("d1", "r1", 17.001, 78.0, base.Add(6*time.Minute)); a != ActionNone {
		t.Fatalf("expected none 2min after reset, got %v", a)
	}
	if a, _ := tr.Check("d1", "r1", 17.001, 78.0, base.Add(7*time.Minute)); a != ActionWarn {
		t.Fatalf("expected warn 3min after reset, got %v", a)
	}
}

func TestTrackerRideChangeResets(t *testing.T) {
	tr := NewTrackerWithThresholds(time.Minute, 2*time.Minute, 50)
	base := time.Now()
	tr.Check("d1", "r1", 17.0, 78.0, base)
	if a, _ := tr.Check("d1", "r2", 17.0, 78.0, base.Add(90*time.Second)); a != ActionNone {
		t.Fatalf("expected reset on ride change, got %v", a)
	}
	tr.Reset("d1")
	if a, _ := tr.Check("d1", "r2", 17.0, 78.0, base.Add(100*time.Second)); a != ActionNone {
		t.Fatalf("expected none after explicit reset, got %v", a)
	}
}

func TestTrackerRestorePreservesElapsedAndStage(t *testing.T) {
	tr := NewTrackerWithThresholds(3*time.Minute, 5*time.Minute, 50)
	base := time.Now()
	tr.Restore([]VehicleState{{
		RideID: "r1", DriverID: "d1", AnchorLat: 17.0, AnchorLng: 78.0,
		StoppedSince: base, Warned: true,
	}})

	action, elapsed, state, changed := tr.CheckState("d1", "r1", 17.0, 78.0, base.Add(4*time.Minute))
	if action != ActionNone || changed || elapsed != 4*time.Minute || !state.Warned {
		t.Fatalf("restored warning state was not preserved: action=%v elapsed=%v changed=%v state=%+v", action, elapsed, changed, state)
	}

	action, elapsed, state, changed = tr.CheckState("d1", "r1", 17.0, 78.0, base.Add(5*time.Minute))
	if action != ActionEscalate || !changed || elapsed != 5*time.Minute || !state.Escalated {
		t.Fatalf("restored timer did not escalate on schedule: action=%v elapsed=%v changed=%v state=%+v", action, elapsed, changed, state)
	}
	view := state.View(base.Add(5 * time.Minute))
	if view.Stage != "emergency" || view.StoppedSeconds != 300 {
		t.Fatalf("unexpected state view: %+v", view)
	}
}

func TestTrackerCheckStateMarksOnlyPersistentChanges(t *testing.T) {
	tr := NewTrackerWithThresholds(3*time.Minute, 5*time.Minute, 50)
	base := time.Now()

	_, _, _, changed := tr.CheckState("d1", "r1", 17.0, 78.0, base)
	if !changed {
		t.Fatal("new anchor must be persisted")
	}
	_, _, _, changed = tr.CheckState("d1", "r1", 17.0, 78.0, base.Add(time.Minute))
	if changed {
		t.Fatal("stationary ping before a stage change must not be persisted")
	}
	_, _, moved, changed := tr.CheckState("d1", "r1", 17.001, 78.0, base.Add(2*time.Minute))
	if !changed || moved.StoppedSince != base.Add(2*time.Minute) {
		t.Fatalf("movement reset must be persisted: changed=%v state=%+v", changed, moved)
	}
}
