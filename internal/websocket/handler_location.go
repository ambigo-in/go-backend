package websocket

import (
	"context"
	"encoding/json"
	"log"
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
		log.Printf("[WebSocket] Failed to parse LocationUpdate from driver %s: %v", client.ID, err)
		return
	}

	// Instantly update the LocationStore (H3 mapping)
	err := m.LocStore.UpdateLocation(client.ID, update.Lat, update.Lng)
	if err != nil {
		log.Printf("[WebSocket] Failed to update location store for driver %s: %v", client.ID, err)
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
	if rideID != "" {
		m.SendToRideWatchers(rideID, EventLocationUpdate, map[string]float64{
			"lat":       update.Lat,
			"lng":       update.Lng,
			"latitude":  update.Lat,
			"longitude": update.Lng,
		})
	}
}
