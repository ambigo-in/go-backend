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

// handleLocationUpdate processes incoming continuous GPS pings from Drivers
// and stores them directly into the high-performance H3 Location Store.
func (m *Manager) handleLocationUpdate(client *Client, payload json.RawMessage) {
	// Only drivers should be sending location updates to the grid
	if client.Role != "driver" {
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

	// Cache driver's vehicle type on first location ping (PG: IDs are UUID strings)
	vType, err := m.LocStore.GetDriverVehicleType(client.ID)
	if err != nil || vType == "" {
		if ids.IsValid(client.ID) {
			driver, lookupErr := m.AuthStore.FindDriverByID(context.Background(), client.ID)
			if lookupErr == nil && driver != nil {
				_ = m.LocStore.SetDriverVehicleType(client.ID, driver.VehicleType)
				vType = driver.VehicleType
			}
		}
	}

	m.mu.RLock()
	rideID := m.activeDriverRide[client.ID]
	m.mu.RUnlock()

	// If driver has an active ride but was recreated as AVAILABLE (e.g. after cleanup), fix the status
	if rideID != "" {
		currentStatus, _ := m.LocStore.GetDriverStatus(client.ID)
		if currentStatus != "BUSY" {
			m.LocStore.SetDriverStatus(client.ID, "BUSY")
		}
	}

	// Publish driver location event (subscriber handles onward relay to ride watchers)
	if m.EventBus != nil {
		m.EventBus.PublishEvent(eventbus.ChannelDriverLocationUpdate, eventbus.DriverLocationUpdatePayload{
			DriverID: client.ID,
			Lat:      update.Lat,
			Lng:      update.Lng,
			RideID:   rideID,
		})
	}

	m.checkStoppedVehicle(client.ID, rideID, vType, update.Lat, update.Lng, time.Now())
}

// checkStoppedVehicle implements the two-stage stopped-vehicle trigger for
// IN_PROGRESS rides, excluding auto/bike/cab types (SOS included: a stopped
// SOS ambulance is the most critical case — the flag flip is a no-op there,
// but driver nudge and admin alarm still fire).
// 3 min stopped -> warn driver, 5 min total -> escalate (flag + admin push).
// It reuses Book-Any type resolution and is a no-op when deps are unset (tests).
func (m *Manager) checkStoppedVehicle(driverID, rideID, vehicleTypeID string, lat, lng float64, now time.Time) {
	if m.Safety == nil || m.RideStore == nil || m.EventBus == nil {
		return
	}
	if rideID == "" {
		m.Safety.Reset(driverID)
		return
	}
	action, elapsed := m.Safety.Check(driverID, rideID, lat, lng, now)
	if action == safety.ActionNone {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r, err := m.RideStore.GetRideByID(ctx, rideID)
	if err != nil || r == nil {
		if err != nil {
			logger.Log.Error().Err(err).Str("ride_id", rideID).Msg("Stopped-vehicle: GetRideByID failed")
		}
		return
	}
	// Only IN_PROGRESS rides owned by this driver qualify (SOS included).
	if r.Status != ride.StatusInProgress {
		m.Safety.Reset(driverID)
		return
	}
	if r.DriverID == nil || *r.DriverID != driverID {
		m.Safety.Reset(driverID)
		return
	}
	ambName := m.ambulanceTypeName(ctx, vehicleTypeID, r)
	if safety.IsExcludedVehicleType(ambName) {
		m.Safety.Reset(driverID)
		return
	}

	// Contact details so every consumer (banner, push, dashboard) can act
	// without another lookup: who, in what, which trip.
	var driverName, driverMobile string
	if ids.IsValid(driverID) {
		if drv, lerr := m.AuthStore.FindDriverByID(ctx, driverID); lerr == nil && drv != nil {
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
		})
	case safety.ActionEscalate:
		if r.EmergencyPriority == 0 {
			escalated, err := m.RideStore.EscalateEmergencyForStoppedVehicle(ctx, rideID)
			if err != nil {
				logger.Log.Error().Err(err).Str("ride_id", rideID).Msg("Stopped-vehicle: escalation update failed")
				return
			}
			if !escalated {
				// Ride finished concurrently; nothing more to do.
				return
			}
		}
		// Already-SOS rides skip the flag write above but still raise the
		// alarm: the notification is the point, not the flag.
		m.EventBus.PublishEvent(eventbus.ChannelSafetyStoppedAlarm, eventbus.SafetyStoppedEmergencyPayload{
			RideID: rideID, DriverID: driverID, UserID: r.UserID,
			Lat: lat, Lng: lng, StoppedMinutes: stoppedMin, AmbType: ambType,
			AmbTypeName: ambName, DriverName: driverName, DriverMobile: driverMobile, RideRef: rideRef,
		})
	}
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
