package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/notification"
)

// FCMNotifier listens to ride events and sends FCM push notifications.
type FCMNotifier struct {
	fcmClient *notification.FCMClient
	authStore *auth.Store
}

func NewFCMNotifier(fcmClient *notification.FCMClient, authStore *auth.Store) *FCMNotifier {
	return &FCMNotifier{fcmClient: fcmClient, authStore: authStore}
}

func (n *FCMNotifier) SubscribeTo(bus *InMemoryBus) {
	bus.Subscribe(ChannelRideDriverOffered, n.handleRideOffered)
	bus.Subscribe(ChannelAuthDriverApproved, n.handleDriverApproved)
	bus.Subscribe(ChannelReferralCredited, n.handleReferralCredited)
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
		"ride_id":         p.RideID,
		"distance":        fmt.Sprintf("%.1f", p.TripDistanceKm),
		"distance_km":     fmt.Sprintf("%.2f", p.TripDistanceKm),
		"cost":            fmt.Sprintf("%.0f", p.DriverShare),
		"fare":            fmt.Sprintf("%.2f", p.Fare),
		"driver_share":    fmt.Sprintf("%.2f", p.DriverShare),
		"pickup_lat":      fmt.Sprintf("%f", p.PickupLat),
		"pickup_lng":      fmt.Sprintf("%f", p.PickupLng),
		"pickup_address":  p.PickupAddress,
		"dropoff_lat":     fmt.Sprintf("%f", p.DropoffLat),
		"dropoff_lng":     fmt.Sprintf("%f", p.DropoffLng),
		"drop_address":    p.DropAddress,
		"payment_mode":    p.PaymentMode,
		"body":            fmt.Sprintf("%.1f km · ₹%.0f", p.TripDistanceKm, p.DriverShare),
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
