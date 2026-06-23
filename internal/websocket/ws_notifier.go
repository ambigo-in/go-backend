package websocket

import (
	"encoding/json"
	"log"

	"ambigo-backend/internal/eventbus"
)

// WSNotifier listens to ride events and sends WebSocket notifications.
type WSNotifier struct {
	wsManager *Manager
}

func NewWSNotifier(wsManager *Manager) *WSNotifier {
	return &WSNotifier{wsManager: wsManager}
}

func (n *WSNotifier) SubscribeTo(bus *eventbus.InMemoryBus) {
	bus.Subscribe(eventbus.ChannelRideAccepted, n.handleRideAccepted)
	bus.Subscribe(eventbus.ChannelRideArrived, n.handleRideStatusChanged)
	bus.Subscribe(eventbus.ChannelRideStarted, n.handleRideStatusChanged)
	bus.Subscribe(eventbus.ChannelRideCompleted, n.handleRideCompleted)
	bus.Subscribe(eventbus.ChannelRideCancelled, n.handleRideCancelled)
	bus.Subscribe(eventbus.ChannelDriverLocationUpdate, n.handleDriverLocationUpdate)
}

func (n *WSNotifier) handleRideAccepted(payload []byte) {
	var p eventbus.RideAcceptedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		log.Printf("[WSNotifier] Unmarshal error (ride:accepted): %v", err)
		return
	}
	n.wsManager.SetActiveRide(p.DriverID, p.RideID)
	msg := map[string]string{"ride_id": p.RideID, "driver_id": p.DriverID, "status": p.Status}
	n.wsManager.SendToClient("user", p.UserID, "RIDE_UPDATE", msg)
	n.wsManager.SendToRideWatchers(p.RideID, "RIDE_UPDATE", msg)
}

func (n *WSNotifier) handleRideStatusChanged(payload []byte) {
	var p eventbus.RideStatusChangedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		log.Printf("[WSNotifier] Unmarshal error (ride:status): %v", err)
		return
	}
	msg := map[string]string{"ride_id": p.RideID, "status": p.Status}
	n.wsManager.SendToRideWatchers(p.RideID, "RIDE_UPDATE", msg)
}

func (n *WSNotifier) handleRideCompleted(payload []byte) {
	var p eventbus.RideCompletedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		log.Printf("[WSNotifier] Unmarshal error (ride:completed): %v", err)
		return
	}
	n.wsManager.ClearActiveRide(p.DriverID)
	msg := map[string]string{"ride_id": p.RideID, "status": "COMPLETED"}
	n.wsManager.SendToRideWatchers(p.RideID, "RIDE_UPDATE", msg)
}

func (n *WSNotifier) handleRideCancelled(payload []byte) {
	var p eventbus.RideCancelledPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		log.Printf("[WSNotifier] Unmarshal error (ride:cancelled): %v", err)
		return
	}
	if p.DriverID != "" {
		n.wsManager.ClearActiveRide(p.DriverID)
	}
	msg := map[string]string{"ride_id": p.RideID, "status": "CANCELLED"}
	n.wsManager.SendToRideWatchers(p.RideID, "RIDE_UPDATE", msg)
	if p.Reason == "no_drivers" || p.Reason == "all_drivers_exhausted" {
		n.wsManager.SendToClient("user", p.UserID, "ERROR", map[string]string{
			"message": "All nearby drivers are busy. Please try again.",
		})
	}
}

func (n *WSNotifier) handleDriverLocationUpdate(payload []byte) {
	var p eventbus.DriverLocationUpdatePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		log.Printf("[WSNotifier] Unmarshal error (driver:location): %v", err)
		return
	}
	if p.RideID == "" {
		return
	}
	n.wsManager.SendToRideWatchers(p.RideID, "LOCATION_UPDATE", map[string]float64{
		"lat": p.Lat, "lng": p.Lng,
	})
}
