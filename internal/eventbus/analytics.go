package eventbus

import (
	"encoding/json"

	"ambigo-backend/internal/logger"

	"github.com/rs/zerolog"
)

// AnalyticsTracker logs one structured line per business event. The flat
// fields keep Cloud Logging queries simple; the same stream is the input for
// the Axiom (30d hot) and BigQuery (long-term) writers (docs/ops-console-buildout/08).
type AnalyticsTracker struct{}

func NewAnalyticsTracker() *AnalyticsTracker {
	return &AnalyticsTracker{}
}

func (a *AnalyticsTracker) SubscribeTo(bus *InMemoryBus) {
	handlers := map[string]func([]byte){
		ChannelRideRequested:          a.handleRideRequested,
		ChannelRideDriverOffered:      a.handleRideDriverOffered,
		ChannelRideWithdrawn:          a.handleRideWithdrawn,
		ChannelRideAccepted:           a.handleRideAccepted,
		ChannelRideArrived:            a.handleRideArrived,
		ChannelRideStarted:            a.handleRideStarted,
		ChannelRideCompleted:          a.handleRideCompleted,
		ChannelRideCancelled:          a.handleRideCancelled,
		ChannelRideDestinationChanged: a.handleRideDestinationChanged,
		ChannelRideConditionUpdated:   a.handleRideConditionUpdated,
		ChannelAuthOTPRequested:       a.handleAuthOTPRequested,
		ChannelAuthUserRegistered:     a.handleUserRegistered,
		ChannelAuthUserLoggedIn:       a.handleUserLoggedIn,
		ChannelAuthDriverCreated:      a.handleDriverCreated,
		ChannelAuthDriverLoggedIn:     a.handleDriverLoggedIn,
		ChannelAuthDriverApproved:     a.handleDriverApproved,
		ChannelAuthSessionReplaced:    a.handleSessionReplaced,
		ChannelAuthLoggedOut:          a.handleLoggedOut,
		ChannelPaymentCompleted:       a.handlePaymentCompleted,
		ChannelWalletWithdrawal:       a.handleWalletWithdrawal,
		ChannelAdminAmbTypeCreated:    a.handleAmbTypeCreated,
		ChannelAdminAmbTypeDeleted:    a.handleAmbTypeDeleted,
		ChannelAdminHospitalAdded:     a.handleHospitalAdded,
		ChannelAdminHospitalUpdated:   a.handleHospitalUpdated,
		ChannelAdminHospitalDeleted:   a.handleHospitalDeleted,
		ChannelAdminOfferCreated:      a.handleOfferCreated,
		ChannelAdminOfferDeleted:      a.handleOfferDeleted,
		ChannelAdminDriverRejected:    a.handleDriverRejected,
		ChannelReferralCredited:       a.handleReferralCredited,
		ChannelSafetyStoppedWarn:      a.handleSafetyStoppedWarn,
		ChannelSafetyStoppedAlarm:     a.handleSafetyStoppedAlarm,
	}
	// Subscribe from AnalyticsChannels() so the tracker can never drift from
	// the canonical analytics list (driver:location_updated excluded there).
	for _, ch := range AnalyticsChannels() {
		if h, ok := handlers[ch]; ok {
			bus.Subscribe(ch, h)
		}
	}
}

// analyticsBase starts every analytics line with the same schema —
// event (canonical underscore name) + event_channel (bus channel) — so all
// sinks (Cloud Logging, Axiom, BigQuery) see identical identifiers.
func analyticsBase(channel string) zerolog.Context {
	return logger.Log.With().Str("event", CanonicalEventName(channel)).Str("event_channel", channel)
}

func (a *AnalyticsTracker) handleRideRequested(payload []byte) {
	var p RideRequestedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelRideRequested).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelRideRequested).Str("ride_id", p.RideID).Str("user_id", p.UserID).Bool("sos", p.IsSOS).Str("payment_mode", p.PaymentMode).Float64("fare", p.Fare).Float64("distance_km", p.DistanceKm).Str("amb_type_id", p.AmbTypeID).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleRideDriverOffered(payload []byte) {
	var p RideDriverOfferedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelRideDriverOffered).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelRideDriverOffered).Str("ride_id", p.RideID).Str("driver_id", p.DriverID).Str("user_id", p.UserID).Float64("pickup_distance_km", p.PickupDistanceKm).Float64("trip_distance_km", p.TripDistanceKm).Int("eta_seconds", p.ETASeconds).Float64("driver_share", p.DriverShare).Bool("sos", p.IsSOS).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleRideWithdrawn(payload []byte) {
	var p RideWithdrawnPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelRideWithdrawn).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelRideWithdrawn).Str("ride_id", p.RideID).Str("driver_id", p.DriverID).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleRideAccepted(payload []byte) {
	var p RideAcceptedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelRideAccepted).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelRideAccepted).Str("ride_id", p.RideID).Str("driver_id", p.DriverID).Str("user_id", p.UserID).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleRideArrived(payload []byte) {
	var p RideStatusChangedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelRideArrived).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelRideArrived).Str("ride_id", p.RideID).Str("user_id", p.UserID).Str("driver_id", p.DriverID).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleRideStarted(payload []byte) {
	var p RideStatusChangedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelRideStarted).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelRideStarted).Str("ride_id", p.RideID).Str("user_id", p.UserID).Str("driver_id", p.DriverID).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleRideCompleted(payload []byte) {
	var p RideCompletedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelRideCompleted).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelRideCompleted).Str("ride_id", p.RideID).Str("user_id", p.UserID).Str("driver_id", p.DriverID).Float64("amount", p.FinalAmount).Float64("driver_share", p.DriverShare).Str("payment_mode", p.PaymentMode).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleRideCancelled(payload []byte) {
	var p RideCancelledPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelRideCancelled).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelRideCancelled).Str("ride_id", p.RideID).Str("user_id", p.UserID).Str("driver_id", p.DriverID).Str("reason", p.Reason).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleRideDestinationChanged(payload []byte) {
	var p RideDestinationChangedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelRideDestinationChanged).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelRideDestinationChanged).Str("ride_id", p.RideID).Str("hospital_id", p.HospitalID).Str("from_hospital_id", p.FromHospital).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleRideConditionUpdated(payload []byte) {
	var p RideConditionUpdatedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelRideConditionUpdated).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelRideConditionUpdated).Str("ride_id", p.RideID).Str("level", p.Level).Int("severity", p.Severity).Str("source", p.Source).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleAuthOTPRequested(payload []byte) {
	var p AuthOTPRequestedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelAuthOTPRequested).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelAuthOTPRequested).Str("mobile", p.Mobile).Str("role", p.Role).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleUserRegistered(payload []byte) {
	var p AuthUserRegisteredPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelAuthUserRegistered).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelAuthUserRegistered).Str("user_id", p.UserID).Str("mobile", p.Mobile).Str("name", p.Name).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleUserLoggedIn(payload []byte) {
	var p AuthUserLoggedInPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelAuthUserLoggedIn).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelAuthUserLoggedIn).Str("user_id", p.UserID).Str("mobile", p.Mobile).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleDriverCreated(payload []byte) {
	var p AuthDriverCreatedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelAuthDriverCreated).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelAuthDriverCreated).Str("driver_id", p.DriverID).Str("mobile", p.Mobile).Str("name", p.Name).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleDriverLoggedIn(payload []byte) {
	var p AuthDriverLoggedInPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelAuthDriverLoggedIn).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelAuthDriverLoggedIn).Str("driver_id", p.DriverID).Str("mobile", p.Mobile).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleDriverApproved(payload []byte) {
	var p AuthDriverApprovedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelAuthDriverApproved).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelAuthDriverApproved).Str("driver_id", p.DriverID).Str("name", p.Name).Str("mobile", p.Mobile).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleSessionReplaced(payload []byte) {
	var p AuthSessionReplacedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelAuthSessionReplaced).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelAuthSessionReplaced).Str("user_id", p.UserID).Str("role", p.Role).Str("session_id", p.SessionID).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleLoggedOut(payload []byte) {
	var p AuthLoggedOutPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelAuthLoggedOut).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelAuthLoggedOut).Str("user_id", p.UserID).Str("role", p.Role).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handlePaymentCompleted(payload []byte) {
	var p PaymentCompletedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelPaymentCompleted).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelPaymentCompleted).Str("payment_id", p.PaymentID).Str("ride_id", p.RideID).Str("user_id", p.UserID).Str("driver_id", p.DriverID).Float64("amount", p.Amount).Str("mode", p.Mode).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleWalletWithdrawal(payload []byte) {
	var p WalletWithdrawalPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelWalletWithdrawal).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelWalletWithdrawal).Str("driver_id", p.DriverID).Float64("amount", p.Amount).Str("status", p.Status).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleAmbTypeCreated(payload []byte) {
	var p AdminAmbTypePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelAdminAmbTypeCreated).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelAdminAmbTypeCreated).Str("amb_type_id", p.AmbTypeID).Str("name", p.Name).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleAmbTypeDeleted(payload []byte) {
	var p AdminAmbTypePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelAdminAmbTypeDeleted).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelAdminAmbTypeDeleted).Str("amb_type_id", p.AmbTypeID).Str("name", p.Name).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleHospitalAdded(payload []byte) {
	var p AdminHospitalPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelAdminHospitalAdded).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelAdminHospitalAdded).Str("hospital_id", p.HospitalID).Str("name", p.Name).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleHospitalUpdated(payload []byte) {
	var p AdminHospitalPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelAdminHospitalUpdated).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelAdminHospitalUpdated).Str("hospital_id", p.HospitalID).Str("name", p.Name).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleHospitalDeleted(payload []byte) {
	var p AdminHospitalPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelAdminHospitalDeleted).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelAdminHospitalDeleted).Str("hospital_id", p.HospitalID).Str("name", p.Name).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleOfferCreated(payload []byte) {
	var p AdminOfferPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelAdminOfferCreated).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelAdminOfferCreated).Str("offer_id", p.OfferID).Str("description", p.Description).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleOfferDeleted(payload []byte) {
	var p AdminOfferPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelAdminOfferDeleted).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelAdminOfferDeleted).Str("offer_id", p.OfferID).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleDriverRejected(payload []byte) {
	var p AdminDriverRejectedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelAdminDriverRejected).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelAdminDriverRejected).Str("driver_id", p.DriverID).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleReferralCredited(payload []byte) {
	var p ReferralCreditedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelReferralCredited).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelReferralCredited).Str("record_id", p.RecordID).Str("recipient_id", p.RecipientID).Str("recipient_role", p.RecipientRole).Float64("amount", p.Amount).Str("reason", p.Reason).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleSafetyStoppedWarn(payload []byte) {
	var p SafetyStoppedWarningPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelSafetyStoppedWarn).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelSafetyStoppedWarn).Str("ride_id", p.RideID).Str("driver_id", p.DriverID).Str("ride_ref", p.RideRef).Int("stopped_minutes", p.StoppedMinutes).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}

func (a *AnalyticsTracker) handleSafetyStoppedAlarm(payload []byte) {
	var p SafetyStoppedEmergencyPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", ChannelSafetyStoppedAlarm).Msg("Unmarshal error")
		return
	}
	l := analyticsBase(ChannelSafetyStoppedAlarm).Str("ride_id", p.RideID).Str("driver_id", p.DriverID).Str("ride_ref", p.RideRef).Int("stopped_minutes", p.StoppedMinutes).Str("request_id", p.RequestID).Logger()
	l.Info().Msg("analytics event")
}
