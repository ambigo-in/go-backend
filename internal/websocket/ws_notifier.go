package websocket

import (
	"encoding/json"

	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/ride"
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
	bus.Subscribe(eventbus.ChannelRideWithdrawn, n.handleRideWithdrawn)
	bus.Subscribe(eventbus.ChannelRideAccepted, n.handleRideAccepted)
	bus.Subscribe(eventbus.ChannelRideArrived, n.handleRideStatusChanged)
	bus.Subscribe(eventbus.ChannelRideStarted, n.handleRideStatusChanged)
	bus.Subscribe(eventbus.ChannelRideCompleted, n.handleRideCompleted)
	bus.Subscribe(eventbus.ChannelRideCancelled, n.handleRideCancelled)
	bus.Subscribe(eventbus.ChannelDriverLocationUpdate, n.handleDriverLocationUpdate)
	bus.Subscribe(eventbus.ChannelAuthSessionReplaced, n.handleSessionReplaced)
	bus.Subscribe(eventbus.ChannelSafetyStoppedWarn, n.handleSafetyStoppedWarn)
	bus.Subscribe(eventbus.ChannelSafetyStoppedAlarm, n.handleSafetyStoppedAlarm)
	activityChannels := []string{
		eventbus.ChannelRideRequested, eventbus.ChannelRideAccepted,
		eventbus.ChannelRideArrived, eventbus.ChannelRideStarted,
		eventbus.ChannelRideCompleted, eventbus.ChannelRideCancelled,
		eventbus.ChannelRideDestinationChanged, eventbus.ChannelRideConditionUpdated,
		eventbus.ChannelSafetyStoppedWarn, eventbus.ChannelSafetyStoppedAlarm,
	}
	for _, channel := range activityChannels {
		bus.Subscribe(channel, n.handleAdminActivity)
	}
}

// handleSessionReplaced records the replacing session and kicks every live
// connection of the replaced session, so a new login on another device logs
// the old device out immediately. Recording first means any reconnect from
// the old session is rejected at registration, not just at kick time.
func (n *WSNotifier) handleSessionReplaced(payload []byte) {
	var p eventbus.AuthSessionReplacedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "auth:session_replaced").Msg("Unmarshal error")
		return
	}
	n.wsManager.SetCurrentSession(p.Role, p.UserID, p.SessionID)
	n.wsManager.KickSessions(p.Role, p.UserID)
}

func (n *WSNotifier) handleRideDriverOffered(payload []byte) {
	var p eventbus.RideDriverOfferedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:driver_offered").Msg("Unmarshal error")
		return
	}
	msg := map[string]interface{}{
		"ride_id":               p.RideID,
		"user_id":               p.UserID,
		"pickup_lat":            p.PickupLat,
		"pickup_lng":            p.PickupLng,
		"pickup_address":        p.PickupAddress,
		"dropoff_lat":           p.DropoffLat,
		"dropoff_lng":           p.DropoffLng,
		"drop_address":          p.DropAddress,
		"eta_seconds":           p.ETASeconds,
		"distance_km":           p.TripDistanceKm,
		"pickup_distance_km":    p.PickupDistanceKm,
		"trip_duration_seconds": p.TripDurationSeconds,
		"fare":                  p.Fare,
		"cost":                  p.DriverShare,
		"payment_mode":          p.PaymentMode,
		"is_sos":                p.IsSOS,
		"amb_type_id":           p.AmbTypeID,
		"offered_at":            p.OfferedAt,
		"offer_expires_in":      p.OfferExpiresIn,
	}
	n.wsManager.SendToClient("driver", p.DriverID, EventRideRequested, msg)
}

func (n *WSNotifier) handleRideWithdrawn(payload []byte) {
	var p eventbus.RideWithdrawnPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:withdrawn").Msg("Unmarshal error")
		return
	}
	msg := map[string]interface{}{
		"ride_id": p.RideID,
	}
	n.wsManager.SendToClient("driver", p.DriverID, "RIDE_WITHDRAWN", msg)
}

func (n *WSNotifier) handleRideAccepted(payload []byte) {
	var p eventbus.RideAcceptedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:accepted").Msg("Unmarshal error")
		return
	}
	n.wsManager.SetActiveRide(p.DriverID, p.RideID)
	n.broadcastFleetStatus(p.DriverID)
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
	n.wsManager.SetRideStatus(p.DriverID, p.RideID, ride.RideStatus(p.Status))
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
	n.broadcastFleetStatus(p.DriverID)
	msg := map[string]interface{}{
		"ride_id":          p.RideID,
		"status":           "COMPLETED",
		"amount":           p.FinalAmount,
		"payment_id":       p.PaymentID,
		"payment_mode":     p.PaymentMode,
		"estimated_amount": p.EstimatedAmount,
		"extra_km":         p.ExtraKm,
		"fare_reason":      p.FareReason,
		"fare_note":        p.FareNote,
	}
	n.wsManager.SendToRideWatchers(p.RideID, "RIDE_UPDATE", msg)
	// Reach a backgrounded user who isn't watching this ride (WS closed),
	// so reopening the app never loops on a stale completed ride.
	if p.UserID != "" {
		n.wsManager.SendToClient("user", p.UserID, "RIDE_UPDATE", msg)
	}
}

func (n *WSNotifier) handleRideCancelled(payload []byte) {
	var p eventbus.RideCancelledPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:cancelled").Msg("Unmarshal error")
		return
	}
	if p.DriverID != "" {
		n.wsManager.ClearActiveRide(p.DriverID)
		n.broadcastFleetStatus(p.DriverID)
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
	if p.RideID != "" {
		n.wsManager.SendToRideWatchers(p.RideID, "LOCATION_UPDATE", map[string]float64{
			"lat": p.Lat, "lng": p.Lng,
		})
	}
	adminUpdate := map[string]interface{}{
		"driver_id":    p.DriverID,
		"driver_name":  p.DriverName,
		"lat":          p.Lat,
		"lng":          p.Lng,
		"ride_id":      p.RideID,
		"status":       p.Status,
		"vehicle_type": p.VehicleType,
		"updated_at":   p.UpdatedAt,
	}
	if p.StoppedSince != "" {
		adminUpdate["stopped_state"] = map[string]interface{}{
			"ride_id": p.RideID, "driver_id": p.DriverID,
			"stopped_since": p.StoppedSince, "stopped_seconds": p.StoppedSeconds, "stage": p.StoppedStage,
		}
	}
	n.wsManager.BroadcastToRole("admin", EventFleetLocation, adminUpdate)
}

func (n *WSNotifier) broadcastFleetStatus(driverID string) {
	if driver, ok := n.wsManager.LocStore.FleetDriver(driverID); ok {
		n.wsManager.BroadcastToRole("admin", EventFleetStatus, driver)
	}
}

func (n *WSNotifier) handleAdminActivity(payload []byte) {
	var event map[string]interface{}
	if err := json.Unmarshal(payload, &event); err != nil {
		return
	}
	channel, _ := event["_channel"].(string)
	eventType, _ := event["_event"].(string)
	createdAt, _ := event["_ts"].(string)
	delete(event, "_channel")
	delete(event, "_event")
	delete(event, "_ts")
	delete(event, "_request_id")
	delete(event, "request_id")
	n.wsManager.BroadcastToRole("admin", EventAdminActivity, map[string]interface{}{
		"event_type": eventType,
		"channel":    channel,
		"payload":    event,
		"created_at": createdAt,
	})
}

// handleSafetyStoppedWarn nudges only the driver (stage 1, 3 min stopped).
func (n *WSNotifier) handleSafetyStoppedWarn(payload []byte) {
	var p eventbus.SafetyStoppedWarningPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "safety:stopped_warning").Msg("Unmarshal error")
		return
	}
	n.wsManager.SendToClient("driver", p.DriverID, "STOPPED_WARNING", map[string]interface{}{
		"ride_id":         p.RideID,
		"ride_ref":        p.RideRef,
		"stopped_minutes": p.StoppedMinutes,
		"lat":             p.Lat,
		"lng":             p.Lng,
		"amb_type_name":   p.AmbTypeName,
	})
}

// handleSafetyStoppedAlarm escalates (stage 2, 5 min): SOS_ALERT to watchers
// and driver, plus broadcast to connected admin apps. FCM fan-out lives in FCMNotifier.
func (n *WSNotifier) handleSafetyStoppedAlarm(payload []byte) {
	var p eventbus.SafetyStoppedEmergencyPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "safety:stopped_emergency").Msg("Unmarshal error")
		return
	}
	msg := map[string]interface{}{
		"ride_id":         p.RideID,
		"ride_ref":        p.RideRef,
		"driver_id":       p.DriverID,
		"driver_name":     p.DriverName,
		"driver_mobile":   p.DriverMobile,
		"amb_type_name":   p.AmbTypeName,
		"reason":          "stopped_vehicle",
		"stopped_minutes": p.StoppedMinutes,
		"lat":             p.Lat,
		"lng":             p.Lng,
	}
	n.wsManager.SendToRideWatchers(p.RideID, EventSOSAlert, msg)
	n.wsManager.SendToClient("driver", p.DriverID, EventSOSAlert, msg)
	n.wsManager.BroadcastToRole("admin", EventSOSAlert, msg)
}
