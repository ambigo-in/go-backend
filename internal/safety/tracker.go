package safety

import (
	"math"
	"sync"
	"time"
)

// Action is the stopped-vehicle stage to fire for a driver ping.
type Action int

const (
	ActionNone Action = iota
	ActionWarn
	ActionEscalate
)

// HaversineMeters returns the great-circle distance in meters.
func HaversineMeters(lat1, lng1, lat2, lng2 float64) float64 {
	const R = 6371000.0
	dLat := (lat2 - lat1) * math.Pi / 180.0
	dLng := (lng2 - lng1) * math.Pi / 180.0
	lat1Rad := lat1 * math.Pi / 180.0
	lat2Rad := lat2 * math.Pi / 180.0
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1Rad)*math.Cos(lat2Rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return R * c
}

type state struct {
	rideID     string
	anchorLat  float64
	anchorLng  float64
	anchorTime time.Time
	warned     bool
	escalated  bool
}

// Tracker keeps per-driver stopped state in memory. It is safe for
// concurrent use from the websocket location handler.
type Tracker struct {
	mu            sync.Mutex
	states        map[string]*state
	warnAfter     time.Duration
	escalateAfter time.Duration
	moveThreshold float64
}

// NewTracker returns a Tracker with production thresholds.
func NewTracker() *Tracker {
	return &Tracker{
		states:        make(map[string]*state),
		warnAfter:     WarnAfter,
		escalateAfter: EscalateAfter,
		moveThreshold: MoveThresholdM,
	}
}

// NewTrackerWithThresholds is used by tests to avoid waiting minutes.
func NewTrackerWithThresholds(warnAfter, escalateAfter time.Duration, moveThresholdM float64) *Tracker {
	return &Tracker{
		states:        make(map[string]*state),
		warnAfter:     warnAfter,
		escalateAfter: escalateAfter,
		moveThreshold: moveThresholdM,
	}
}

func (t *Tracker) Restore(states []VehicleState) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, persisted := range states {
		if persisted.DriverID == "" || persisted.RideID == "" || persisted.StoppedSince.IsZero() {
			continue
		}
		t.states[persisted.DriverID] = &state{
			rideID: persisted.RideID, anchorLat: persisted.AnchorLat, anchorLng: persisted.AnchorLng,
			anchorTime: persisted.StoppedSince, warned: persisted.Warned, escalated: persisted.Escalated,
		}
	}
}

func (t *Tracker) Reset(driverID string) {
	t.ResetState(driverID)
}

func (t *Tracker) ResetState(driverID string) bool {
	if t == nil || driverID == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.states[driverID]; !exists {
		return false
	}
	delete(t.states, driverID)
	return true
}

func (t *Tracker) Snapshot(driverID string, now time.Time) (VehicleState, bool) {
	if t == nil || driverID == "" {
		return VehicleState{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st, exists := t.states[driverID]
	if !exists {
		return VehicleState{}, false
	}
	return stateSnapshot(driverID, st, now), true
}

func (t *Tracker) Check(driverID, rideID string, lat, lng float64, now time.Time) (Action, time.Duration) {
	action, elapsed, _, _ := t.CheckState(driverID, rideID, lat, lng, now)
	return action, elapsed
}

func (t *Tracker) CheckState(driverID, rideID string, lat, lng float64, now time.Time) (Action, time.Duration, VehicleState, bool) {
	if t == nil || driverID == "" || rideID == "" {
		return ActionNone, 0, VehicleState{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	st, ok := t.states[driverID]
	if !ok || st.rideID != rideID {
		st = &state{rideID: rideID, anchorLat: lat, anchorLng: lng, anchorTime: now}
		t.states[driverID] = st
		return ActionNone, 0, stateSnapshot(driverID, st, now), true
	}
	if st.escalated {
		return ActionNone, now.Sub(st.anchorTime), stateSnapshot(driverID, st, now), false
	}
	if HaversineMeters(st.anchorLat, st.anchorLng, lat, lng) > t.moveThreshold {
		st.anchorLat, st.anchorLng, st.anchorTime = lat, lng, now
		st.warned = false
		return ActionNone, 0, stateSnapshot(driverID, st, now), true
	}
	elapsed := now.Sub(st.anchorTime)
	if !st.warned {
		if elapsed >= t.warnAfter {
			st.warned = true
			return ActionWarn, elapsed, stateSnapshot(driverID, st, now), true
		}
		return ActionNone, elapsed, stateSnapshot(driverID, st, now), false
	}
	if elapsed >= t.escalateAfter {
		st.escalated = true
		return ActionEscalate, elapsed, stateSnapshot(driverID, st, now), true
	}
	return ActionNone, elapsed, stateSnapshot(driverID, st, now), false
}

func stateSnapshot(driverID string, st *state, now time.Time) VehicleState {
	return VehicleState{
		RideID: st.rideID, DriverID: driverID,
		AnchorLat: st.anchorLat, AnchorLng: st.anchorLng,
		StoppedSince: st.anchorTime, Warned: st.warned, Escalated: st.escalated,
		UpdatedAt: now,
	}
}
