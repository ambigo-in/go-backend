package dispatch

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"

	"ambigo-backend/interfaces"
	"ambigo-backend/internal/location"
	"ambigo-backend/internal/logger"
)

// haversineKm returns the great-circle distance between two points in kilometers.
// Used to cheaply sort candidates before expensive Google Routes calls.
func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const R = 6371.0 // Earth radius in km
	dLat := (lat2 - lat1) * math.Pi / 180.0
	dLng := (lng2 - lng1) * math.Pi / 180.0
	lat1Rad := lat1 * math.Pi / 180.0
	lat2Rad := lat2 * math.Pi / 180.0
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1Rad)*math.Cos(lat2Rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return R * c
}

type Candidate struct {
	DriverID        string
	ETASeconds      int
	DistanceKm      float64
	EncodedPolyline string
}

type Matcher struct {
	LocStore     *location.MemoryStore
	RouteCli     *RouteClient
	AmbTypeNames map[string]string // amb_type_id → display name

	// HospitalDeps resolves MD-registered drivers for hospital-first dispatch.
	// Nil-safe: when unset, FindBestHospitalDriver reports no candidate.
	Hospitals hospitalDriverSource
	Drivers   driverIDResolver
}

// hospitalDriverSource lists mobiles with an active link under a hospital.
type hospitalDriverSource interface {
	ListActiveDriverMobilesByHospital(ctx context.Context, hospitalID string) ([]string, error)
}

// driverIDResolver maps mobiles to verified driver IDs.
type driverIDResolver interface {
	FindDriverIDsByMobiles(ctx context.Context, mobiles []string) (map[string]string, error)
}

// HospitalFirstRadiusKm bounds hospital-first offers to drivers near pickup.
const HospitalFirstRadiusKm = 10.0

// SetHospitalDeps wires hospital-first dispatch (called from main after stores exist).
func (m *Matcher) SetHospitalDeps(h hospitalDriverSource, d driverIDResolver) {
	m.Hospitals = h
	m.Drivers = d
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
	{9, 1},   // ~0.5  km
	{9, 10},  // ~3    km
	{9, 32},  // ~10   km
	{9, 64},  // ~20   km
	{9, 128}, // ~40   km
}

// FindAvailableOtherTypes searches for available drivers of OTHER ambulance types
// near the given location (using the widest ring ~40km). Returns the display names
// of ambulance types that have available drivers, excluding the requested type.
func (m *Matcher) FindAvailableOtherTypes(pickupLat, pickupLng float64, excludeAmbTypeID string) []string {
	lastStep := expansionPlan[len(expansionPlan)-1]
	originCell := location.GetH3CellAtResolution(pickupLat, pickupLng, lastStep.resolution)
	if originCell == "" {
		logger.Log.Warn().Float64("lat", pickupLat).Float64("lng", pickupLng).Msg("FindAvailableOtherTypes: originCell empty")
		return nil
	}
	searchCells, err := location.GetNeighborCellsAtRing(originCell, lastStep.ring)
	if err != nil {
		logger.Log.Warn().Err(err).Str("origin", originCell).Int("ring", lastStep.ring).Msg("FindAvailableOtherTypes: GetNeighborCellsAtRing failed")
		return nil
	}
	logger.Log.Debug().Str("origin", originCell).Int("cells", len(searchCells)).Msg("FindAvailableOtherTypes: searching cells")
	driverIDs, err := m.LocStore.GetDriversInCells(searchCells)
	if err != nil {
		logger.Log.Warn().Err(err).Msg("FindAvailableOtherTypes: GetDriversInCells failed")
		return nil
	}
	if len(driverIDs) == 0 {
		logger.Log.Debug().Str("origin", originCell).Int("cells", len(searchCells)).Msg("FindAvailableOtherTypes: no drivers found in any cell")
		return nil
	}
	logger.Log.Debug().Str("origin", originCell).Int("drivers_found", len(driverIDs)).Msg("FindAvailableOtherTypes: drivers found in cells")

	seen := make(map[string]bool)
	var names []string
	for _, driverID := range driverIDs {
		status, err := m.LocStore.GetDriverStatus(driverID)
		if err != nil {
			logger.Log.Debug().Str("driver_id", driverID).Err(err).Msg("FindAvailableOtherTypes: GetDriverStatus error")
			continue
		}
		if status != interfaces.StatusAvailable {
			logger.Log.Debug().Str("driver_id", driverID).Str("status", string(status)).Msg("FindAvailableOtherTypes: driver not available")
			continue
		}
		vType, err := m.LocStore.GetDriverVehicleType(driverID)
		if err != nil {
			logger.Log.Debug().Str("driver_id", driverID).Err(err).Msg("FindAvailableOtherTypes: GetDriverVehicleType error")
			continue
		}
		if vType == "" {
			logger.Log.Debug().Str("driver_id", driverID).Msg("FindAvailableOtherTypes: driver has empty vehicle type")
			continue
		}
		if vType == excludeAmbTypeID {
			logger.Log.Debug().Str("driver_id", driverID).Str("vtype", vType).Msg("FindAvailableOtherTypes: driver has excluded type")
			continue
		}
		if seen[vType] {
			continue
		}
		seen[vType] = true
		if name, ok := m.AmbTypeNames[vType]; ok {
			names = append(names, name)
			logger.Log.Debug().Str("driver_id", driverID).Str("vtype", vType).Str("name", name).Msg("FindAvailableOtherTypes: added available type")
		} else {
			logger.Log.Debug().Str("driver_id", driverID).Str("vtype", vType).Msg("FindAvailableOtherTypes: vtype not in AmbTypeNames map")
		}
	}
	logger.Log.Debug().Int("types_found", len(names)).Strs("names", names).Msg("FindAvailableOtherTypes: result")
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

	// Cap to 10 closest by haversine BEFORE expensive Google Routes calls.
	// Previously every driver in the ring triggered a Google call, then capped to 5 after.
	// In dense Hyderabad (100 drivers in k=1 ring) that was 100 HTTP calls per ride → 429s.
	// Now: cheap haversine sort → take 10 → only 10 Google calls → keep top 5 by real ETA.
	if len(available) > 10 {
		sort.Slice(available, func(i, j int) bool {
			return haversineKm(available[i].lat, available[i].lng, pickupLat, pickupLng) < haversineKm(available[j].lat, available[j].lng, pickupLat, pickupLng)
		})
		available = available[:10]
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

// FindBestHospitalDriver returns the single nearest AVAILABLE driver registered
// under the given hospital within HospitalFirstRadiusKm of pickup.
// It respects ambTypeID when non-empty and reports ok=false on any miss
// (no hospital, no active links, none available/nearby), letting the caller
// fall through to the normal flow.
func (m *Matcher) FindBestHospitalDriver(ctx context.Context, hospitalID string, pickupLat, pickupLng float64, ambTypeID string) (Candidate, bool) {
	var none Candidate
	if m.Hospitals == nil || m.Drivers == nil || hospitalID == "" {
		return none, false
	}
	mobiles, err := m.Hospitals.ListActiveDriverMobilesByHospital(ctx, hospitalID)
	if err != nil || len(mobiles) == 0 {
		return none, false
	}
	idByMobile, err := m.Drivers.FindDriverIDsByMobiles(ctx, mobiles)
	if err != nil || len(idByMobile) == 0 {
		return none, false
	}

	type scored struct {
		id  string
		km  float64
		lat float64
		lng float64
	}
	var scoredList []scored
	for _, driverID := range idByMobile {
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
		lat, lng, err := m.LocStore.GetLocation(driverID)
		if err != nil {
			continue
		}
		km := haversineKm(lat, lng, pickupLat, pickupLng)
		if km > HospitalFirstRadiusKm {
			continue
		}
		scoredList = append(scoredList, scored{id: driverID, km: km, lat: lat, lng: lng})
	}
	if len(scoredList) == 0 {
		return none, false
	}
	sort.Slice(scoredList, func(i, j int) bool { return scoredList[i].km < scoredList[j].km })

	// Nearest-first with ETA: one Google call for the best case, fall back
	// down the list only if ETA resolution fails.
	for _, s := range scoredList {
		route, err := m.RouteCli.CalculateETA(ctx, s.lat, s.lng, pickupLat, pickupLng)
		if err != nil {
			continue
		}
		return Candidate{
			DriverID:        s.id,
			ETASeconds:      route.DurationSeconds,
			DistanceKm:      route.DistanceKm,
			EncodedPolyline: route.Polyline,
		}, true
	}
	return none, false
}
