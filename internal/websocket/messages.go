package websocket

import "encoding/json"

// Event types
const (
	EventLocationUpdate = "LOCATION_UPDATE"
	EventWatchRide      = "WATCH_RIDE"
	EventRideUpdate     = "RIDE_UPDATE"
	EventRideRequested  = "RIDE_REQUESTED"
	EventRideAccepted   = "RIDE_ACCEPTED"
	EventRideDeclined   = "RIDE_DECLINED"
	EventSOSAlert       = "SOS_ALERT"
	EventError          = "ERROR"
)

// BaseMessage is the generic envelope for all websocket communications
type BaseMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// LocationUpdatePayload is sent by drivers continuously
type LocationUpdatePayload struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// RideRequestedPayload is broadcast to nearby drivers
type RideRequestedPayload struct {
	RideID     string  `json:"ride_id"`
	PickupLat  float64 `json:"pickup_lat"`
	PickupLng  float64 `json:"pickup_lng"`
	DropoffLat float64 `json:"dropoff_lat"`
	DropoffLng float64 `json:"dropoff_lng"`
	ETASeconds int     `json:"eta_seconds"`
	DistanceKm float64 `json:"distance_km"`
	Fare       float64 `json:"fare"`
}

// ErrorPayload is sent to the client if something goes wrong
type ErrorPayload struct {
	Message string `json:"message"`
}
