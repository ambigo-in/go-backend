package payment

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type WalletTransaction struct {
	ID                  primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	DriverID            string             `bson:"driver_id" json:"driver_id"`
	ZwitchBeneficiaryID string             `bson:"zwitch_beneficiary_id" json:"zwitch_beneficiary_id"`
	ZwitchID            string             `bson:"zwitch_id" json:"zwitch_id"`
	Amount              float64            `bson:"amount" json:"amount"`
	AccountNo           string             `bson:"account_no" json:"account_no"`
	MerchantReferenceID string             `bson:"merchant_reference_id" json:"merchant_reference_id"`
	BankReferenceNo     string             `bson:"bank_reference_no" json:"bank_reference_no"`
	ZwitchTransferID    string             `bson:"zwitch_transfer_id" json:"zwitch_transfer_id"`
	Status              string             `bson:"status" json:"status"`
	ErrorMessage        string             `bson:"error_message" json:"error_message"`
	CreatedAt           time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt           *time.Time         `bson:"updated_at,omitempty" json:"updated_at,omitempty"`
}

type WalletStore struct {
	transactions *mongo.Collection
	drivers      *mongo.Collection
}

func NewWalletStore(recordsDB *mongo.Database, usersDB *mongo.Database) *WalletStore {
	return &WalletStore{
		transactions: recordsDB.Collection("wallet"),
		drivers:      usersDB.Collection("drivers"),
	}
}

func (s *WalletStore) InsertTransaction(ctx context.Context, tx *WalletTransaction) error {
	tx.ID = primitive.NewObjectID()
	tx.CreatedAt = time.Now()
	_, err := s.transactions.InsertOne(ctx, tx)
	return err
}

func (s *WalletStore) ListTransactions(ctx context.Context, driverID string) ([]WalletTransaction, error) {
	cursor, err := s.transactions.Find(ctx, bson.M{"driver_id": driverID})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var list []WalletTransaction
	if err = cursor.All(ctx, &list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []WalletTransaction{}
	}
	return list, nil
}

// UpdateWalletBalance increments or decrements the wallet balance.
// A negative amount indicates a withdrawal.
func (s *WalletStore) UpdateWalletBalance(ctx context.Context, driverID primitive.ObjectID, amount float64) error {
	filter := bson.M{"_id": driverID}
	update := bson.M{"$inc": bson.M{"wallet_balance": amount}}
	_, err := s.drivers.UpdateOne(ctx, filter, update)
	return err
}

// DeductBalance atomically deducts the amount only if sufficient balance exists.
// Prevents concurrent withdrawals from driving the balance negative.
func (s *WalletStore) DeductBalance(ctx context.Context, driverID primitive.ObjectID, amount float64) error {
	filter := bson.M{
		"_id":            driverID,
		"wallet_balance": bson.M{"$gte": amount},
	}
	update := bson.M{"$inc": bson.M{"wallet_balance": -amount}}
	result, err := s.drivers.UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	if result.ModifiedCount == 0 {
		return errors.New("insufficient wallet balance")
	}
	return nil
}

// UpdateWalletDetails saves the driver's Zwitch bank details.
func (s *WalletStore) UpdateWalletDetails(ctx context.Context, driverID primitive.ObjectID, details interface{}) error {
	filter := bson.M{"_id": driverID}
	update := bson.M{"$set": bson.M{"wallet_details": details}}
	_, err := s.drivers.UpdateOne(ctx, filter, update)
	return err
}
