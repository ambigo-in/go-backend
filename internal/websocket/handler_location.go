package websocket

import (
	"context"
	"encoding/json"
	"time"

	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/ids"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/ride"
	"ambigo-backend/internal/safety"
)

// rideRecoveryInterval bounds how often a driver with no in-memory ride state
// triggers a DB lookup. Idle drivers (no active ride) pay at most one lookup
// per interval instead of one per GPS ping.
const rideRecoveryInterval = 30 * time.Second

// recoverActiveRide rebuilds the driver->ride mapping from persistent state.
// Called when the in-memory map has no entry (backend restart, failed startup
// seed). Returns ("", "") when the driver has no active ride.
func (m *Manager) recoverActiveRide(driverID string) (string, ride.RideStatus) {
	if m.RideStore == nil || driverID == "" {
		return "", ""
	}

	m.rideRecoveryMu.Lock()
	if m.rideRecovery == nil {
		m.rideRecovery = make(map[string]time.Time)
	}
	if last, ok := m.rideRecovery[driverID]; ok && time.Since(last) < rideRecoveryInterval {
		m.rideRecoveryMu.Unlock()
		return "", ""
	}
	m.rideRecovery[driverID] = time.Now()
	m.rideRecoveryMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	active, err := m.RideStore.GetCurrentRide(ctx, driverID, "driver")
	if err != nil || active == nil {
		return "", ""
	}

	m.mu.Lock()
	// Re-check: a concurrent SetActiveRide may have won the race.
	if m.activeDriverRide[driverID] == "" {
		m.activeDriverRide[driverID] = active.ID
		m.activeDriverStatus[driverID] = active.Status
	}
	rideID := m.activeDriverRide[driverID]
	status := m.activeDriverStatus[driverID]
	m.mu.Unlock()

	// Recovered: drop the throttle entry so a later loss re-queries promptly.
	m.rideRecoveryMu.Lock()
	delete(m.rideRecovery, driverID)
	m.rideRecoveryMu.Unlock()

	logger.Log.Info().Str("driver_id", driverID).Str("ride_id", rideID).Str("status", string(status)).Msg("Recovered active ride from store")
	return rideID, status
}

// handleLocationUpdate processes incoming continuous GPS pings from Drivers
// and stores them directly into the high-performance H3 Location Store.
func (m *Manager) handleLocationUpdate(client *Client, payload json.RawMessage) {
	// Only drivers should be sending location updates to the grid.
	// Unverified drivers are still drivers for GPS purposes: their JWT carries
	// "unvrf_driver" and dropping their pings silently left active riders with
	// no LOCATION_UPDATE / ETA at all.
	if client.Role != "driver" && client.Role != "unvrf_driver" {
		return
	}

	var update LocationUpdatePayload
	if err := json.Unmarshal(payload, &update); err != nil {
		logger.Log.Error().Err(err).Str("driver_id", client.ID).Msg("Failed to parse LocationUpdate")
		return
	}

	// Instantly update the LocationStore (H3 mapping)
	err := m.LocStore.UpdateLocation(client.ID, update.Lat, update.Lng)
	if err != nil {
		logger.Log.Error().Err(err).Str("driver_id", client.ID).Msg("Failed to update location store")
	}

	// Cache the identity fields used by dispatch and the admin fleet feed.
	vType, err := m.LocStore.GetDriverVehicleType(client.ID)
	if err != nil || vType == "" {
		if ids.IsValid(client.ID) {
			driver, lookupErr := m.AuthStore.FindDriverByID(context.Background(), client.ID)
			if lookupErr == nil && driver != nil {
				_ = m.LocStore.SetDriverIdentity(client.ID, driver.Name, driver.VehicleType)
				vType = driver.VehicleType
			}
		}
	}

	m.mu.RLock()
	rideID := m.activeDriverRide[client.ID]
	rideStatus := m.activeDriverStatus[client.ID]
	m.mu.RUnlock()

	// Rehydrate after a restart/redeploy: the in-memory driver->ride map is
	// empty until clients reconnect, so without this the ping is published
	// with RideID="" and watchers never receive location or ETA updates.
	if rideID == "" {
		rideID, rideStatus = m.recoverActiveRide(client.ID)
	}

	if rideID != "" {
		currentStatus, _ := m.LocStore.GetDriverStatus(client.ID)
		if currentStatus != "BUSY" {
			_ = m.LocStore.SetDriverStatus(client.ID, "BUSY")
		}
		_ = m.LocStore.SetDriverRide(client.ID, rideID)
	} else {
		_ = m.LocStore.SetDriverRide(client.ID, "")
	}

	if rideID != "" && m.RideStore != nil {
		lat, lng := update.Lat, update.Lng
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = m.RideStore.InsertTrailPoint(ctx, rideID, lat, lng)
		}()
	}

	if rideID != "" && m.ETAEngine != nil {
		driverID := client.ID
		curLat, curLng := update.Lat, update.Lng
		curStatus := rideStatus
		go func() {
			if etaPayload := m.ETAEngine.OnDriverLocation(rideID, driverID, curLat, curLng, curStatus); etaPayload != nil {
				if m.EventBus != nil {
					m.EventBus.PublishEvent(eventbus.ChannelRideETAUpdate, *etaPayload)
				}
			}
		}()
	}

	var stoppedSince, stoppedStage string
	var stoppedSeconds int64
	if rideStatus == ride.StatusInProgress {
		if state := m.checkStoppedVehicle(client.ID, rideID, vType, update.Lat, update.Lng, time.Now(), client.SessionID); state != nil {
			view := state.View(time.Now())
			stoppedSince = view.StoppedSince.UTC().Format(time.RFC3339Nano)
			stoppedSeconds = view.StoppedSeconds
			stoppedStage = view.Stage
		}
	}

	if m.EventBus != nil && m.allowLocPublish(client.ID) {
		driver, _ := m.LocStore.FleetDriver(client.ID)
		m.EventBus.PublishEvent(eventbus.ChannelDriverLocationUpdate, eventbus.DriverLocationUpdatePayload{
			DriverID:       client.ID,
			DriverName:     driver.DriverName,
			Lat:            update.Lat,
			Lng:            update.Lng,
			RideID:         rideID,
			Status:         string(driver.Status),
			VehicleType:    driver.VehicleType,
			UpdatedAt:      driver.LastUpdated.UTC().Format(time.RFC3339Nano),
			StoppedSince:   stoppedSince,
			StoppedSeconds: stoppedSeconds,
			StoppedStage:   stoppedStage,
			RequestID:      client.SessionID,
		})
	}
}

// checkStoppedVehicle implements the two-stage stopped-vehicle trigger for
// IN_PROGRESS rides, excluding auto/bike/cab types (SOS included: a stopped
// SOS ambulance is the most critical case — the flag flip is a no-op there,
// but driver nudge and admin alarm still fire).
// 3 min stopped -> warn driver, 5 min total -> escalate (flag + admin push).
// It reuses Book-Any type resolution and is a no-op when deps are unset (tests).
func (m *Manager) checkStoppedVehicle(driverID, rideID, vehicleTypeID string, lat, lng float64, now time.Time, sessionID string) *safety.VehicleState {
	if m.Safety == nil || m.RideStore == nil || m.EventBus == nil || rideID == "" {
		return nil
	}
	m.mu.RLock()
	cachedAmbName := m.AmbTypeNames[vehicleTypeID]
	m.mu.RUnlock()
	if safety.IsExcludedVehicleType(cachedAmbName) {
		if m.Safety.ResetState(driverID) {
			m.deleteSafetyState(driverID)
		}
		return nil
	}

	action, elapsed, state, changed := m.Safety.CheckState(driverID, rideID, lat, lng, now)
	if changed {
		m.persistSafetyState(state)
	}
	if action == safety.ActionNone {
		return &state
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r, err := m.RideStore.GetRideByID(ctx, rideID)
	if err != nil || r == nil {
		if err != nil {
			logger.Log.Error().Err(err).Str("ride_id", rideID).Msg("Stopped-vehicle: GetRideByID failed")
		}
		return &state
	}
	if r.Status != ride.StatusInProgress || r.DriverID == nil || *r.DriverID != driverID {
		m.resetSafetyState(driverID)
		return nil
	}
	ambName := m.ambulanceTypeName(ctx, vehicleTypeID, r)
	if safety.IsExcludedVehicleType(ambName) {
		m.resetSafetyState(driverID)
		return nil
	}

	var driverName, driverMobile string
	if ids.IsValid(driverID) {
		if drv, lookupErr := m.AuthStore.FindDriverByID(ctx, driverID); lookupErr == nil && drv != nil {
			driverName, driverMobile = drv.Name, drv.Mobile
		}
	}

	stoppedMin := int(elapsed / time.Minute)
	if stoppedMin < 1 {
		stoppedMin = 1
	}
	ambType := vehicleTypeID
	if r.AmbTypeID != nil {
		ambType = *r.AmbTypeID
	}
	rideRef := eventbus.ShortRideRef(rideID)

	switch action {
	case safety.ActionWarn:
		m.EventBus.PublishEvent(eventbus.ChannelSafetyStoppedWarn, eventbus.SafetyStoppedWarningPayload{
			RideID: rideID, DriverID: driverID, UserID: r.UserID,
			Lat: lat, Lng: lng, StoppedMinutes: stoppedMin, AmbType: ambType,
			AmbTypeName: ambName, DriverName: driverName, DriverMobile: driverMobile, RideRef: rideRef,
			RequestID: sessionID,
		})
	case safety.ActionEscalate:
		if r.EmergencyPriority == 0 {
			escalated, updateErr := m.RideStore.EscalateEmergencyForStoppedVehicle(ctx, rideID)
			if updateErr != nil {
				logger.Log.Error().Err(updateErr).Str("ride_id", rideID).Msg("Stopped-vehicle: escalation update failed")
				return &state
			}
			if !escalated {
				return &state
			}
		}
		m.EventBus.PublishEvent(eventbus.ChannelSafetyStoppedAlarm, eventbus.SafetyStoppedEmergencyPayload{
			RideID: rideID, DriverID: driverID, UserID: r.UserID,
			Lat: lat, Lng: lng, StoppedMinutes: stoppedMin, AmbType: ambType,
			AmbTypeName: ambName, DriverName: driverName, DriverMobile: driverMobile, RideRef: rideRef,
			RequestID: sessionID,
		})
	}
	return &state
}

// ambulanceTypeName resolves the display name using the cached Book-Any map,
// falling back to a DB lookup. Empty means unknown (treated as eligible).
func (m *Manager) ambulanceTypeName(ctx context.Context, vehicleTypeID string, r *ride.Ride) string {
	id := vehicleTypeID
	if id == "" && r.AmbTypeID != nil {
		id = *r.AmbTypeID
	}
	if id == "" {
		return ""
	}
	m.mu.RLock()
	name := m.AmbTypeNames[id]
	m.mu.RUnlock()
	if name != "" {
		return name
	}
	if m.AdminStore == nil {
		return ""
	}
	amb, err := m.AdminStore.GetAmbulanceTypeByID(ctx, id)
	if err != nil || amb == nil {
		return ""
	}
	return amb.Name
}
