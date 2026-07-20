package auth

import (
	"time"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type GeoJSONPoint struct {
	Type        string    `bson:"type" json:"type"`
	Coordinates []float64 `bson:"coordinates" json:"coordinates"`
}

type User struct {
	ID             primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Name           string             `bson:"name" json:"name"`
	Mobile         string             `bson:"mobile" json:"mobile"`
	ReferralCode   string             `bson:"referral_code" json:"referral_code"`
	MyReferralCode string             `bson:"my_referral_code,omitempty" json:"my_referral_code,omitempty"`
	Location       *GeoJSONPoint      `bson:"location,omitempty" json:"location,omitempty"`
	FCMToken       *string            `bson:"fcm_token,omitempty" json:"fcm_token,omitempty"`
	JWTToken       *string            `bson:"jwt_token,omitempty" json:"jwt_token,omitempty"`
}

type DriverDetails struct {
	POIImage  string `bson:"poi_image" json:"poi_image"`
	RCNumber  string `bson:"rc_number" json:"rc_number"`
	RCImage   string `bson:"rc_image" json:"rc_image"`
	DLNumber  string `bson:"dl_number" json:"dl_number"`
	DLImage   string `bson:"dl_image" json:"dl_image"`
	AmbFront  string `bson:"amb_front,omitempty" json:"amb_front,omitempty"`
	AmbInside string `bson:"amb_inside,omitempty" json:"amb_inside,omitempty"`
}

type WalletDetails struct {
	AccountNo string `bson:"account_no" json:"account_no" validate:"required"`
	BenfName  string `bson:"benf_name" json:"benf_name" validate:"required"`
	IFSCCode  string `bson:"ifsc_code" json:"ifsc_code" validate:"required"`
	BenfID    string `bson:"benf_id" json:"benf_id"`
}

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
	MyReferralCode     string             `bson:"my_referral_code,omitempty" json:"my_referral_code,omitempty"`
	Location           *GeoJSONPoint      `bson:"location,omitempty" json:"location,omitempty"`
	FCMToken           *string            `bson:"fcm_token,omitempty" json:"fcm_token,omitempty"`
	JWTToken           *string            `bson:"jwt_token,omitempty" json:"jwt_token,omitempty"`
	LastLocationUpdate *time.Time         `bson:"last_location_update,omitempty" json:"last_location_update,omitempty"`
	Details            *DriverDetails     `bson:"details,omitempty" json:"details,omitempty"`
}

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

type VerificationUpdateRequest struct {
	PortraitImage string `json:"portrait_image" validate:"required"`
	POIImage      string `json:"poi_image"      validate:"required"`
	DLImage       string `json:"dl_image"       validate:"required"`
	RCImage       string `json:"rc_image"       validate:"required"`
	AmbFront      string `json:"amb_front"      validate:"required"`
	AmbInside     string `json:"amb_inside"     validate:"required"`
}

type AuthOTP struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Number    string             `bson:"number" json:"number"`
	OTP       string             `bson:"otp" json:"otp"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
}

type Referral struct {
	ID             primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	UserType       string             `bson:"user_type" json:"user_type"`
	RefFrom        string             `bson:"ref_from" json:"ref_from"`
	RefTo          string             `bson:"ref_to" json:"ref_to"`
	Value          string             `bson:"value" json:"value"`
	RidesDone      int                `bson:"rides_done" json:"rides_done"`
	AmountReceived bool               `bson:"amount_recievied" json:"amount_received"`
	CreatedAt      time.Time          `bson:"created_at" json:"created_at"`
}

type RefreshToken struct {
	ID         primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	UserID     string             `bson:"user_id" json:"user_id"`
	Role       string             `bson:"role" json:"role"`
	TokenHash  string             `bson:"token_hash" json:"-"`
	DeviceID   string             `bson:"device_id,omitempty" json:"device_id,omitempty"`
	DeviceName string             `bson:"device_name,omitempty" json:"device_name,omitempty"`
	CreatedAt  time.Time          `bson:"created_at" json:"created_at"`
	ExpiresAt  time.Time          `bson:"expires_at" json:"expires_at"`
	Revoked     bool               `bson:"revoked" json:"revoked"`
	SupersededBy primitive.ObjectID `bson:"superseded_by,omitempty" json:"superseded_by,omitempty"`
}

type OTPAttempt struct {
	ID         primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Mobile     string             `bson:"mobile" json:"mobile"`
	Attempts   int                `bson:"attempts" json:"attempts"`
	LockedUntil *time.Time         `bson:"locked_until,omitempty" json:"locked_until,omitempty"`
	UpdatedAt  time.Time          `bson:"updated_at" json:"updated_at"`
}
