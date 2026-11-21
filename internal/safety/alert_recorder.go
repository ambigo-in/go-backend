package safety

import (
	"context"
	"encoding/json"

	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/logger"
)

type AlertRecorder struct {
	store    *AlertStore
	onChange func()
}

func NewAlertRecorder(store *AlertStore, onChange func()) *AlertRecorder {
	return &AlertRecorder{store: store, onChange: onChange}
}

func (r *AlertRecorder) SubscribeTo(bus *eventbus.InMemoryBus) {
	bus.Subscribe(eventbus.ChannelRideRequested, r.handleRideRequested)
	bus.Subscribe(eventbus.ChannelSafetyStoppedAlarm, r.handleStoppedVehicle)
	bus.Subscribe(eventbus.ChannelRideCompleted, r.handleRideCompleted)
	bus.Subscribe(eventbus.ChannelRideCancelled, r.handleRideCancelled)
}

func (r *AlertRecorder) handleRideRequested(payload []byte) {
	var event eventbus.RideRequestedPayload
	if err := json.Unmarshal(payload, &event); err != nil || !event.IsSOS {
		return
	}
	lat, lng := event.PickupLat, event.PickupLng
	r.create(&Alert{
		RideID: event.RideID, UserID: event.UserID, AlertType: "sos_booking",
		Severity: "critical", LocationLat: &lat, LocationLng: &lng,
		LocationLabel: event.PickupAddress,
	})
}

func (r *AlertRecorder) handleStoppedVehicle(payload []byte) {
	var event eventbus.SafetyStoppedEmergencyPayload
	if err := json.Unmarshal(payload, &event); err != nil {
		return
	}
	lat, lng := event.Lat, event.Lng
	r.create(&Alert{
		RideID: event.RideID, UserID: event.UserID, DriverID: event.DriverID,
		DriverName: event.DriverName, DriverMobile: event.DriverMobile,
		AlertType: "stopped_vehicle", Severity: "critical",
		LocationLat: &lat, LocationLng: &lng,
	})
}

func (r *AlertRecorder) create(alert *Alert) {
	if err := r.store.Create(context.Background(), alert); err != nil {
		logger.Log.Error().Err(err).Str("ride_id", alert.RideID).Str("alert_type", alert.AlertType).Msg("Failed to persist safety alert")
		return
	}
	if r.onChange != nil {
		r.onChange()
	}
}

func (r *AlertRecorder) handleRideCompleted(payload []byte) {
	var event eventbus.RideCompletedPayload
	if json.Unmarshal(payload, &event) == nil {
		r.resolveRide(event.RideID, "ride_completed")
	}
}

func (r *AlertRecorder) handleRideCancelled(payload []byte) {
	var event eventbus.RideCancelledPayload
	if json.Unmarshal(payload, &event) == nil {
		r.resolveRide(event.RideID, "ride_cancelled")
	}
}

func (r *AlertRecorder) resolveRide(rideID, resolution string) {
	if err := r.store.ResolveRide(context.Background(), rideID, resolution); err != nil {
		logger.Log.Error().Err(err).Str("ride_id", rideID).Msg("Failed to close ride safety alerts")
		return
	}
	if r.onChange != nil {
		r.onChange()
	}
}
