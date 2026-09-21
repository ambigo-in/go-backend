package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/notification"
)

// FCMNotifier listens to ride events and sends FCM push notifications.
type FCMNotifier struct {
	fcmClient  *notification.FCMClient
	authStore  *auth.Store
	adminStore *admin.Store
}

func NewFCMNotifier(fcmClient *notification.FCMClient, authStore *auth.Store) *FCMNotifier {
	return &FCMNotifier{fcmClient: fcmClient, authStore: authStore}
}

// SetAdminStore wires admin tokens for stopped-vehicle escalation.
func (n *FCMNotifier) SetAdminStore(adminStore *admin.Store) {
	n.adminStore = adminStore
}

const fcmWorkerPoolSize = 10

func (n *FCMNotifier) SubscribeTo(bus *InMemoryBus) {
	n.subscribeWithPool(bus, ChannelRideDriverOffered, n.handleRideOffered)
	n.subscribeWithPool(bus, ChannelRideAccepted, n.handleRideAccepted)
	n.subscribeWithPool(bus, ChannelRideArrived, n.handleRideArrived)
	n.subscribeWithPool(bus, ChannelRideStarted, n.handleRideStarted)
	n.subscribeWithPool(bus, ChannelRideCompleted, n.handleRideCompleted)
	n.subscribeWithPool(bus, ChannelRideCancelled, n.handleRideCancelled)
	n.subscribeWithPool(bus, ChannelAuthDriverApproved, n.handleDriverApproved)
	n.subscribeWithPool(bus, ChannelReferralCredited, n.handleReferralCredited)
	n.subscribeWithPool(bus, ChannelAuthSessionReplaced, n.handleSessionReplaced)
	n.subscribeWithPool(bus, ChannelSafetyStoppedWarn, n.handleSafetyStoppedWarn)
	n.subscribeWithPool(bus, ChannelSafetyStoppedAlarm, n.handleSafetyStoppedAlarm)
}

// subscribeWithPool creates a single shared channel via SubscribeWithChan and
// spawns 10 goroutines that compete for messages. This replaces the previous
// single-goroutine-per-channel model which at 200 rps needed 50s to drain
// 500 ride:driver_offered events (FCM fetch 5s + send). With 10 workers the
// drain time is ~5s while preserving the 5s context + GetFCMToken + SendDataMessage
// logic inside each handler.
func (n *FCMNotifier) subscribeWithPool(bus *InMemoryBus, channel string, handler func([]byte)) {
	ch := bus.SubscribeWithChan(channel)
	for i := 0; i < fcmWorkerPoolSize; i++ {
		go func(workerID int) {
			defer func() {
				if r := recover(); r != nil {
					logger.Log.Error().Interface("panic", r).Str("channel", channel).Int("worker", workerID).Msg("Panic in FCM worker")
				}
			}()
			for msg := range ch {
				func() {
					defer func() {
						if r := recover(); r != nil {
							logger.Log.Error().Interface("panic", r).Str("channel", channel).Int("worker", workerID).Msg("Panic in FCM handler")
						}
					}()
					handler(msg)
				}()
			}
		}(i)
	}
}

func (n *FCMNotifier) handleRideOffered(payload []byte) {
	var p RideDriverOfferedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:driver_offered").Msg("Unmarshal error")
		return
	}

	log := logger.Log.With()
	if p.RequestID != "" {
		log = log.Str("request_id", p.RequestID)
	}
	ll := log.Logger()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	token, err := n.authStore.GetDriverFCMToken(ctx, p.DriverID)
	if err != nil {
		ll.Error().Err(err).Str("driver_id", p.DriverID).Msg("Failed to get FCM token for driver")
		return
	}
	if token == nil || *token == "" {
		return
	}

	data := map[string]string{
		"type":                  "RIDE_OFFERED",
		"ride_id":               p.RideID,
		"distance":              fmt.Sprintf("%.1f", p.TripDistanceKm),
		"distance_km":           fmt.Sprintf("%.2f", p.TripDistanceKm),
		"cost":                  fmt.Sprintf("%.0f", p.DriverShare),
		"fare":                  fmt.Sprintf("%.2f", p.Fare),
		"driver_share":          fmt.Sprintf("%.2f", p.DriverShare),
		"pickup_lat":            fmt.Sprintf("%f", p.PickupLat),
		"pickup_lng":            fmt.Sprintf("%f", p.PickupLng),
		"pickup_address":        p.PickupAddress,
		"dropoff_lat":           fmt.Sprintf("%f", p.DropoffLat),
		"dropoff_lng":           fmt.Sprintf("%f", p.DropoffLng),
		"drop_address":          p.DropAddress,
		"payment_mode":          p.PaymentMode,
		"eta_seconds":           fmt.Sprintf("%d", p.ETASeconds),
		"pickup_distance_km":    fmt.Sprintf("%.2f", p.PickupDistanceKm),
		"trip_duration_seconds": fmt.Sprintf("%d", p.TripDurationSeconds),
		"amb_type_id":           p.AmbTypeID,
		"offered_at":            p.OfferedAt,
		"offer_expires_in":      fmt.Sprintf("%d", p.OfferExpiresIn),
		"body":                  fmt.Sprintf("%.1f km · ₹%.0f", p.TripDistanceKm, p.DriverShare),
	}
	if p.IsSOS {
		data["title"] = "EMERGENCY ALERT"
		data["is_sos"] = "true"
	} else {
		data["title"] = "New Ride Request"
		data["is_sos"] = "false"
	}

	if err := n.fcmClient.SendDataMessage(ctx, *token, data); err != nil {
		ll.Error().Err(err).Str("driver_id", p.DriverID).Msg("FCM push failed for driver")
	}
}

// handleRideAccepted sends a push to the user when a driver accepts the ride.
func (n *FCMNotifier) handleRideAccepted(payload []byte) {
	var p RideAcceptedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:accepted").Msg("Unmarshal error")
		return
	}

	if p.UserID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	token, err := n.authStore.GetUserFCMToken(ctx, p.UserID)
	if err != nil || token == nil || *token == "" {
		return
	}

	data := map[string]string{
		"type":      "RIDE_ACCEPTED",
		"ride_id":   p.RideID,
		"driver_id": p.DriverID,
		"title":     "Driver found",
		"body":      "Your driver is on the way to your pickup point.",
	}

	if err := n.fcmClient.SendDataMessage(ctx, *token, data); err != nil {
		logger.Log.Error().Err(err).Str("user_id", p.UserID).Msg("Ride accepted FCM push failed for user")
	}
}

// handleRideArrived sends a push to the user when the driver reaches the pickup.
func (n *FCMNotifier) handleRideArrived(payload []byte) {
	var p RideStatusChangedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:arrived").Msg("Unmarshal error")
		return
	}

	if p.UserID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	token, err := n.authStore.GetUserFCMToken(ctx, p.UserID)
	if err != nil || token == nil || *token == "" {
		return
	}

	data := map[string]string{
		"type":    "RIDE_ARRIVED",
		"ride_id": p.RideID,
		"title":   "Driver has arrived",
		"body":    "Your driver has reached the pickup point.",
	}

	if err := n.fcmClient.SendDataMessage(ctx, *token, data); err != nil {
		logger.Log.Error().Err(err).Str("user_id", p.UserID).Msg("Ride arrived FCM push failed for user")
	}
}

// handleRideStarted sends a push to the user when the ride begins.
func (n *FCMNotifier) handleRideStarted(payload []byte) {
	var p RideStatusChangedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:started").Msg("Unmarshal error")
		return
	}

	if p.UserID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	token, err := n.authStore.GetUserFCMToken(ctx, p.UserID)
	if err != nil || token == nil || *token == "" {
		return
	}

	data := map[string]string{
		"type":    "RIDE_STARTED",
		"ride_id": p.RideID,
		"title":   "Ride started",
		"body":    "Your ride has started. Have a safe ride!",
	}

	if err := n.fcmClient.SendDataMessage(ctx, *token, data); err != nil {
		logger.Log.Error().Err(err).Str("user_id", p.UserID).Msg("Ride started FCM push failed for user")
	}
}

// handleRideCompleted sends a push to the user with the final fare.
func (n *FCMNotifier) handleRideCompleted(payload []byte) {
	var p RideCompletedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:completed").Msg("Unmarshal error")
		return
	}

	if p.UserID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	token, err := n.authStore.GetUserFCMToken(ctx, p.UserID)
	if err != nil || token == nil || *token == "" {
		return
	}

	data := map[string]string{
		"type":         "RIDE_COMPLETED",
		"ride_id":      p.RideID,
		"amount":       fmt.Sprintf("%.2f", p.FinalAmount),
		"payment_mode": p.PaymentMode,
		"title":        "Ride completed",
		"body":         fmt.Sprintf("Ride completed. Total fare ₹%.2f.", p.FinalAmount),
	}

	if err := n.fcmClient.SendDataMessage(ctx, *token, data); err != nil {
		logger.Log.Error().Err(err).Str("user_id", p.UserID).Msg("Ride completed FCM push failed for user")
	}
}

// handleRideCancelled notifies the user or driver, depending on who cancelled.
func (n *FCMNotifier) handleRideCancelled(payload []byte) {
	var p RideCancelledPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "ride:cancelled").Msg("Unmarshal error")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	switch p.Reason {
	case "driver_cancelled":
		if p.UserID == "" {
			return
		}
		token, err := n.authStore.GetUserFCMToken(ctx, p.UserID)
		if err != nil || token == nil || *token == "" {
			return
		}
		data := map[string]string{
			"type":    "RIDE_CANCELLED",
			"ride_id": p.RideID,
			"title":   "Driver cancelled the ride",
			"body":    "We are finding you another ride, please wait.",
		}
		if err := n.fcmClient.SendDataMessage(ctx, *token, data); err != nil {
			logger.Log.Error().Err(err).Str("user_id", p.UserID).Msg("Ride cancelled FCM push failed for user")
		}
	case "user_cancelled":
		if p.DriverID == "" {
			return
		}
		token, err := n.authStore.GetDriverFCMToken(ctx, p.DriverID)
		if err != nil || token == nil || *token == "" {
			return
		}
		data := map[string]string{
			"type":    "RIDE_CANCELLED",
			"ride_id": p.RideID,
			"title":   "Ride cancelled by user",
			"body":    "The rider has cancelled the ride.",
		}
		if err := n.fcmClient.SendDataMessage(ctx, *token, data); err != nil {
			logger.Log.Error().Err(err).Str("driver_id", p.DriverID).Msg("Ride cancelled FCM push failed for driver")
		}
	case "no_drivers", "all_drivers_exhausted":
		if p.UserID == "" {
			return
		}
		token, err := n.authStore.GetUserFCMToken(ctx, p.UserID)
		if err != nil || token == nil || *token == "" {
			return
		}
		data := map[string]string{
			"type":    "RIDE_CANCELLED",
			"ride_id": p.RideID,
			"title":   "No drivers found",
			"body":    "No drivers available right now. Please try again in a few minutes.",
		}
		if err := n.fcmClient.SendDataMessage(ctx, *token, data); err != nil {
			logger.Log.Error().Err(err).Str("user_id", p.UserID).Msg("No drivers FCM push failed for user")
		}
	}
}

// handleSessionReplaced pushes the single-session kill-switch: every
// superseded device's session token gets a SESSION_REVOKED data push, which
// reaches the old phone in seconds even with no socket open or the app killed.
func (n *FCMNotifier) handleSessionReplaced(payload []byte) {
	var p AuthSessionReplacedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "auth:session_replaced").Msg("Unmarshal error")
		return
	}
	if p.UserID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tokens, err := n.authStore.ListSupersededSessionFCMTokens(ctx, p.UserID, p.SessionID)
	if err != nil {
		logger.Log.Error().Err(err).Str("user_id", p.UserID).Msg("Kill-switch: failed to list superseded session tokens")
		return
	}
	for _, token := range tokens {
		data := map[string]string{
			"type":   "SESSION_REVOKED",
			"title":  "Logged in on another device",
			"body":   "Your account was logged in on another device. You have been logged out.",
			"is_sos": "false",
		}
		if err := n.fcmClient.SendDataMessage(ctx, token, data); err != nil {
			logger.Log.Error().Err(err).Str("user_id", p.UserID).Msg("Kill-switch FCM push failed")
		}
	}
}

func (n *FCMNotifier) handleDriverApproved(payload []byte) {
	var p AuthDriverApprovedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "auth:driver_approved").Msg("Unmarshal error")
		return
	}

	log := logger.Log.With()
	if p.RequestID != "" {
		log = log.Str("request_id", p.RequestID)
	}
	ll := log.Logger()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	token, err := n.authStore.GetDriverFCMToken(ctx, p.DriverID)
	if err != nil || token == nil || *token == "" {
		return
	}

	data := map[string]string{
		"type":  "ACCOUNT_APPROVED",
		"title": "Welcome to Ambigo!",
		"body":  "Your driver account has been approved. Please login again.",
	}

	if err := n.fcmClient.SendDataMessage(ctx, *token, data); err != nil {
		ll.Error().Err(err).Str("driver_id", p.DriverID).Msg("Welcome FCM push failed for driver")
	}
}

// handleReferralCredited sends a push notification when a referral reward is credited.
func (n *FCMNotifier) handleReferralCredited(payload []byte) {
	var p ReferralCreditedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "referral:credited").Msg("Unmarshal error")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var token *string
	var err error

	switch p.RecipientRole {
	case "driver":
		token, err = n.authStore.GetDriverFCMToken(ctx, p.RecipientID)
	case "user":
		token, err = n.authStore.GetUserFCMToken(ctx, p.RecipientID)
	default:
		return
	}

	if err != nil || token == nil || *token == "" {
		return
	}

	// Build notification message based on reason
	var title, body string
	switch p.Reason {
	case "signup_referral":
		title = "🎉 Referral Bonus!"
		body = fmt.Sprintf("Your friend signed up! ₹%.0f credit added!", p.Amount)
	case "ride_threshold_met":
		title = "🎉 Referral Reward!"
		body = fmt.Sprintf("Your referral completed the required rides! ₹%.0f added to your wallet!", p.Amount)
	case "welcome_bonus":
		title = "🎉 Welcome Bonus!"
		body = fmt.Sprintf("₹%.0f referral credit added to your account!", p.Amount)
	default:
		title = "🎉 Referral Reward!"
		body = fmt.Sprintf("₹%.0f referral credit added!", p.Amount)
	}

	data := map[string]string{
		"title": title,
		"body":  body,
	}

	if err := n.fcmClient.SendDataMessage(ctx, *token, data); err != nil {
		logger.Log.Error().Err(err).Str("recipient_id", p.RecipientID).Msg("Referral FCM push failed")
	}
}

// handleSafetyStoppedWarn pushes the 3-minute nudge to the driver only,
// reusing the driver-offer token lookup + data-message pattern.
func (n *FCMNotifier) handleSafetyStoppedWarn(payload []byte) {
	var p SafetyStoppedWarningPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "safety:stopped_warning").Msg("Unmarshal error")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	token, err := n.authStore.GetDriverFCMToken(ctx, p.DriverID)
	if err != nil || token == nil || *token == "" {
		return
	}
	data := map[string]string{
		"type":            "STOPPED_WARNING",
		"ride_id":         p.RideID,
		"ride_ref":        p.RideRef,
		"driver_id":       p.DriverID,
		"stopped_minutes": fmt.Sprintf("%d", p.StoppedMinutes),
		"lat":             fmt.Sprintf("%f", p.Lat),
		"lng":             fmt.Sprintf("%f", p.Lng),
		"title":           "Are you stopped?",
		"body":            fmt.Sprintf("Stopped %d min on trip #%s — tap OK if all good.", p.StoppedMinutes, p.RideRef),
	}
	if err := n.fcmClient.SendDataMessage(ctx, *token, data); err != nil {
		logger.Log.Error().Err(err).Str("driver_id", p.DriverID).Msg("Stopped-warning FCM push failed for driver")
	}
}

// handleSafetyStoppedAlarm pushes the 5-minute escalation to driver, user,
// and every active admin. Bodies carry contact details (who, in what, which
// trip) instead of raw IDs so the admin notification is actionable on sight.
// Admin pushes include a visible notification block so backgrounded/killed
// admin apps still surface it in the tray; driver/user apps render their own
// in-app banners from the data payload.
func (n *FCMNotifier) handleSafetyStoppedAlarm(payload []byte) {
	var p SafetyStoppedEmergencyPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.Log.Error().Err(err).Str("channel", "safety:stopped_emergency").Msg("Unmarshal error")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	who := p.DriverName
	if who == "" {
		who = "Driver"
	}
	if p.DriverMobile != "" {
		who += " " + p.DriverMobile
	}
	amb := p.AmbTypeName
	if amb == "" {
		amb = "Ambulance"
	}
	title := "Stopped vehicle emergency"
	body := fmt.Sprintf("%s • %s • stopped %d min • trip #%s.", amb, who, p.StoppedMinutes, p.RideRef)

	base := map[string]string{
		"type":            "STOPPED_EMERGENCY",
		"ride_id":         p.RideID,
		"ride_ref":        p.RideRef,
		"driver_id":       p.DriverID,
		"driver_name":     p.DriverName,
		"driver_mobile":   p.DriverMobile,
		"amb_type_name":   p.AmbTypeName,
		"stopped_minutes": fmt.Sprintf("%d", p.StoppedMinutes),
		"lat":             fmt.Sprintf("%f", p.Lat),
		"lng":             fmt.Sprintf("%f", p.Lng),
		"title":           title,
		"body":            body,
		"is_sos":          "true",
	}

	if token, err := n.authStore.GetDriverFCMToken(ctx, p.DriverID); err == nil && token != nil && *token != "" {
		if err := n.fcmClient.SendDataMessage(ctx, *token, base); err != nil {
			logger.Log.Error().Err(err).Str("driver_id", p.DriverID).Msg("Stopped-emergency FCM push failed for driver")
		}
	}
	if p.UserID != "" {
		if token, err := n.authStore.GetUserFCMToken(ctx, p.UserID); err == nil && token != nil && *token != "" {
			if err := n.fcmClient.SendDataMessage(ctx, *token, base); err != nil {
				logger.Log.Error().Err(err).Str("user_id", p.UserID).Msg("Stopped-emergency FCM push failed for user")
			}
		}
	}
	if n.adminStore == nil {
		return
	}
	tokens, err := n.adminStore.ListActiveAdminFCMTokens(ctx)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Stopped-emergency: failed to list admin FCM tokens")
		return
	}
	for _, token := range tokens {
		if err := n.fcmClient.SendAlertMessage(ctx, token, title, body, base); err != nil {
			logger.Log.Error().Err(err).Msg("Stopped-emergency FCM push failed for admin")
		}
	}
}
