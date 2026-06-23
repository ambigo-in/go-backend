package ride

import "errors"

// ValidateTransition enforces the strict ride lifecycle rules
// defined in the SRS Phase 0 specification.
func ValidateTransition(current RideStatus, next RideStatus) error {
	switch current {
	case StatusSearching:
		if next == StatusAssigned || next == StatusCancelled {
			return nil
		}
	case StatusAssigned:
		if next == StatusArrived || next == StatusCancelled {
			return nil
		}
	case StatusArrived:
		if next == StatusInProgress {
			return nil
		}
	case StatusInProgress:
		if next == StatusCompleted {
			return nil
		}
	case StatusCompleted, StatusCancelled:
		// Terminal states cannot transition to anything else
		return errors.New("cannot transition from a terminal state")
	}

	return errors.New("illegal state transition: " + string(current) + " -> " + string(next))
}
