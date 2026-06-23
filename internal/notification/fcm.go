package notification

import (
	"context"
	"fmt"
	"log"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"
)

// FCMClient sends push notifications via the Firebase Admin SDK.
type FCMClient struct {
	client *messaging.Client
}

func NewFCMClient(ctx context.Context, credentialsPath string) *FCMClient {
	if credentialsPath == "" {
		log.Println("[FCM] FIREBASE_CREDENTIALS_PATH is empty. Push notifications are disabled.")
		return &FCMClient{client: nil}
	}

	opt := option.WithCredentialsFile(credentialsPath)
	app, err := firebase.NewApp(ctx, nil, opt)
	if err != nil {
		log.Printf("[FCM] Failed to initialize Firebase app: %v\n", err)
		return &FCMClient{client: nil}
	}

	client, err := app.Messaging(ctx)
	if err != nil {
		log.Printf("[FCM] Failed to initialize Firebase Messaging client: %v\n", err)
		return &FCMClient{client: nil}
	}

	log.Println("[FCM] Successfully initialized Firebase Admin SDK.")
	return &FCMClient{client: client}
}

// SendDataMessage sends an FCM data message to a specific device token.
func (f *FCMClient) SendDataMessage(ctx context.Context, token string, data map[string]string) error {
	if f.client == nil {
		return nil
	}

	// We pass a 10 second timeout on top of whatever context is passed, just to be safe.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Convert boolean values strings since FCM data requires strictly string values
	// This was already done in the caller, but good to be aware.

	message := &messaging.Message{
		Token: token,
		Data:  data,
		Android: &messaging.AndroidConfig{
			Priority: "high",
		},
	}

	// Only attach a visible system Notification payload if this is NOT a ride request.
	// For ride requests, we MUST send a "Data-only" message, otherwise Android will 
	// swallow the push into the system tray and never trigger onMessageReceived() in the background!
	if data["ride_id"] == "" {
		message.Android.Notification = &messaging.AndroidNotification{
			ChannelID:    "high_importance_channel",
			Priority:     messaging.PriorityMax,
			Sound:        "default",
			DefaultSound: true,
		}
	}

	response, err := f.client.Send(ctx, message)
	if err != nil {
		return fmt.Errorf("fcm send error: %v", err)
	}

	log.Printf("[FCM] Successfully sent message ID: %s\n", response)
	return nil
}
