package admin

import (
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Admin struct {
	ID             primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Username       string             `bson:"username" json:"username"`
	HashedPassword string             `bson:"hashed_password" json:"-"`
	Name           string             `bson:"name" json:"name"`
	Role           string             `bson:"role" json:"role"`
	Active         bool               `bson:"active" json:"active"`
	Mobile         string             `bson:"mobile,omitempty" json:"mobile,omitempty"`
}

type PricingTier struct {
	ThresholdDistance float64 `bson:"threshold_distance" json:"threshold_distance"`
	CostPerKm         float64 `bson:"cost_per_km" json:"cost_per_km"`
}

type AmbulanceType struct {
	ID               primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Name             string             `bson:"name" json:"name" validate:"required"`
	Photo            string             `bson:"photo" json:"photo"`
	HelperIncluded   bool               `bson:"helper_included" json:"helper_included"`
	OTPRequired      bool               `bson:"otp_required" json:"otp_required"`
	ListingThreshold float64            `bson:"listing_threshold" json:"listing_threshold" validate:"gte=0"`
	BaseFare         float64            `bson:"base_fare" json:"base_fare" validate:"gte=0"`
	DriverShare      float64            `bson:"driver_share" json:"driver_share" validate:"gte=0"`
	PricingTier      []PricingTier      `bson:"pricing_tier" json:"pricing_tier"`
}
