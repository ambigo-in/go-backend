package payment

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type PaymentMode string

const (
	ModeCash   PaymentMode = "cash"
	ModeOnline PaymentMode = "online"
)

type Payment struct {
	ID                primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	UserID            string             `bson:"user_id" json:"user_id"`
	PartnerID         string             `bson:"partner_id" json:"partner_id"`
	RideID            string             `bson:"ride_id" json:"ride_id"`
	Description       string             `bson:"description" json:"description"`
	OriginalAmount    float64            `bson:"original_amount" json:"original_amount"`
	ChargedAmount     float64            `bson:"charged_amount" json:"charged_amount"`
	PaymentMode       PaymentMode        `bson:"payment_mode" json:"payment_mode"`
	Paid              bool               `bson:"paid" json:"paid"`
	RazorpayOrderID   *string            `bson:"razorpay_order_id,omitempty" json:"razorpay_order_id,omitempty"`
	RazorpayPaymentID *string            `bson:"razorpay_payment_id,omitempty" json:"razorpay_payment_id,omitempty"`
	PaidAt            *time.Time         `bson:"paid_at,omitempty" json:"paid_at,omitempty"`
	CreatedAt         time.Time          `bson:"created_at" json:"created_at"`
	Offer             *string            `bson:"offer,omitempty" json:"offer,omitempty"`
}
