package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"ambigo-backend/internal/auth"
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
}

func (n *FCMNotifier) handleRideOffered(payload []byte) {
	var p RideDriverOfferedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		log.Printf("[FCMNotifier] Unmarshal error (ride:driver_offered): %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	token, err := n.authStore.GetDriverFCMToken(ctx, p.DriverID)
	if err != nil {
		log.Printf("[FCMNotifier] Failed to get FCM token for driver %s: %v", p.DriverID, err)
		return
	}
	if token == nil || *token == "" {
		return
	}

	data := map[string]string{
		"ride_id":    p.RideID,
		"body":       fmt.Sprintf("Estimated fare: ₹%.2f", p.Fare),
	}
	if p.IsSOS {
		data["title"] = "EMERGENCY ALERT"
		data["is_sos"] = "true"
	} else {
		data["title"] = "New Ride Request"
		data["is_sos"] = "false"
	}

	if err := n.fcmClient.SendDataMessage(ctx, *token, data); err != nil {
		log.Printf("[FCMNotifier] FCM push failed for driver %s: %v", p.DriverID, err)
	}
}

func (n *FCMNotifier) handleDriverApproved(payload []byte) {
	var p AuthDriverApprovedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		log.Printf("[FCMNotifier] Unmarshal error (auth:driver_approved): %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	token, err := n.authStore.GetDriverFCMToken(ctx, p.DriverID)
	if err != nil || token == nil || *token == "" {
		return
	}

	data := map[string]string{
		"title": "Welcome to Ambigo!",
		"body":  "Your driver account has been approved. You can now accept rides.",
	}

	if err := n.fcmClient.SendDataMessage(ctx, *token, data); err != nil {
		log.Printf("[FCMNotifier] Welcome FCM push failed for driver %s: %v", p.DriverID, err)
	}
}
