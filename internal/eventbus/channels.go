package eventbus

// Channel constants for EventBus pub/sub
const (
	ChannelRideRequested        = "ride:requested"
	ChannelRideDriverOffered    = "ride:driver_offered"
	ChannelRideAccepted         = "ride:accepted"
	ChannelRideArrived          = "ride:arrived"
	ChannelRideStarted          = "ride:started"
	ChannelRideCompleted        = "ride:completed"
	ChannelRideCancelled        = "ride:cancelled"
	ChannelAuthOTPRequested     = "auth:otp_requested"
	ChannelAuthUserRegistered   = "auth:user_registered"
	ChannelAuthUserLoggedIn     = "auth:user_logged_in"
	ChannelAuthDriverCreated    = "auth:driver_created"
	ChannelAuthDriverLoggedIn   = "auth:driver_logged_in"
	ChannelAuthDriverApproved   = "auth:driver_approved"
	ChannelPaymentCompleted     = "payment:completed"
	ChannelWalletWithdrawal     = "wallet:withdrawal"
	ChannelDriverLocationUpdate = "driver:location_updated"
	ChannelAdminAmbTypeCreated  = "admin:ambulance_type_created"
	ChannelAdminAmbTypeDeleted  = "admin:ambulance_type_deleted"
	ChannelAdminHospitalAdded   = "admin:hospital_added"
	ChannelAdminHospitalUpdated = "admin:hospital_updated"
	ChannelAdminHospitalDeleted = "admin:hospital_deleted"
	ChannelAdminOfferCreated    = "admin:offer_created"
	ChannelAdminOfferDeleted    = "admin:offer_deleted"
)

// RideRequestedPayload is published when a user requests a ride
type RideRequestedPayload struct {
	RideID        string  `json:"ride_id"`
	UserID        string  `json:"user_id"`
	PickupLat     float64 `json:"pickup_lat"`
	PickupLng     float64 `json:"pickup_lng"`
	PickupAddress string  `json:"pickup_address"`
	DropoffLat    float64 `json:"dropoff_lat"`
	DropoffLng    float64 `json:"dropoff_lng"`
	DropAddress   string  `json:"drop_address"`
	AmbTypeID     string  `json:"amb_type_id"`
	HospitalID    string  `json:"hospital_id"`
	PaymentMode   string  `json:"payment_mode"`
	IsSOS         bool    `json:"is_sos"`
	Fare          float64 `json:"fare"`
	DriverShare   float64 `json:"driver_share"`
	DistanceKm    float64 `json:"distance_km"`
	RequestID     string  `json:"request_id,omitempty"`
}

// RideDriverOfferedPayload is published when a ride is offered to a specific driver
type RideDriverOfferedPayload struct {
	RideID           string  `json:"ride_id"`
	DriverID         string  `json:"driver_id"`
	UserID           string  `json:"user_id"`
	PickupLat        float64 `json:"pickup_lat"`
	PickupLng        float64 `json:"pickup_lng"`
	PickupAddress    string  `json:"pickup_address"`
	DropoffLat       float64 `json:"dropoff_lat"`
	DropoffLng       float64 `json:"dropoff_lng"`
	DropAddress      string  `json:"drop_address"`
	ETASeconds       int     `json:"eta_seconds"`
	PickupDistanceKm float64 `json:"pickup_distance_km"`
	TripDistanceKm   float64 `json:"trip_distance_km"`
	Fare             float64 `json:"fare"`
	DriverShare      float64 `json:"driver_share"`
	PaymentMode      string  `json:"payment_mode"`
	IsSOS            bool    `json:"is_sos"`
	RequestID        string  `json:"request_id,omitempty"`
}

// RideAcceptedPayload is published when a driver accepts a ride
type RideAcceptedPayload struct {
	RideID    string `json:"ride_id"`
	DriverID  string `json:"driver_id"`
	UserID    string `json:"user_id"`
	Status    string `json:"status"`
	RequestID string `json:"request_id,omitempty"`
}

// RideStatusChangedPayload is used for arrived, started status transitions
type RideStatusChangedPayload struct {
	RideID    string `json:"ride_id"`
	UserID    string `json:"user_id,omitempty"`
	DriverID  string `json:"driver_id,omitempty"`
	Status    string `json:"status"`
	RequestID string `json:"request_id,omitempty"`
}

// RideCompletedPayload is published when a ride is completed
type RideCompletedPayload struct {
	RideID      string  `json:"ride_id"`
	DriverID    string  `json:"driver_id"`
	UserID      string  `json:"user_id"`
	PaymentMode string  `json:"payment_mode"`
	FinalAmount float64 `json:"final_amount"`
	DriverShare float64 `json:"driver_share"`
	DropAddress string  `json:"drop_address"`
	RequestID   string  `json:"request_id,omitempty"`
}

// RideCancelledPayload is published when a ride is cancelled
type RideCancelledPayload struct {
	RideID    string `json:"ride_id"`
	Reason    string `json:"reason"`
	UserID    string `json:"user_id,omitempty"`
	DriverID  string `json:"driver_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// AuthOTPRequestedPayload is published when an OTP is sent
type AuthOTPRequestedPayload struct {
	Mobile    string `json:"mobile"`
	Role      string `json:"role"`
	RequestID string `json:"request_id,omitempty"`
}

// AuthUserRegisteredPayload is published when a new user is created
type AuthUserRegisteredPayload struct {
	UserID    string `json:"user_id"`
	Mobile    string `json:"mobile"`
	Name      string `json:"name"`
	RequestID string `json:"request_id,omitempty"`
}

// AuthUserLoggedInPayload is published when a user logs in
type AuthUserLoggedInPayload struct {
	UserID    string `json:"user_id"`
	Mobile    string `json:"mobile"`
	RequestID string `json:"request_id,omitempty"`
}

// AuthDriverCreatedPayload is published when a new unverified driver is created
type AuthDriverCreatedPayload struct {
	DriverID  string `json:"driver_id"`
	Mobile    string `json:"mobile"`
	Name      string `json:"name"`
	RequestID string `json:"request_id,omitempty"`
}

// AuthDriverLoggedInPayload is published when a driver logs in
type AuthDriverLoggedInPayload struct {
	DriverID  string `json:"driver_id"`
	Mobile    string `json:"mobile"`
	Role      string `json:"role"`
	RequestID string `json:"request_id,omitempty"`
}

// AuthDriverApprovedPayload is published when an admin approves a driver
type AuthDriverApprovedPayload struct {
	DriverID  string `json:"driver_id"`
	Name      string `json:"name"`
	Mobile    string `json:"mobile"`
	RequestID string `json:"request_id,omitempty"`
}

// PaymentCompletedPayload is published when a payment is processed
type PaymentCompletedPayload struct {
	PaymentID string  `json:"payment_id"`
	RideID    string  `json:"ride_id"`
	UserID    string  `json:"user_id"`
	DriverID  string  `json:"driver_id"`
	Amount    float64 `json:"amount"`
	Mode      string  `json:"mode"`
	RequestID string  `json:"request_id,omitempty"`
}

// WalletWithdrawalPayload is published when a driver initiates a withdrawal
type WalletWithdrawalPayload struct {
	DriverID  string  `json:"driver_id"`
	Amount    float64 `json:"amount"`
	Status    string  `json:"status"`
	RequestID string  `json:"request_id,omitempty"`
}

// DriverLocationUpdatePayload is published when a driver sends a GPS ping
type DriverLocationUpdatePayload struct {
	DriverID  string  `json:"driver_id"`
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	RideID    string  `json:"ride_id,omitempty"`
	RequestID string  `json:"request_id,omitempty"`
}

// AdminAmbTypePayload is published for ambulance type CRUD events
type AdminAmbTypePayload struct {
	AmbTypeID string `json:"amb_type_id"`
	Name      string `json:"name,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// AdminHospitalPayload is published for hospital CRUD events
type AdminHospitalPayload struct {
	HospitalID string `json:"hospital_id"`
	Name       string `json:"name,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
}

// AdminOfferPayload is published for offer CRUD events
type AdminOfferPayload struct {
	OfferID     string `json:"offer_id"`
	Description string `json:"description,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
}
