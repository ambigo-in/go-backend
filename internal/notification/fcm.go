package notification

import (
	"context"
	"fmt"
	"time"

	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/metrics"
	"ambigo-backend/internal/retry"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"github.com/sony/gobreaker"
	"google.golang.org/api/option"
)

// FCMClient sends push notifications via the Firebase Admin SDK.
type FCMClient struct {
	client  *messaging.Client
	breaker *gobreaker.CircuitBreaker
}

func newFCMBreaker() *gobreaker.CircuitBreaker {
	return gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "fcm",
		MaxRequests: 20,
		Interval:    60 * time.Second,
		Timeout:     30 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			if counts.Requests < 10 {
				return false
			}
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return failureRatio >= 0.5
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			logger.Log.Info().Str("client", name).Str("from", from.String()).Str("to", to.String()).Msg("circuit breaker state changed")
			var val float64
			switch to {
			case gobreaker.StateClosed:
				val = 0
			case gobreaker.StateOpen:
				val = 1
			case gobreaker.StateHalfOpen:
				val = 2
			}
			metrics.CircuitBreakerState.WithLabelValues(name).Set(val)
			metrics.CircuitBreakerTransitions.WithLabelValues(name, from.String(), to.String()).Inc()
		},
	})
}

func NewFCMClient(ctx context.Context, credentialsPath string) *FCMClient {
	if credentialsPath == "" {
		logger.Log.Warn().Msg("FIREBASE_CREDENTIALS_PATH is empty. Push notifications are disabled.")
		return &FCMClient{client: nil, breaker: newFCMBreaker()}
	}

	opt := option.WithCredentialsFile(credentialsPath)
	app, err := firebase.NewApp(ctx, nil, opt)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to initialize Firebase app")
		return &FCMClient{client: nil, breaker: newFCMBreaker()}
	}

	client, err := app.Messaging(ctx)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to initialize Firebase Messaging client")
		return &FCMClient{client: nil, breaker: newFCMBreaker()}
	}

	logger.Log.Info().Msg("Successfully initialized Firebase Admin SDK.")
	return &FCMClient{client: client, breaker: newFCMBreaker()}
}

// SendAlertMessage sends a data message with a visible notification block so
// backgrounded or killed apps surface it in the system tray automatically.
// Foreground apps receive data only (no auto-display) and must render their
// own overlay/dialog from the data payload.
func (f *FCMClient) SendAlertMessage(ctx context.Context, token, title, body string, data map[string]string) error {
	if f.client == nil {
		return nil
	}

	if f.breaker != nil && f.breaker.State() == gobreaker.StateOpen {
		return fmt.Errorf("fcm circuit breaker open: %w", gobreaker.ErrOpenState)
	}

	_, err := f.breaker.Execute(func() (interface{}, error) {
		return nil, retry.Do(ctx, retry.Default, func(ctx context.Context) error {
			ctxTimeout, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			if data == nil {
				data = map[string]string{}
			}
			data["title"] = title
			data["body"] = body

			message := &messaging.Message{
				Token: token,
				Data:  data,
				Notification: &messaging.Notification{
					Title: title,
					Body:  body,
				},
				Android: &messaging.AndroidConfig{
					Priority: "high",
					Notification: &messaging.AndroidNotification{
						ChannelID:    "high_importance_channel",
						Priority:     messaging.PriorityMax,
						Sound:        "default",
						DefaultSound: true,
					},
				},
			}

			response, err := f.client.Send(ctxTimeout, message)
			if err != nil {
				return fmt.Errorf("fcm send error: %v", err)
			}

			logger.Log.Info().Str("message_id", response).Msg("FCM alert sent successfully")
			return nil
		})
	})
	if err != nil {
		return err
	}
	return nil
}

// SendDataMessage sends an FCM data message to a specific device token.
func (f *FCMClient) SendDataMessage(ctx context.Context, token string, data map[string]string) error {
	if f.client == nil {
		return nil
	}

	if f.breaker != nil && f.breaker.State() == gobreaker.StateOpen {
		return fmt.Errorf("fcm circuit breaker open: %w", gobreaker.ErrOpenState)
	}

	_, err := f.breaker.Execute(func() (interface{}, error) {
		return nil, retry.Do(ctx, retry.Default, func(ctx context.Context) error {
			ctxTimeout, cancel := context.WithTimeout(ctx, 10*time.Second)
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

			response, err := f.client.Send(ctxTimeout, message)
			if err != nil {
				return fmt.Errorf("fcm send error: %v", err)
			}

			logger.Log.Info().Str("message_id", response).Msg("FCM message sent successfully")
			return nil
		})
	})
	if err != nil {
		return err
	}
	return nil
}
