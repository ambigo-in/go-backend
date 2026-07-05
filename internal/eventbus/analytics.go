package eventbus

import (
	"encoding/json"

	"ambigo-backend/internal/logger"
)

// AnalyticsTracker listens to ride and auth events for business analytics.
// Currently a stub — wire to your analytics backend (BigQuery, Kafka, etc.).
type AnalyticsTracker struct{}

func NewAnalyticsTracker() *AnalyticsTracker {
	return &AnalyticsTracker{}
}

func (a *AnalyticsTracker) SubscribeTo(bus *InMemoryBus) {
	bus.Subscribe(ChannelRideRequested, a.handleRideRequested)
	bus.Subscribe(ChannelRideDriverOffered, a.handleRideDriverOffered)
	bus.Subscribe(ChannelRideAccepted, a.handleRideAccepted)
	bus.Subscribe(ChannelRideArrived, a.handleRideArrived)
	bus.Subscribe(ChannelRideStarted, a.handleRideStarted)
	bus.Subscribe(ChannelRideCompleted, a.handleRideCompleted)
	bus.Subscribe(ChannelRideCancelled, a.handleRideCancelled)

	bus.Subscribe(ChannelAuthOTPRequested, a.handleAuthOTPRequested)
	bus.Subscribe(ChannelAuthUserRegistered, a.handleUserRegistered)
	bus.Subscribe(ChannelAuthUserLoggedIn, a.handleUserLoggedIn)
	bus.Subscribe(ChannelAuthDriverCreated, a.handleDriverCreated)
	bus.Subscribe(ChannelAuthDriverLoggedIn, a.handleDriverLoggedIn)
	bus.Subscribe(ChannelAuthDriverApproved, a.handleDriverApproved)

	bus.Subscribe(ChannelPaymentCompleted, a.handlePaymentCompleted)
	bus.Subscribe(ChannelWalletWithdrawal, a.handleWalletWithdrawal)

	bus.Subscribe(ChannelAdminAmbTypeCreated, a.handleAmbTypeCreated)
	bus.Subscribe(ChannelAdminAmbTypeDeleted, a.handleAmbTypeDeleted)
	bus.Subscribe(ChannelAdminHospitalAdded, a.handleHospitalAdded)
	bus.Subscribe(ChannelAdminHospitalUpdated, a.handleHospitalUpdated)
	bus.Subscribe(ChannelAdminHospitalDeleted, a.handleHospitalDeleted)

	bus.Subscribe(ChannelAdminOfferCreated, a.handleOfferCreated)
	bus.Subscribe(ChannelAdminOfferDeleted, a.handleOfferDeleted)
}

func (a *AnalyticsTracker) handleRideRequested(payload []byte) {
	var p RideRequestedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:requested").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "ride_requested").Str("ride_id", p.RideID).Str("user_id", p.UserID).Bool("sos", p.IsSOS).Str("payment_mode", p.PaymentMode).Float64("fare", p.Fare).Float64("distance_km", p.DistanceKm).Str("amb_type_id", p.AmbTypeID).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleRideDriverOffered(payload []byte) {
	var p RideDriverOfferedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:driver_offered").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "ride_driver_offered").Str("ride_id", p.RideID).Str("driver_id", p.DriverID).Str("user_id", p.UserID).Float64("pickup_distance_km", p.PickupDistanceKm).Float64("trip_distance_km", p.TripDistanceKm).Int("eta_seconds", p.ETASeconds).Float64("driver_share", p.DriverShare).Bool("sos", p.IsSOS).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleRideAccepted(payload []byte) {
	var p RideAcceptedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:accepted").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "ride_accepted").Str("ride_id", p.RideID).Str("driver_id", p.DriverID).Str("user_id", p.UserID).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleRideArrived(payload []byte) {
	var p RideStatusChangedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:arrived").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "ride_arrived").Str("ride_id", p.RideID).Str("user_id", p.UserID).Str("driver_id", p.DriverID).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleRideStarted(payload []byte) {
	var p RideStatusChangedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:started").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "ride_started").Str("ride_id", p.RideID).Str("user_id", p.UserID).Str("driver_id", p.DriverID).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleRideCompleted(payload []byte) {
	var p RideCompletedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:completed").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "ride_completed").Str("ride_id", p.RideID).Str("user_id", p.UserID).Str("driver_id", p.DriverID).Float64("amount", p.FinalAmount).Float64("driver_share", p.DriverShare).Str("payment_mode", p.PaymentMode).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleRideCancelled(payload []byte) {
	var p RideCancelledPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:cancelled").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "ride_cancelled").Str("ride_id", p.RideID).Str("user_id", p.UserID).Str("driver_id", p.DriverID).Str("reason", p.Reason).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleAuthOTPRequested(payload []byte) {
	var p AuthOTPRequestedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "auth:otp_requested").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "auth_otp_requested").Str("mobile", p.Mobile).Str("role", p.Role).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleUserRegistered(payload []byte) {
	var p AuthUserRegisteredPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "auth:user_registered").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "user_registered").Str("user_id", p.UserID).Str("mobile", p.Mobile).Str("name", p.Name).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleUserLoggedIn(payload []byte) {
	var p AuthUserLoggedInPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "auth:user_logged_in").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "user_logged_in").Str("user_id", p.UserID).Str("mobile", p.Mobile).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleDriverCreated(payload []byte) {
	var p AuthDriverCreatedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "auth:driver_created").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "driver_created").Str("driver_id", p.DriverID).Str("mobile", p.Mobile).Str("name", p.Name).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleDriverLoggedIn(payload []byte) {
	var p AuthDriverLoggedInPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "auth:driver_logged_in").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "driver_logged_in").Str("driver_id", p.DriverID).Str("mobile", p.Mobile).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleDriverApproved(payload []byte) {
	var p AuthDriverApprovedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "auth:driver_approved").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "driver_approved").Str("driver_id", p.DriverID).Str("name", p.Name).Str("mobile", p.Mobile).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handlePaymentCompleted(payload []byte) {
	var p PaymentCompletedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "payment:completed").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "payment_completed").Str("payment_id", p.PaymentID).Str("ride_id", p.RideID).Str("user_id", p.UserID).Str("driver_id", p.DriverID).Float64("amount", p.Amount).Str("mode", p.Mode).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleWalletWithdrawal(payload []byte) {
	var p WalletWithdrawalPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "wallet:withdrawal").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "wallet_withdrawal").Str("driver_id", p.DriverID).Float64("amount", p.Amount).Str("status", p.Status).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleAmbTypeCreated(payload []byte) {
	var p AdminAmbTypePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "admin:ambulance_type_created").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "amb_type_created").Str("amb_type_id", p.AmbTypeID).Str("name", p.Name).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleAmbTypeDeleted(payload []byte) {
	var p AdminAmbTypePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "admin:ambulance_type_deleted").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "amb_type_deleted").Str("amb_type_id", p.AmbTypeID).Str("name", p.Name).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleHospitalAdded(payload []byte) {
	var p AdminHospitalPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "admin:hospital_added").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "hospital_added").Str("hospital_id", p.HospitalID).Str("name", p.Name).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleHospitalUpdated(payload []byte) {
	var p AdminHospitalPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "admin:hospital_updated").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "hospital_updated").Str("hospital_id", p.HospitalID).Str("name", p.Name).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleHospitalDeleted(payload []byte) {
	var p AdminHospitalPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "admin:hospital_deleted").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "hospital_deleted").Str("hospital_id", p.HospitalID).Str("name", p.Name).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleOfferCreated(payload []byte) {
	var p AdminOfferPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "admin:offer_created").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "offer_created").Str("offer_id", p.OfferID).Str("description", p.Description).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}

func (a *AnalyticsTracker) handleOfferDeleted(payload []byte) {
	var p AdminOfferPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "admin:offer_deleted").Msg("Unmarshal error")
		return
	}
	l := logger.Log.With().Str("event", "offer_deleted").Str("offer_id", p.OfferID).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("")
}
