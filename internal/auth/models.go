package auth

import (
	"time"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// GeoJSONPoint represents a MongoDB geospatial point
type GeoJSONPoint struct {
	Type        string    `bson:"type" json:"type"`
	Coordinates []float64 `bson:"coordinates" json:"coordinates"` // [longitude, latitude]
}

// User represents a rider profile
type User struct {
	ID           primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Name         string             `bson:"name" json:"name"`
	Mobile       string             `bson:"mobile" json:"mobile"`
	ReferralCode string             `bson:"referral_code" json:"referral_code"`
	Location     *GeoJSONPoint      `bson:"location,omitempty" json:"location,omitempty"`
	FCMToken     *string            `bson:"fcm_token,omitempty" json:"fcm_token,omitempty"`
	JWTToken     *string            `bson:"jwt_token,omitempty" json:"jwt_token,omitempty"`
}

// DriverDetails contains the document URLs for verification
type DriverDetails struct {
	POIImage  string `bson:"poi_image" json:"poi_image"`
	RCNumber  string `bson:"rc_number" json:"rc_number"`
	RCImage   string `bson:"rc_image" json:"rc_image"`
	DLNumber  string `bson:"dl_number" json:"dl_number"`
	DLImage   string `bson:"dl_image" json:"dl_image"`
	AmbFront  string `bson:"amb_front,omitempty" json:"amb_front,omitempty"`
	AmbInside string `bson:"amb_inside,omitempty" json:"amb_inside,omitempty"`
}

// WalletDetails tracks a driver's Zwitch banking configuration
type WalletDetails struct {
	AccountNo string `bson:"account_no" json:"account_no" validate:"required"`
	BenfName  string `bson:"benf_name" json:"benf_name" validate:"required"`
	IFSCCode  string `bson:"ifsc_code" json:"ifsc_code" validate:"required"`
	BenfID    string `bson:"benf_id" json:"benf_id"`
}

// Driver represents a fully verified active driver profile
type Driver struct {
	ID                 primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Name               string             `bson:"name" json:"name" validate:"required"`
	Mobile             string             `bson:"mobile" json:"mobile" validate:"required"`
	Photo              string             `bson:"photo" json:"photo"`
	VehicleType        string             `bson:"vehicle_type" json:"vehicle_type" validate:"required"`
	VehicleReg         string             `bson:"vehicle_registration" json:"vehicle_registration" validate:"required"`
	WalletDetails      WalletDetails      `bson:"wallet_details" json:"wallet_details"`
	WalletBalance      float64            `bson:"wallet_balance" json:"wallet_balance"`
	ReferralCode       string             `bson:"referral_code" json:"referral_code"`
	Location           *GeoJSONPoint      `bson:"location,omitempty" json:"location,omitempty"`
	FCMToken           *string            `bson:"fcm_token,omitempty" json:"fcm_token,omitempty"`
	JWTToken           *string            `bson:"jwt_token,omitempty" json:"jwt_token,omitempty"`
	LastLocationUpdate *time.Time         `bson:"last_location_update,omitempty" json:"last_location_update,omitempty"`
	Details            *DriverDetails     `bson:"details,omitempty" json:"details,omitempty"`
}

// UnverifiedDriver represents a driver in the onboarding pipeline
type UnverifiedDriver struct {
	ID                 primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Name               string             `bson:"name" json:"name" validate:"required"`
	Mobile             string             `bson:"mobile" json:"mobile" validate:"required"`
	PortraitImage      string             `bson:"portrait_image" json:"portrait_image"`
	POIImage           string             `bson:"poi_image" json:"poi_image"`
	DLImage            string             `bson:"dl_image" json:"dl_image"`
	RCImage            string             `bson:"rc_image" json:"rc_image"`
	AmbFront           string             `bson:"amb_front" json:"amb_front"`
	AmbInside          string             `bson:"amb_inside" json:"amb_inside"`
	VehicleType        string             `bson:"vehicle_type" json:"vehicle_type"`
	UnderProgress      bool               `bson:"under_progress" json:"under_progress"`
	ErrorMessage       *string            `bson:"error_message,omitempty" json:"error_message,omitempty"`
	FCMToken           *string            `bson:"fcm_token,omitempty" json:"fcm_token,omitempty"`
	JWTToken           *string            `bson:"jwt_token,omitempty" json:"jwt_token,omitempty"`
	Location           *GeoJSONPoint      `bson:"location,omitempty" json:"location,omitempty"`
}

// VerificationUpdateRequest contains only document image fields sent by the driver app
type VerificationUpdateRequest struct {
	PortraitImage string `json:"portrait_image"`
	POIImage      string `json:"poi_image"`
	DLImage       string `json:"dl_image"`
	RCImage       string `json:"rc_image"`
	AmbFront      string `json:"amb_front"`
	AmbInside     string `json:"amb_inside"`
}

// AuthOTP represents a temporary OTP request
type AuthOTP struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Number    string             `bson:"number" json:"number"`
	OTP       string             `bson:"otp" json:"otp"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
}

// Referral represents a referral reward transaction
type Referral struct {
	ID              primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	UserType        string             `bson:"user_type" json:"user_type"` // "user" or "driver"
	RefFrom         string             `bson:"ref_from" json:"ref_from"`
	RefTo           string             `bson:"ref_to" json:"ref_to"`
	Value           string             `bson:"value" json:"value"`
	RidesDone       int                `bson:"rides_done" json:"rides_done"`
	AmountReceived  bool               `bson:"amount_recievied" json:"amount_received"`
	CreatedAt       time.Time          `bson:"created_at" json:"created_at"`
}
