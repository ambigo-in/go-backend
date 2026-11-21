package location

import (
	"ambigo-backend/interfaces"
	"errors"
	"sort"
	"sync"
	"time"
)

type DriverLocation struct {
	DriverID    string                  `json:"driver_id"`
	DriverName  string                  `json:"driver_name,omitempty"`
	Lat         float64                 `json:"lat"`
	Lng         float64                 `json:"lng"`
	H3Cell      string                  `json:"-"`
	Status      interfaces.DriverStatus `json:"status"`
	LastUpdated time.Time               `json:"last_updated"`
	VehicleType string                  `json:"vehicle_type,omitempty"`
	RideID      string                  `json:"ride_id,omitempty"`
}

// MemoryStore implements interfaces.LocationStore using sync.RWMutex
// for ultra-fast $O(1)$ memory lookups without hitting a database.
type MemoryStore struct {
	mu      sync.RWMutex
	drivers map[string]*DriverLocation
	cells   map[string]map[string]bool // map[h3_cell]set[driver_id]
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		drivers: make(map[string]*DriverLocation),
		cells:   make(map[string]map[string]bool),
	}
}

func (s *MemoryStore) UpdateLocation(driverID string, lat float64, lng float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	newCell := GetH3Cell(lat, lng)

	// Check if driver already exists
	driver, exists := s.drivers[driverID]
	if exists {
		// If the driver moved to a new cell, remove them from the old cell set
		if driver.H3Cell != newCell {
			delete(s.cells[driver.H3Cell], driverID)
		}
		driver.Lat = lat
		driver.Lng = lng
		driver.H3Cell = newCell
		driver.LastUpdated = time.Now()
	} else {
		// New driver tracking
		s.drivers[driverID] = &DriverLocation{
			DriverID:    driverID,
			Lat:         lat,
			Lng:         lng,
			H3Cell:      newCell,
			Status:      interfaces.StatusAvailable, // Default to available
			LastUpdated: time.Now(),
		}
	}

	// Add driver to the new cell set
	if s.cells[newCell] == nil {
		s.cells[newCell] = make(map[string]bool)
	}
	s.cells[newCell][driverID] = true

	return nil
}

func (s *MemoryStore) GetLocation(driverID string) (float64, float64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	driver, exists := s.drivers[driverID]
	if !exists {
		return 0, 0, errors.New("driver not found in memory store")
	}

	return driver.Lat, driver.Lng, nil
}

func (s *MemoryStore) GetDriversInCell(h3Cell string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	driverSet := s.cells[h3Cell]
	drivers := make([]string, 0, len(driverSet))
	for driverID := range driverSet {
		drivers = append(drivers, driverID)
	}

	return drivers, nil
}

func (s *MemoryStore) GetDriversInCells(h3Cells []string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []string
	seen := make(map[string]bool)

	for _, cell := range h3Cells {
		driverSet := s.cells[cell]
		for driverID := range driverSet {
			if !seen[driverID] {
				result = append(result, driverID)
				seen[driverID] = true
			}
		}
	}

	return result, nil
}

func (s *MemoryStore) RemoveDriver(driverID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	driver, exists := s.drivers[driverID]
	if !exists {
		return nil
	}

	// Remove from cell mapping
	delete(s.cells[driver.H3Cell], driverID)
	// Remove from main mapping
	delete(s.drivers, driverID)

	return nil
}

func (s *MemoryStore) OnlineCounts() (available, busy int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	for _, d := range s.drivers {
		if now.Sub(d.LastUpdated) > 15*time.Second {
			continue
		}
		switch d.Status {
		case interfaces.StatusAvailable:
			available++
		case interfaces.StatusBusy:
			busy++
		}
	}
	return available, busy
}

func (s *MemoryStore) SetDriverStatus(driverID string, status interfaces.DriverStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	driver, exists := s.drivers[driverID]
	if !exists {
		return errors.New("driver not found")
	}

	driver.Status = status
	return nil
}

func (s *MemoryStore) GetDriverStatus(driverID string) (interfaces.DriverStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	driver, exists := s.drivers[driverID]
	if !exists {
		return "", errors.New("driver not found")
	}

	return driver.Status, nil
}

func (s *MemoryStore) SetDriverVehicleType(driverID, vehicleType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	driver, exists := s.drivers[driverID]
	if !exists {
		return errors.New("driver not found")
	}

	driver.VehicleType = vehicleType
	return nil
}

func (s *MemoryStore) GetDriverVehicleType(driverID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	driver, exists := s.drivers[driverID]
	if !exists {
		return "", errors.New("driver not found")
	}

	return driver.VehicleType, nil
}

func (s *MemoryStore) SetDriverIdentity(driverID, name, vehicleType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	driver, exists := s.drivers[driverID]
	if !exists {
		return errors.New("driver not found")
	}

	driver.DriverName = name
	driver.VehicleType = vehicleType
	return nil
}

func (s *MemoryStore) SetDriverRide(driverID, rideID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	driver, exists := s.drivers[driverID]
	if !exists {
		return errors.New("driver not found")
	}
	driver.RideID = rideID
	return nil
}

func (s *MemoryStore) FleetDriver(driverID string) (DriverLocation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	driver, exists := s.drivers[driverID]
	if !exists || time.Since(driver.LastUpdated) > 15*time.Second {
		return DriverLocation{}, false
	}
	return *driver, true
}

func (s *MemoryStore) FleetSnapshot() []DriverLocation {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	drivers := make([]DriverLocation, 0, len(s.drivers))
	for _, driver := range s.drivers {
		if now.Sub(driver.LastUpdated) <= 15*time.Second {
			drivers = append(drivers, *driver)
		}
	}
	sort.Slice(drivers, func(i, j int) bool {
		return drivers[i].DriverID < drivers[j].DriverID
	})
	return drivers
}
