package dispatch

import (
	"context"
	"errors"
	"sort"
	"sync"

	"ambigo-backend/interfaces"
	"ambigo-backend/internal/location"
	"ambigo-backend/internal/logger"
)

type Candidate struct {
	DriverID        string
	ETASeconds      int
	DistanceKm      float64
	EncodedPolyline string
}

type Matcher struct {
	LocStore    *location.MemoryStore
	RouteCli    *RouteClient
	AmbTypeNames map[string]string // amb_type_id → display name
}

func NewMatcher(ls *location.MemoryStore, rc *RouteClient, ambTypeNames map[string]string) *Matcher {
	return &Matcher{
		LocStore:     ls,
		RouteCli:     rc,
		AmbTypeNames: ambTypeNames,
	}
}

// expansionStep defines a single H3 resolution and ring to search.
type expansionStep struct {
	resolution int
	ring       int
}

// expansionPlan defines the progressive search radii for ambulance dispatch.
// Each step searches a wider area until drivers are found.
var expansionPlan = []expansionStep{
	{9, 1}, // Res 9 ring 1  ~0.5 km
	{7, 1}, // Res 7 ring 1  ~3   km
	{6, 2}, // Res 6 ring 2  ~15  km
	{5, 2}, // Res 5 ring 2  ~40  km
}

// FindAvailableOtherTypes searches for available drivers of OTHER ambulance types
// near the given location (using the widest ring ~40km). Returns the display names
// of ambulance types that have available drivers, excluding the requested type.
func (m *Matcher) FindAvailableOtherTypes(pickupLat, pickupLng float64, excludeAmbTypeID string) []string {
	lastStep := expansionPlan[len(expansionPlan)-1]
	originCell := location.GetH3CellAtResolution(pickupLat, pickupLng, lastStep.resolution)
	if originCell == "" {
		return nil
	}
	searchCells, err := location.GetNeighborCellsAtRing(originCell, lastStep.ring)
	if err != nil {
		return nil
	}
	driverIDs, err := m.LocStore.GetDriversInCells(searchCells)
	if err != nil || len(driverIDs) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	var names []string
	for _, driverID := range driverIDs {
		status, err := m.LocStore.GetDriverStatus(driverID)
		if err != nil || status != interfaces.StatusAvailable {
			continue
		}
		vType, err := m.LocStore.GetDriverVehicleType(driverID)
		if err != nil || vType == "" || vType == excludeAmbTypeID || seen[vType] {
			continue
		}
		seen[vType] = true
		if name, ok := m.AmbTypeNames[vType]; ok {
			names = append(names, name)
		}
	}
	return names
}

// FindBestDrivers takes the pickup coordinates and progressively searches wider
// areas for available drivers, sorted by real driving ETA. If no drivers are found
// at any radius, returns an error (caller cancels the ride).
// If ambTypeID is non-empty, only drivers with matching vehicle_type are considered.
func (m *Matcher) FindBestDrivers(ctx context.Context, pickupLat, pickupLng float64, maxCandidates int, ambTypeID string) ([]Candidate, error) {
	type driverInfo struct {
		id  string
		lat float64
		lng float64
	}

	var available []driverInfo

	for _, step := range expansionPlan {
		originCell := location.GetH3CellAtResolution(pickupLat, pickupLng, step.resolution)
		if originCell == "" {
			continue
		}

		searchCells, err := location.GetNeighborCellsAtRing(originCell, step.ring)
		if err != nil {
			continue
		}

		driverIDs, err := m.LocStore.GetDriversInCells(searchCells)
		if err != nil {
			continue
		}

		if len(driverIDs) == 0 {
			continue
		}

		for _, driverID := range driverIDs {
			status, err := m.LocStore.GetDriverStatus(driverID)
			if err != nil || status != interfaces.StatusAvailable {
				continue
			}
			if ambTypeID != "" {
				vType, err := m.LocStore.GetDriverVehicleType(driverID)
				if err != nil || vType != ambTypeID {
					continue
				}
			}
			driverLat, driverLng, err := m.LocStore.GetLocation(driverID)
			if err != nil {
				continue
			}
			available = append(available, driverInfo{id: driverID, lat: driverLat, lng: driverLng})
		}

		if len(available) > 0 {
			break
		}
	}

	if len(available) == 0 {
		return nil, errors.New("no drivers found in the vicinity")
	}

	var candidates []Candidate
	var mu sync.Mutex
	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup

	for _, d := range available {
		wg.Add(1)
		go func(d driverInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			route, err := m.RouteCli.CalculateETA(ctx, d.lat, d.lng, pickupLat, pickupLng)
			if err != nil {
				l := logger.Ctx(ctx)
			l.Error().Err(err).Str("driver_id", d.id).Msg("Error getting ETA for driver")
				return
			}

			mu.Lock()
			candidates = append(candidates, Candidate{
				DriverID:        d.id,
				ETASeconds:      route.DurationSeconds,
				DistanceKm:      route.DistanceKm,
				EncodedPolyline: route.Polyline,
			})
			mu.Unlock()
		}(d)
	}

	wg.Wait()

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ETASeconds < candidates[j].ETASeconds
	})

	if len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}

	return candidates, nil
}
