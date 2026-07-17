package driver

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type VehicleDetails struct {
	TypeID            string `bson:"type_id" json:"type_id"`
	RegistrationPlate string `bson:"registration_plate" json:"registration_plate"`
	Capability        string `bson:"capability" json:"capability"`
}

type Wallet struct {
	Balance float64 `bson:"balance" json:"balance"`
}

type Driver struct {
	ID             primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Mobile         string             `bson:"mobile" json:"mobile"`
	Name           string             `bson:"name" json:"name"`
	ReferralCode   string             `bson:"referral_code" json:"referral_code"`
	MyReferralCode string             `bson:"my_referral_code,omitempty" json:"my_referral_code,omitempty"`
	VehicleDetails VehicleDetails     `bson:"vehicle_details" json:"vehicle_details"`
	Wallet         Wallet             `bson:"wallet" json:"wallet"`
	CreatedAt      time.Time          `bson:"created_at" json:"created_at"`
	IsDeleted      bool               `bson:"is_deleted" json:"-"`
	IsVerified     bool               `bson:"is_verified" json:"is_verified"`
}
