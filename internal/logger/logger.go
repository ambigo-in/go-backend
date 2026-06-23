package logger

import (
	"os"
	"time"

	"github.com/rs/zerolog"
)

var Log zerolog.Logger

func init() {
	output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
	Log = zerolog.New(output).With().Timestamp().Logger()
}

func NewRideLogger(rideID string) zerolog.Logger {
	return Log.With().Str("ride_id", rideID).Logger()
}

func NewUserLogger(userID string) zerolog.Logger {
	return Log.With().Str("user_id", userID).Logger()
}
