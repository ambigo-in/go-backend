package notification

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/metrics"
	"ambigo-backend/internal/retry"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"github.com/sony/gobreaker"
	"google.golang.org/api/option"
)

// isPermanentFCMError checks if an FCM error is a client-side permanent error
// (such as an unregistered, expired, or invalid device token) that will not
// succeed on retry and should not trip the global infrastructure circuit breaker.
func isPermanentFCMError(err error) bool {
	if err == nil {
		return false
	}
	var nre *retry.NonRetryableError
	if errors.As(err, &nre) {
		err = nre.Err
	}
	if messaging.IsUnregistered(err) || messaging.IsInvalidArgument(err) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "NotRegistered") ||
		strings.Contains(msg, "Unregistered") ||
		strings.Contains(msg, "InvalidArgument") ||
		strings.Contains(msg, "registration-token-not-registered")
}

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

// iosSilentPayload asks iOS to wake the app so its FCM background handler runs.
// Without content-available iOS drops a data-only push on the floor for any app
// that is not in the foreground.
func iosSilentPayload() *messaging.APNSConfig {
	return &messaging.APNSConfig{
		Payload: &messaging.APNSPayload{Aps: &messaging.Aps{ContentAvailable: true}},
	}
}

// iosAlertPayload makes iOS draw the banner itself, which is the only way a
// locked screen or an app the user swiped away shows anything. The alert goes in
// the aps dict rather than the shared Notification field because that field also
// switches on Android's auto-display, and both apps build their own Android
// notification from the data map in MyFirebaseMessagingService — setting it
// would show riders and drivers two notifications per event.
//
// content-available is deliberately not combined with the alert: when a push
// carries aps.alert iOS shows it without running the app, so each message picks
// one of the two modes.
func iosAlertPayload(title, body string) *messaging.APNSConfig {
	return &messaging.APNSConfig{
		Payload: &messaging.APNSPayload{Aps: &messaging.Aps{
			Alert: &messaging.ApsAlert{Title: title, Body: body},
			Sound: "default",
		}},
	}
}

// send delivers message through the circuit breaker and the retry policy.
func (f *FCMClient) send(ctx context.Context, message *messaging.Message) error {
	if f.client == nil {
		return nil
	}

	if f.breaker != nil && f.breaker.State() == gobreaker.StateOpen {
		return fmt.Errorf("fcm circuit breaker open: %w", gobreaker.ErrOpenState)
	}

	_, err := f.breaker.Execute(func() (interface{}, error) {
		sendErr := retry.Do(ctx, retry.Default, func(ctx context.Context) error {
			ctxTimeout, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			response, err := f.client.Send(ctxTimeout, message)
			if err != nil {
				if isPermanentFCMError(err) {
					logger.Log.Warn().Err(err).Msg("Permanent FCM error (unregistered/invalid token), skipping retry")
					return &retry.NonRetryableError{Err: err}
				}
				return fmt.Errorf("fcm send error: %v", err)
			}

			logger.Log.Info().Str("message_id", response).Msg("FCM message sent successfully")
			return nil
		})

		if sendErr != nil && isPermanentFCMError(sendErr) {
			return nil, nil
		}
		return nil, sendErr
	})
	return err
}

// dataMessage builds the platform-neutral half of a data push. Pushes with no
// ride_id have no app-drawn UI on Android, so the system tray shows them and
// needs the text as well as the channel. SESSION_REVOKED is excluded: a
// notification block makes the tray swallow the message instead of calling
// onMessageReceived, and that push exists to make the app wipe its credentials.
func dataMessage(token string, data map[string]string, apns *messaging.APNSConfig) *messaging.Message {
	message := &messaging.Message{
		Token:   token,
		Data:    data,
		Android: &messaging.AndroidConfig{Priority: "high"},
		APNS:    apns,
	}

	if data["ride_id"] == "" && data["type"] != "SESSION_REVOKED" {
		message.Android.Notification = &messaging.AndroidNotification{
			ChannelID:    "high_importance_channel",
			Priority:     messaging.PriorityMax,
			Sound:        "default",
			DefaultSound: true,
			Title:        data["title"],
			Body:         data["body"],
		}
	}

	return message
}

// SendAlertMessage sends a data message with a visible notification block so
// backgrounded or killed apps surface it in the system tray automatically.
// Foreground apps receive data only (no auto-display) and must render their
// own overlay/dialog from the data payload.
func (f *FCMClient) SendAlertMessage(ctx context.Context, token, title, body string, data map[string]string) error {
	if data == nil {
		data = map[string]string{}
	}
	data["title"] = title
	data["body"] = body

	return f.send(ctx, &messaging.Message{
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
		// The Notification block already gives iOS its banner, but iOS plays
		// nothing unless the aps dict also asks for a sound.
		APNS: &messaging.APNSConfig{
			Payload: &messaging.APNSPayload{Aps: &messaging.Aps{Sound: "default"}},
		},
	})
}

// SendDataMessage sends a data message for the pushes the app is expected to
// act on rather than announce: iOS wakes the app and its background handler
// renders whatever UI is wanted.
func (f *FCMClient) SendDataMessage(ctx context.Context, token string, data map[string]string) error {
	return f.send(ctx, dataMessage(token, data, iosSilentPayload()))
}

// SendVisibleDataMessage is SendDataMessage plus an iOS-drawn banner, for the
// pushes that have to reach a locked screen or an app the user killed.
func (f *FCMClient) SendVisibleDataMessage(ctx context.Context, token string, data map[string]string) error {
	return f.send(ctx, dataMessage(token, data, iosAlertPayload(data["title"], data["body"])))
}
