package admin

import "time"

type Admin struct {
	ID             string    `db:"id" json:"_id"`
	Username       string    `db:"username" json:"username"`
	HashedPassword string    `db:"hashed_password" json:"-"`
	Name           string    `db:"name" json:"name"`
	Role           string    `db:"role" json:"role"`
	Active         bool      `db:"active" json:"active"`
	Mobile         string    `db:"mobile" json:"mobile,omitempty"`
	CreatedAt      time.Time `db:"created_at" json:"created_at,omitempty"`
	UpdatedAt      time.Time `db:"updated_at" json:"updated_at,omitempty"`
}

type AdminStaff struct {
	ID        string    `db:"id" json:"id"`
	IDAlias   string    `json:"_id"`
	Username  string    `db:"username" json:"username"`
	Email     string    `json:"email"`
	Name      string    `db:"name" json:"name"`
	Role      string    `db:"role" json:"role"`
	Active    bool      `db:"active" json:"active"`
	Status    string    `json:"status"`
	Mobile    string    `db:"mobile" json:"mobile,omitempty"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

type PricingTier struct {
	ThresholdDistance float64 `db:"threshold_distance" json:"threshold_distance"`
	CostPerKm         float64 `db:"cost_per_km" json:"cost_per_km"`
}

type AmbulanceType struct {
	ID               string        `db:"id" json:"_id"`
	Name             string        `db:"name" json:"name" validate:"required"`
	Photo            string        `db:"photo" json:"photo"`
	HelperIncluded   bool          `db:"helper_included" json:"helper_included"`
	OTPRequired      bool          `db:"otp_required" json:"otp_required"`
	ListingThreshold float64       `db:"listing_threshold" json:"listing_threshold" validate:"gte=0"`
	BaseFare         float64       `db:"base_fare" json:"base_fare" validate:"gte=0"`
	DriverShare      float64       `db:"driver_share" json:"driver_share" validate:"gte=0"`
	PricingTier      []PricingTier `db:"pricing_tier" json:"pricing_tier"`
}
