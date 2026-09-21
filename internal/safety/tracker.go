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

// Reset clears state for a driver (ride ended, ride changed, or ineligible).
func (t *Tracker) Reset(driverID string) {
	if t == nil || driverID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.states, driverID)
}

// Check records a ping and reports which stage (if any) should fire.
// Movement beyond the threshold resets the anchor and warn flag (one warn
// per continuous stop), while escalated sticks for the ride lifetime.
func (t *Tracker) Check(driverID, rideID string, lat, lng float64, now time.Time) (Action, time.Duration) {
	if t == nil || driverID == "" || rideID == "" {
		return ActionNone, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	st, ok := t.states[driverID]
	if !ok || st.rideID != rideID {
		t.states[driverID] = &state{rideID: rideID, anchorLat: lat, anchorLng: lng, anchorTime: now}
		return ActionNone, 0
	}
	if st.escalated {
		return ActionNone, 0
	}
	if HaversineMeters(st.anchorLat, st.anchorLng, lat, lng) > t.moveThreshold {
		st.anchorLat, st.anchorLng, st.anchorTime = lat, lng, now
		st.warned = false
		return ActionNone, 0
	}
	elapsed := now.Sub(st.anchorTime)
	if !st.warned {
		if elapsed >= t.warnAfter {
			st.warned = true
			return ActionWarn, elapsed
		}
		return ActionNone, 0
	}
	if elapsed >= t.escalateAfter {
		st.escalated = true
		return ActionEscalate, elapsed
	}
	return ActionNone, 0
}
