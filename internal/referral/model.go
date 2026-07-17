package referral

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Config represents a referral type configuration stored in the Data DB.
// Admins can configure reward amounts and ride thresholds per referral type.
type Config struct {
	ID             primitive.ObjectID `bson:"_id,omitempty" json:"_id,omitempty"`
	Type           string             `bson:"type" json:"type"` // "user_to_user", "user_to_driver", "driver_to_user", "driver_to_driver"
	ReferrerAmount float64            `bson:"referrer_amount" json:"referrer_amount"`
	NewUserAmount  float64            `bson:"new_user_amount" json:"new_user_amount"`
	RidesRequired  int                `bson:"rides_required" json:"rides_required"`
	Enabled        bool               `bson:"enabled" json:"enabled"`
}

// Record tracks an individual referral relationship between two users/drivers.
type Record struct {
	ID               primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Type             string             `bson:"type" json:"type"` // "user_to_user", etc.
	ReferrerID       string             `bson:"referrer_id" json:"referrer_id"`
	ReferrerRole     string             `bson:"referrer_role" json:"referrer_role"` // "user" or "driver"
	RefereeID        string             `bson:"referee_id" json:"referee_id"`
	RefereeRole      string             `bson:"referee_role" json:"referee_role"` // "user" or "driver"
	Code             string             `bson:"code" json:"code"`
	RidesRequired    int                `bson:"rides_required" json:"rides_required"`
	RidesDone        int                `bson:"rides_done" json:"rides_done"`
	ReferrerCredited bool               `bson:"referrer_credited" json:"referrer_credited"`
	RefereeCredited  bool               `bson:"referee_credited" json:"referee_credited"`
	ReferrerAmount   float64            `bson:"referrer_amount" json:"referrer_amount"`
	RefereeAmount    float64            `bson:"referee_amount" json:"referee_amount"`
	CreatedAt        time.Time          `bson:"created_at" json:"created_at"`
	CompletedAt      *time.Time         `bson:"completed_at,omitempty" json:"completed_at,omitempty"`
}

// ReferralSummary is a user-facing summary of a single referral.
type ReferralSummary struct {
	RefereeName   string  `json:"referee_name"`
	RefereeRole   string  `json:"referee_role"`
	RidesRequired int     `json:"rides_required"`
	RidesDone     int     `json:"rides_done"`
	AmountEarned  float64 `json:"amount_earned"`
	Pending       bool    `json:"pending"`
}

// RewardsResponse is the API response for GET /referral/rewards.
type RewardsResponse struct {
	MyReferralCode  string            `json:"my_referral_code"`
	AvailableCredit float64           `json:"available_credit"`
	TotalEarned     float64           `json:"total_earned"`
	Referrals       []ReferralSummary `json:"referrals"`
	Promos          []string          `json:"promos"` // e.g. "Refer a driver and earn ₹100!"
}

// ValidTypes lists the allowed referral type strings.
var ValidTypes = map[string]bool{
	"user_to_user":    true,
	"user_to_driver":  true,
	"driver_to_user":  true,
	"driver_to_driver": true,
}
