package location

import (
	"time"

	"ambigo-backend/internal/logger"
)

// StartCleanupWorker runs a background goroutine that sweeps the MemoryStore
// every 15 seconds and removes drivers who haven't pinged in over 15 seconds.
func (s *MemoryStore) StartCleanupWorker() {
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for {
			<-ticker.C
			s.cleanupStaleDrivers()
		}
	}()
}

func (s *MemoryStore) cleanupStaleDrivers() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	timeout := 15 * time.Second
	removedCount := 0

	for driverID, driver := range s.drivers {
		if now.Sub(driver.LastUpdated) > timeout {
			delete(s.cells[driver.H3Cell], driverID)
			delete(s.drivers, driverID)
			removedCount++
		}
	}

	if removedCount > 0 {
		logger.Log.Info().Int("count", removedCount).Msg("Cleaned up stale drivers")
	}
}
