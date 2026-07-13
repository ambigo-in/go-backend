package ride

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type RideStatus string

const (
	StatusSearching  RideStatus = "SEARCHING"
	StatusAssigned   RideStatus = "ASSIGNED"
	StatusArrived    RideStatus = "ARRIVED"
	StatusInProgress RideStatus = "IN_PROGRESS"
	StatusCompleted  RideStatus = "COMPLETED"
	StatusCancelled  RideStatus = "CANCELLED"
)

type GeoJSONPoint struct {
	Type        string    `bson:"type" json:"type"`
	Coordinates []float64 `bson:"coordinates" json:"coordinates"` // [longitude, latitude]
}

type Route struct {
	DistanceKm      float64 `bson:"distance_km" json:"distance_km"`
	DurationSeconds int     `bson:"duration_seconds" json:"duration_seconds"`
	Polyline        string  `bson:"polyline" json:"polyline"`
}

type Fare struct {
	BaseFare           float64 `bson:"base_fare" json:"base_fare"`
	DistanceFare       float64 `bson:"distance_fare" json:"distance_fare"`
	EmergencySurcharge float64 `bson:"emergency_surcharge" json:"emergency_surcharge"`
	NightSurcharge     float64 `bson:"night_surcharge" json:"night_surcharge"`
	WaitingCharge      float64 `bson:"waiting_charge" json:"waiting_charge"`
	Total              float64 `bson:"total" json:"total"`
	DriverShare        float64 `bson:"driver_share" json:"driver_share"`
	Currency           string  `bson:"currency" json:"currency"`
}

type TimeLog struct {
	CreatedAt   time.Time  `bson:"created_at" json:"created_at"`
	AssignedAt  *time.Time `bson:"assigned_at,omitempty" json:"assigned_at,omitempty"`
	ArrivedAt   *time.Time `bson:"arrived_at,omitempty" json:"arrived_at,omitempty"`
	StartedAt   *time.Time `bson:"started_at,omitempty" json:"started_at,omitempty"`
	CompletedAt *time.Time `bson:"completed_at,omitempty" json:"completed_at,omitempty"`
	CancelledAt *time.Time `bson:"cancelled_at,omitempty" json:"cancelled_at,omitempty"`
}

type DispatchMetadata struct {
	CandidatesSearched  int `bson:"candidates_searched" json:"candidates_searched"`
	OffersSent          int `bson:"offers_sent" json:"offers_sent"`
	OffersDeclined      int `bson:"offers_declined" json:"offers_declined"`
	OffersTimedOut      int `bson:"offers_timed_out" json:"offers_timed_out"`
	AssignmentLatencyMs int `bson:"assignment_latency_ms" json:"assignment_latency_ms"`
}

// Ride represents the V2 single-collection document schema
type Ride struct {
	ID                primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	UserID            string             `bson:"user_id" json:"user_id"`
	DriverID          *string            `bson:"driver_id,omitempty" json:"driver_id,omitempty"`
	AmbTypeID         *string            `bson:"amb_type_id,omitempty" json:"amb_type_id,omitempty"`
	HospitalID        *string            `bson:"hospital_id,omitempty" json:"hospital_id,omitempty"`
	StartOTP          string             `bson:"start_otp,omitempty" json:"start_otp,omitempty"`
	Status            RideStatus         `bson:"status" json:"status"`
	Pickup            GeoJSONPoint       `bson:"pickup" json:"pickup"`
	PickupAddress     string             `bson:"pickup_address" json:"pickup_address"`
	PickupH3Cell      string             `bson:"pickup_h3_cell" json:"pickup_h3_cell"`
	Drop              GeoJSONPoint       `bson:"drop" json:"drop"`
	DropAddress       string             `bson:"drop_address" json:"drop_address"`
	Route             *Route             `bson:"route,omitempty" json:"route,omitempty"`
	Fare              *Fare              `bson:"fare,omitempty" json:"fare,omitempty"`
	EmergencyType     *string            `bson:"emergency_type,omitempty" json:"emergency_type,omitempty"`
	EmergencyPriority int                `bson:"emergency_priority" json:"emergency_priority"`
	PaymentMode       string             `bson:"payment_mode" json:"payment_mode"` // "cash" | "online"
	PaymentID         *string            `bson:"payment_id,omitempty" json:"payment_id,omitempty"`
	Time              TimeLog            `bson:"time" json:"time"`
	DispatchMetadata  DispatchMetadata   `bson:"dispatch_metadata" json:"dispatch_metadata"`
	CancellationReason string            `bson:"cancellation_reason,omitempty" json:"cancellation_reason,omitempty"`
}
