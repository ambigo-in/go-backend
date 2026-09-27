package ride

import (
	"time"
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
	Type        string    `db:"type" json:"type"`
	Coordinates []float64 `db:"coordinates" json:"coordinates"` // [longitude, latitude]
}

type Route struct {
	DistanceKm      float64 `db:"route_distance_km" json:"distance_km"`
	DurationSeconds int     `db:"route_duration_seconds" json:"duration_seconds"`
	Polyline        string  `db:"route_polyline" json:"polyline"`
}

type Fare struct {
	BaseFare           float64 `db:"fare_base" json:"base_fare"`
	DistanceFare       float64 `db:"fare_distance" json:"distance_fare"`
	EmergencySurcharge float64 `db:"fare_emergency" json:"emergency_surcharge"`
	NightSurcharge     float64 `db:"fare_night" json:"night_surcharge"`
	WaitingCharge      float64 `db:"fare_waiting" json:"waiting_charge"`
	Total              float64 `db:"fare_total" json:"total"`
	DriverShare        float64 `db:"fare_driver_share" json:"driver_share"`
	ReferralDiscount   float64 `db:"fare_referral_discount" json:"referral_discount,omitempty"`
	Currency           string  `db:"fare_currency" json:"currency"`
}

type TimeLog struct {
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
	AssignedAt  *time.Time `db:"assigned_at" json:"assigned_at,omitempty"`
	ArrivedAt   *time.Time `db:"arrived_at" json:"arrived_at,omitempty"`
	StartedAt   *time.Time `db:"started_at" json:"started_at,omitempty"`
	CompletedAt *time.Time `db:"completed_at" json:"completed_at,omitempty"`
	CancelledAt *time.Time `db:"cancelled_at" json:"cancelled_at,omitempty"`
}

type ConditionUpdate struct {
	Level     string    `db:"level" json:"level"` // stable | serious | critical | worsening
	Severity  int       `db:"severity" json:"severity"`
	Note      string    `db:"note" json:"note,omitempty"`
	Source    string    `db:"source" json:"source"` // user | attendant
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type DispatchMetadata struct {
	CandidatesSearched  int `db:"dispatch_candidates_searched" json:"candidates_searched"`
	OffersSent          int `db:"dispatch_offers_sent" json:"offers_sent"`
	OffersDeclined      int `db:"dispatch_offers_declined" json:"offers_declined"`
	OffersTimedOut      int `db:"dispatch_offers_timed_out" json:"offers_timed_out"`
	AssignmentLatencyMs int `db:"dispatch_assignment_latency_ms" json:"assignment_latency_ms"`
}

func ConditionSeverity(level string) int {
	switch level {
	case "stable":
		return 1
	case "serious":
		return 2
	case "critical":
		return 3
	case "worsening":
		return 4
	default:
		return 0
	}
}

// Ride represents the V2 single-collection document schema, now persisted in Postgres.
// Normalized columns per migrations/00001_init.sql:
//   route_* (route_distance_km, route_duration_seconds, route_polyline)
//   fare_* (fare_base, fare_distance, fare_emergency, fare_night, fare_waiting, fare_total, fare_driver_share, fare_referral_discount, fare_currency)
//   time.* -> created_at, assigned_at, arrived_at, started_at, completed_at, cancelled_at
//   dispatch_* (dispatch_candidates_searched, dispatch_offers_sent, dispatch_offers_declined, dispatch_offers_timed_out, dispatch_assignment_latency_ms)
// JSONB columns: pickup, drop (GeoJSONPoint), available_types, latest_condition, condition_on_arrival
type Ride struct {
	ID                string           `db:"id" json:"_id"`
	UserID            string           `db:"user_id" json:"user_id"`
	DriverID          *string          `db:"driver_id" json:"driver_id,omitempty"`
	AmbTypeID         *string          `db:"amb_type_id" json:"amb_type_id,omitempty"`
	HospitalID        *string          `db:"hospital_id" json:"hospital_id,omitempty"`
	StartOTP          string           `db:"start_otp" json:"start_otp,omitempty"`
	Status            RideStatus       `db:"status" json:"status"`
	Pickup            GeoJSONPoint     `db:"pickup" json:"pickup"`
	PickupAddress     string           `db:"pickup_address" json:"pickup_address"`
	PickupH3Cell      string           `db:"pickup_h3_cell" json:"pickup_h3_cell"`
	Drop              GeoJSONPoint     `db:"drop" json:"drop"`
	DropAddress       string           `db:"drop_address" json:"drop_address"`
	Route             *Route           `db:"route_distance_km" json:"route,omitempty"`
	Fare              *Fare            `db:"fare_total" json:"fare,omitempty"`
	EmergencyType     *string          `db:"emergency_type" json:"emergency_type,omitempty"`
	EmergencyPriority int              `db:"emergency_priority" json:"emergency_priority"`
	PaymentMode       string           `db:"payment_mode" json:"payment_mode"` // "cash" | "online"
	PaymentID         *string          `db:"payment_id" json:"payment_id,omitempty"`
	RegionID          *string          `db:"region_id" json:"region_id,omitempty"`
	// OtpRequired is the resolved type+region OTP flag, computed per request.
	// Transient: never stored, never scanned (db:"-" skips pgx struct mapping;
	// all SQL uses explicit column lists).
	OtpRequired *bool `db:"-" json:"otp_required,omitempty"`
	Time              TimeLog          `db:"created_at" json:"time"`
	DispatchMetadata  DispatchMetadata `db:"dispatch_candidates_searched" json:"dispatch_metadata"`
	CancellationReason string          `db:"cancellation_reason" json:"cancellation_reason,omitempty"`
	AvailableTypes     []string         `db:"available_types" json:"available_types,omitempty"`
	ConditionUpdates   []ConditionUpdate `db:"condition_updates" json:"condition_updates,omitempty"`
	LatestCondition    *ConditionUpdate `db:"latest_condition" json:"latest_condition,omitempty"`
	ConditionOnArrival *ConditionUpdate `db:"condition_on_arrival" json:"condition_on_arrival,omitempty"`
}
