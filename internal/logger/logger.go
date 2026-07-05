package logger

import (
	"context"
	"os"

	"ambigo-backend/internal/requestid"

	"github.com/rs/zerolog"
)

var Log zerolog.Logger

func init() {
	Log = zerolog.New(os.Stdout).With().Timestamp().Logger()
}

func Ctx(ctx context.Context) zerolog.Logger {
	l := Log.With()
	if reqID := requestid.FromContext(ctx); reqID != "" {
		l = l.Str("request_id", reqID)
	}
	return l.Logger()
}

func NewRideLogger(rideID string) zerolog.Logger {
	return Log.With().Str("ride_id", rideID).Logger()
}

func NewUserLogger(userID string) zerolog.Logger {
	return Log.With().Str("user_id", userID).Logger()
}
