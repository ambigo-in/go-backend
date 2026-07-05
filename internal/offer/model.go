package offer

import (
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Offer struct {
	ID              primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Description     string             `bson:"description" json:"description" validate:"required"`
	UserID          *string            `bson:"user_id,omitempty" json:"user_id,omitempty"`
	OfferPercentage *float64           `bson:"offer_percentage,omitempty" json:"offer_percentage,omitempty"`
	OfferAmount     *float64           `bson:"offer_amount,omitempty" json:"offer_amount,omitempty"`
	MaxDiscount     *float64           `bson:"max_discount,omitempty" json:"max_discount,omitempty"`
}
