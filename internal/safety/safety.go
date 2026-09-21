package safety

import (
	"strings"
	"time"
)

// Two-stage stopped-vehicle thresholds for normal (non-SOS) IN_PROGRESS rides.
// Stage 1 (warn driver) at 3 minutes, Stage 2 (escalate to admin) at 5 minutes total.
const (
	WarnAfter     = 3 * time.Minute
	EscalateAfter = 5 * time.Minute
	// MoveThresholdM resets the stopped timer when the ambulance moves beyond this.
	MoveThresholdM = 50.0
)

// IsExcludedVehicleType reports whether an ambulance type name should be
// excluded from stopped-vehicle monitoring (non-medical transport).
// Matches the Book-Any exclusion (Auto Riksha, Car Cab) plus Bike,
// using case-insensitive substring matching so variants like
// "Bike Ambulance" or "Auto Rickshaw" are covered without schema changes.
func IsExcludedVehicleType(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	for _, sub := range []string{"auto", "bike", "cab", "riksha", "rickshaw"} {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}
