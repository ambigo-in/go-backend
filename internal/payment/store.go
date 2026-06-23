package payment

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type Store struct {
	collection *mongo.Collection
}

func NewStore(db *mongo.Database) *Store {
	return &Store{
		collection: db.Collection("payments"),
	}
}

// CreatePayment inserts a new payment record into the database
func (s *Store) CreatePayment(ctx context.Context, payment *Payment) error {
	payment.ID = primitive.NewObjectID()
	_, err := s.collection.InsertOne(ctx, payment)
	return err
}

// FindPendingPaymentByUserID looks for an unpaid payment belonging to a user
func (s *Store) FindPendingPaymentByUserID(ctx context.Context, userID string) (*Payment, error) {
	var payment Payment
	err := s.collection.FindOne(ctx, bson.M{"user_id": userID, "paid": false}).Decode(&payment)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &payment, nil
}

// FindPendingPaymentByPartnerID looks for an unpaid payment belonging to a driver
func (s *Store) FindPendingPaymentByPartnerID(ctx context.Context, partnerID string) (*Payment, error) {
	var payment Payment
	err := s.collection.FindOne(ctx, bson.M{"partner_id": partnerID, "paid": false}).Decode(&payment)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &payment, nil
}

// MarkPaymentPaid marks a payment as complete with its corresponding Razorpay transaction ID
func (s *Store) MarkPaymentPaid(ctx context.Context, id primitive.ObjectID, razorpayPaymentID string, paymentMode PaymentMode) error {
	filter := bson.M{"_id": id}
	update := bson.M{
		"$set": bson.M{
			"paid": true,
			"razorpay_payment_id": razorpayPaymentID,
			"payment_mode": paymentMode,
		},
		"$currentDate": bson.M{
			"paid_at": true,
		},
	}
	_, err := s.collection.UpdateOne(ctx, filter, update)
	return err
}

// FindPaymentByID retrieves a payment by its ObjectID
func (s *Store) FindPaymentByID(ctx context.Context, id primitive.ObjectID) (*Payment, error) {
	var payment Payment
	err := s.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&payment)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &payment, nil
}

// FindPaymentByRazorpayOrderID retrieves a payment by its Razorpay order ID.
func (s *Store) FindPaymentByRazorpayOrderID(ctx context.Context, orderID string) (*Payment, error) {
	var payment Payment
	err := s.collection.FindOne(ctx, bson.M{"razorpay_order_id": orderID}).Decode(&payment)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &payment, nil
}

// FindPaymentByRideID retrieves a payment using its string ride_id
func (s *Store) FindPaymentByRideID(ctx context.Context, rideID string) (*Payment, error) {
	var payment Payment
	err := s.collection.FindOne(ctx, bson.M{"ride_id": rideID}).Decode(&payment)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &payment, nil
}
