package dispatch

import (
	"ambigo-backend/interfaces"
	"ambigo-backend/internal/location"
	"context"
	"errors"
	"log"
	"sort"
	"sync"
)

type Candidate struct {
	DriverID        string
	ETASeconds      int
	DistanceKm      float64
	EncodedPolyline string
}

type Matcher struct {
	LocStore *location.MemoryStore
	RouteCli *RouteClient
}

func NewMatcher(ls *location.MemoryStore, rc *RouteClient) *Matcher {
	return &Matcher{
		LocStore: ls,
		RouteCli: rc,
	}
}

// FindBestDrivers takes the pickup coordinates and finds the absolute closest
// AVAILABLE drivers, sorted completely by real driving ETA (not straight line distance).
// If ambTypeID is non-empty, only drivers with matching vehicle_type are considered.
func (m *Matcher) FindBestDrivers(ctx context.Context, pickupLat, pickupLng float64, maxCandidates int, ambTypeID string) ([]Candidate, error) {
	originCell := location.GetH3Cell(pickupLat, pickupLng)

	// 2. Expand search radius to 1-ring (Origin + 6 immediate neighbors)
	searchCells, err := location.GetNeighborCells(originCell)
	if err != nil {
		return nil, err
	}

	// 3. Get all drivers in those 7 cells
	driverIDs, err := m.LocStore.GetDriversInCells(searchCells)
	if err != nil {
		return nil, err
	}

	if len(driverIDs) == 0 {
		return nil, errors.New("no drivers found in the vicinity")
	}

	type driverInfo struct {
		id  string
		lat float64
		lng float64
	}

	var available []driverInfo
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

	if len(available) == 0 {
		return nil, errors.New("no drivers found in the vicinity")
	}

	// 4. Call Google Routes API in parallel (max 10 concurrent)
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
				log.Printf("[Matcher] Error getting ETA for driver %s: %v", d.id, err)
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

	// 5. Sort the candidates by lowest ETA
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ETASeconds < candidates[j].ETASeconds
	})

	// 6. Return up to the maximum requested candidates
	if len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}

	return candidates, nil
}
