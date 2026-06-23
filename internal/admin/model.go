package admin

import (
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type PricingTier struct {
	ThresholdDistance float64 `bson:"threshold_distance" json:"threshold_distance"`
	CostPerKm         float64 `bson:"cost_per_km" json:"cost_per_km"`
}

type AmbulanceType struct {
	ID               primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Name             string             `bson:"name" json:"name"`
	Photo            string             `bson:"photo" json:"photo"`
	HelperIncluded   bool               `bson:"helper_included" json:"helper_included"`
	OTPRequired      bool               `bson:"otp_required" json:"otp_required"`
	ListingThreshold float64            `bson:"listing_threshold" json:"listing_threshold"`
	BaseFare         float64            `bson:"base_fare" json:"base_fare"`
	DriverShare      float64            `bson:"driver_share" json:"driver_share"`
	PricingTier      []PricingTier      `bson:"pricing_tier" json:"pricing_tier"`
}
