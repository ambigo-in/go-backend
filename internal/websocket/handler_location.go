package websocket

import (
	"context"
	"encoding/json"

	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/logger"
	"go.mongodb.org/mongo-driver/bson/primitive"
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

	// Cache driver's vehicle type on first location ping
	vType, err := m.LocStore.GetDriverVehicleType(client.ID)
	if err != nil || vType == "" {
		objID, convErr := primitive.ObjectIDFromHex(client.ID)
		if convErr == nil {
			driver, lookupErr := m.AuthStore.FindDriverByID(context.Background(), objID)
			if lookupErr == nil && driver != nil {
				_ = m.LocStore.SetDriverVehicleType(client.ID, driver.VehicleType)
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
}
