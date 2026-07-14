package websocket

import (
	"encoding/json"

	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/logger"
)

// WSNotifier listens to ride events and sends WebSocket notifications.
type WSNotifier struct {
	wsManager *Manager
}

func NewWSNotifier(wsManager *Manager) *WSNotifier {
	return &WSNotifier{wsManager: wsManager}
}

func (n *WSNotifier) SubscribeTo(bus *eventbus.InMemoryBus) {
	bus.Subscribe(eventbus.ChannelRideDriverOffered, n.handleRideDriverOffered)
	bus.Subscribe(eventbus.ChannelRideAccepted, n.handleRideAccepted)
	bus.Subscribe(eventbus.ChannelRideArrived, n.handleRideStatusChanged)
	bus.Subscribe(eventbus.ChannelRideStarted, n.handleRideStatusChanged)
	bus.Subscribe(eventbus.ChannelRideCompleted, n.handleRideCompleted)
	bus.Subscribe(eventbus.ChannelRideCancelled, n.handleRideCancelled)
	bus.Subscribe(eventbus.ChannelDriverLocationUpdate, n.handleDriverLocationUpdate)
}

func (n *WSNotifier) handleRideDriverOffered(payload []byte) {
	var p eventbus.RideDriverOfferedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:driver_offered").Msg("Unmarshal error")
		return
	}
	msg := map[string]interface{}{
		"ride_id":            p.RideID,
		"user_id":            p.UserID,
		"pickup_lat":         p.PickupLat,
		"pickup_lng":         p.PickupLng,
		"pickup_address":     p.PickupAddress,
		"dropoff_lat":        p.DropoffLat,
		"dropoff_lng":        p.DropoffLng,
		"drop_address":       p.DropAddress,
		"eta_seconds":        p.ETASeconds,
		"distance_km":        p.TripDistanceKm,
		"pickup_distance_km": p.PickupDistanceKm,
		"fare":               p.Fare,
		"cost":               p.DriverShare,
		"payment_mode":       p.PaymentMode,
		"is_sos":             p.IsSOS,
	}
	n.wsManager.SendToClient("driver", p.DriverID, EventRideRequested, msg)
}

func (n *WSNotifier) handleRideAccepted(payload []byte) {
	var p eventbus.RideAcceptedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:accepted").Msg("Unmarshal error")
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
		logger.Log.Error().Err(err).Str("channel", "ride:status").Msg("Unmarshal error")
		return
	}
	msg := map[string]string{"ride_id": p.RideID, "status": p.Status}
	n.wsManager.SendToRideWatchers(p.RideID, "RIDE_UPDATE", msg)
}

func (n *WSNotifier) handleRideCompleted(payload []byte) {
	var p eventbus.RideCompletedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:completed").Msg("Unmarshal error")
		return
	}
	n.wsManager.ClearActiveRide(p.DriverID)
	msg := map[string]string{"ride_id": p.RideID, "status": "COMPLETED"}
	n.wsManager.SendToRideWatchers(p.RideID, "RIDE_UPDATE", msg)
}

func (n *WSNotifier) handleRideCancelled(payload []byte) {
	var p eventbus.RideCancelledPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:cancelled").Msg("Unmarshal error")
		return
	}
	if p.DriverID != "" {
		n.wsManager.ClearActiveRide(p.DriverID)
	}
	msg := map[string]interface{}{"ride_id": p.RideID, "status": "CANCELLED"}
	if p.Reason == "no_drivers" || p.Reason == "all_drivers_exhausted" {
		types := p.AvailableTypes
		if types == nil {
			types = []string{}
		}
		msg["available_types"] = types
		n.wsManager.SendToClient("user", p.UserID, "ERROR", map[string]interface{}{
			"message":         "All nearby drivers are busy. Please try again.",
			"available_types": types,
		})
	}
	n.wsManager.SendToRideWatchers(p.RideID, "RIDE_UPDATE", msg)
}

func (n *WSNotifier) handleDriverLocationUpdate(payload []byte) {
	var p eventbus.DriverLocationUpdatePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "driver:location").Msg("Unmarshal error")
		return
	}
	if p.RideID == "" {
		return
	}
	n.wsManager.SendToRideWatchers(p.RideID, "LOCATION_UPDATE", map[string]float64{
		"lat": p.Lat, "lng": p.Lng,
	})
}
