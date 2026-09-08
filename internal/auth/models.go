package auth

import (
	"time"
)

type GeoJSONPoint struct {
	Type        string    `db:"type" json:"type"`
	Coordinates []float64 `db:"coordinates" json:"coordinates"`
}

type User struct {
	ID             string        `db:"id" json:"_id"`
	Name           string        `db:"name" json:"name"`
	Mobile         string        `db:"mobile" json:"mobile"`
	ReferralCode   string        `db:"referral_code" json:"referral_code"`
	MyReferralCode string        `db:"my_referral_code" json:"my_referral_code,omitempty"`
	Location       *GeoJSONPoint `db:"location" json:"location,omitempty"`
	FCMToken       *string       `db:"fcm_token" json:"fcm_token,omitempty"`
	JWTToken       *string       `db:"jwt_token" json:"jwt_token,omitempty"`
}

type DriverDetails struct {
	POIImage  string `db:"poi_image" json:"poi_image"`
	RCNumber  string `db:"rc_number" json:"rc_number"`
	RCImage   string `db:"rc_image" json:"rc_image"`
	DLNumber  string `db:"dl_number" json:"dl_number"`
	DLImage   string `db:"dl_image" json:"dl_image"`
	AmbFront  string `db:"amb_front" json:"amb_front,omitempty"`
	AmbInside string `db:"amb_inside" json:"amb_inside,omitempty"`
}

type WalletDetails struct {
	AccountNo string `db:"account_no" json:"account_no"`
	BenfName  string `db:"benf_name" json:"benf_name"`
	IFSCCode  string `db:"ifsc_code" json:"ifsc_code"`
	BenfID    string `db:"benf_id" json:"benf_id"`
}

type Driver struct {
	ID                 string         `db:"id" json:"_id"`
	Name               string         `db:"name" json:"name" validate:"required"`
	Mobile             string         `db:"mobile" json:"mobile" validate:"required"`
	Photo              string         `db:"photo" json:"photo"`
	VehicleType        string         `db:"vehicle_type" json:"vehicle_type" validate:"required"`
	VehicleReg         string         `db:"vehicle_registration" json:"vehicle_registration" validate:"required"`
	WalletDetails      *WalletDetails `db:"wallet_details" json:"wallet_details,omitempty"`
	WalletBalance      float64        `db:"wallet_balance" json:"wallet_balance"`
	ReferralCode       string         `db:"referral_code" json:"referral_code"`
	MyReferralCode     string         `db:"my_referral_code" json:"my_referral_code,omitempty"`
	Location           *GeoJSONPoint  `db:"location" json:"location,omitempty"`
	FCMToken           *string        `db:"fcm_token" json:"fcm_token,omitempty"`
	JWTToken           *string        `db:"jwt_token" json:"jwt_token,omitempty"`
	LastLocationUpdate *time.Time     `db:"last_location_update" json:"last_location_update,omitempty"`
	Details            *DriverDetails `db:"details" json:"details,omitempty"`
}

type UnverifiedDriver struct {
	ID            string        `db:"id" json:"_id"`
	Name          string        `db:"name" json:"name" validate:"required"`
	Mobile        string        `db:"mobile" json:"mobile" validate:"required"`
	PortraitImage string        `db:"portrait_image" json:"portrait_image"`
	POIImage      string        `db:"poi_image" json:"poi_image"`
	DLImage       string        `db:"dl_image" json:"dl_image"`
	RCImage       string        `db:"rc_image" json:"rc_image"`
	AmbFront      string        `db:"amb_front" json:"amb_front"`
	AmbInside     string        `db:"amb_inside" json:"amb_inside"`
	VehicleType   string        `db:"vehicle_type" json:"vehicle_type"`
	UnderProgress bool          `db:"under_progress" json:"under_progress"`
	ErrorMessage  *string       `db:"error_message" json:"error_message,omitempty"`
	FCMToken      *string       `db:"fcm_token" json:"fcm_token,omitempty"`
	JWTToken      *string       `db:"jwt_token" json:"jwt_token,omitempty"`
	Location      *GeoJSONPoint `db:"location" json:"location,omitempty"`
}

type VerificationUpdateRequest struct {
	PortraitImage string `json:"portrait_image" validate:"required"`
	POIImage      string `json:"poi_image" validate:"required"`
	DLImage       string `json:"dl_image" validate:"required"`
	RCImage       string `json:"rc_image" validate:"required"`
	AmbFront      string `json:"amb_front" validate:"required"`
	AmbInside     string `json:"amb_inside" validate:"required"`
}

type AuthOTP struct {
	ID        string    `db:"id" json:"_id"`
	Number    string    `db:"number" json:"number"`
	OTP       string    `db:"otp" json:"otp"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type Referral struct {
	ID             string    `db:"id" json:"_id"`
	UserType       string    `db:"user_type" json:"user_type"`
	RefFrom        string    `db:"ref_from" json:"ref_from"`
	RefTo          string    `db:"ref_to" json:"ref_to"`
	Value          string    `db:"value" json:"value"`
	RidesDone      int       `db:"rides_done" json:"rides_done"`
	AmountReceived bool      `db:"amount_recieved" json:"amount_received"`
	CreatedAt      time.Time `db:"created_at" json:"created_at"`
}

type RefreshToken struct {
	ID            string     `db:"id" json:"_id"`
	UserID        string     `db:"user_id" json:"user_id"`
	Role          string     `db:"role" json:"role"`
	TokenHash     string     `db:"token_hash" json:"-"`
	SessionID     string     `db:"session_id" json:"session_id,omitempty"`
	DeviceID      string     `db:"device_id" json:"device_id,omitempty"`
	DeviceName    string     `db:"device_name" json:"device_name,omitempty"`
	CreatedAt     time.Time  `db:"created_at" json:"created_at"`
	ExpiresAt     time.Time  `db:"expires_at" json:"expires_at"`
	Revoked       bool       `db:"revoked" json:"revoked"`
	RevokedAt     *time.Time `db:"revoked_at" json:"revoked_at,omitempty"`
	RevokedReason string     `db:"revoked_reason" json:"revoked_reason,omitempty"`
	SupersededBy  *string    `db:"superseded_by" json:"superseded_by,omitempty"`
}

type OTPAttempt struct {
	ID          string     `db:"id" json:"_id"`
	Mobile      string     `db:"mobile" json:"mobile"`
	Attempts    int        `db:"attempts" json:"attempts"`
	LockedUntil *time.Time `db:"locked_until" json:"locked_until,omitempty"`
	UpdatedAt   time.Time  `db:"updated_at" json:"updated_at"`
}

type HospitalMD struct {
	ID                string     `db:"id" json:"_id"`
	HospitalPendingID *string    `db:"hospital_pending_id" json:"hospital_pending_id,omitempty"`
	HospitalID        *string    `db:"hospital_id" json:"hospital_id,omitempty"`
	Name              string     `db:"name" json:"name"`
	Email             string     `db:"email" json:"email"`
	Mobile            string     `db:"mobile" json:"mobile"`
	OfficialNumber    string     `db:"official_number" json:"official_number"`
	Username          *string    `db:"username" json:"username,omitempty"`
	PasswordHash      *string    `db:"password_hash" json:"-"`
	Status            string     `db:"status" json:"status"`
	JWTToken          *string    `db:"jwt_token" json:"jwt_token,omitempty"`
	FCMToken          *string    `db:"fcm_token" json:"fcm_token,omitempty"`
	CreatedAt         time.Time  `db:"created_at" json:"created_at"`
}

type HospitalReceptionist struct {
	ID                 string    `db:"id" json:"_id"`
	HospitalID         string    `db:"hospital_id" json:"hospital_id"`
	CreatedByMDID      string    `db:"created_by_md_id" json:"created_by_md_id"`
	Name               string    `db:"name" json:"name"`
	Username           string    `db:"username" json:"username"`
	Email              *string   `db:"email" json:"email,omitempty"`
	PasswordHash       string    `db:"password_hash" json:"-"`
	Mobile             *string   `db:"mobile" json:"mobile,omitempty"`
	Active             bool      `db:"active" json:"active"`
	Status             string    `db:"status" json:"status"`
	MustChangePassword bool      `db:"must_change_password" json:"must_change_password"`
	InvitedAt          time.Time `db:"invited_at" json:"invited_at"`
	CreatedAt          time.Time `db:"created_at" json:"created_at"`
	JWTToken           *string   `db:"jwt_token" json:"jwt_token,omitempty"`
}

type AmbulanceAttendant struct {
	ID               string    `db:"id" json:"_id"`
	Name             string    `db:"name" json:"name"`
	Mobile           string    `db:"mobile" json:"mobile"`
	AssignedDriverID *string   `db:"assigned_driver_id" json:"assigned_driver_id,omitempty"`
	JWTToken         *string   `db:"jwt_token" json:"jwt_token,omitempty"`
	FCMToken         *string   `db:"fcm_token" json:"fcm_token,omitempty"`
	Active           bool      `db:"active" json:"active"`
	CreatedAt        time.Time `db:"created_at" json:"created_at"`
}
