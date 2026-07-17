package user

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type User struct {
	ID             primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Mobile         string             `bson:"mobile" json:"mobile"`
	Name           string             `bson:"name" json:"name"`
	ReferralCode   string             `bson:"referral_code" json:"referral_code"`
	MyReferralCode string             `bson:"my_referral_code,omitempty" json:"my_referral_code,omitempty"`
	CreatedAt      time.Time          `bson:"created_at" json:"created_at"`
	IsDeleted      bool               `bson:"is_deleted" json:"-"`
}
