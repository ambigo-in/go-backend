package notification

import (
	"context"
	"fmt"
	"time"

	"ambigo-backend/internal/logger"

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
		logger.Log.Warn().Msg("FIREBASE_CREDENTIALS_PATH is empty. Push notifications are disabled.")
		return &FCMClient{client: nil}
	}

	opt := option.WithCredentialsFile(credentialsPath)
	app, err := firebase.NewApp(ctx, nil, opt)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to initialize Firebase app")
		return &FCMClient{client: nil}
	}

	client, err := app.Messaging(ctx)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to initialize Firebase Messaging client")
		return &FCMClient{client: nil}
	}

	logger.Log.Info().Msg("Successfully initialized Firebase Admin SDK.")
	return &FCMClient{client: client}
}

// SendDataMessage sends an FCM data message to a specific device token.
func (f *FCMClient) SendDataMessage(ctx context.Context, token string, data map[string]string) error {
	if f.client == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	message := &messaging.Message{
		Token: token,
		Data:  data,
		Android: &messaging.AndroidConfig{
			Priority: "high",
		},
	}

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

	logger.Log.Info().Str("message_id", response).Msg("FCM message sent successfully")
	return nil
}
