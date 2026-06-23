package config

import (
	"context"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Database holds references to MongoDB collections
type Database struct {
	Client   *mongo.Client
	Users    *mongo.Collection
	Drivers  *mongo.Collection
	Rides    *mongo.Collection
	Payments *mongo.Collection
}

// InitMongoDB connects to MongoDB and initializes the consolidated 'ambigo' database
func InitMongoDB(uri string) (*Database, error) {
	log.Println("Connecting to MongoDB...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	clientOptions := options.Client().ApplyURI(uri)
	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		return nil, err
	}

	err = client.Ping(ctx, nil)
	if err != nil {
		return nil, err
	}

	log.Println("Successfully connected to MongoDB!")

	db := client.Database("ambigo")

	return &Database{
		Client:   client,
		Users:    db.Collection("users"),
		Drivers:  db.Collection("drivers"),
		Rides:    db.Collection("rides"),
		Payments: db.Collection("payments"),
	}, nil
}

// EnsureIndexes creates all required indexes for the production collections.
// Must be called once on boot before any store operations.
func EnsureIndexes(client *mongo.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rides := client.Database("Rides").Collection("rides")
	payments := client.Database("Records").Collection("payments")
	authOTP := client.Database("Users").Collection("auth_otp")

	// Rides — queries: GetRideHistory (user_id+time, driver_id+time), GetCurrentRide (user_id+status, driver_id+status), CancelStaleSearchingRides (status+time)
	rideIndexes := []mongo.IndexModel{
		{
			Keys: bson.D{{"user_id", 1}, {"time.created_at", -1}},
		},
		{
			Keys: bson.D{{"driver_id", 1}, {"time.created_at", -1}},
		},
		{
			Keys: bson.D{{"status", 1}, {"time.created_at", -1}},
		},
	}

	// Payments — queries: FindPendingPaymentByUserID (user_id+paid), FindPendingPaymentByPartnerID (partner_id+paid)
	paymentIndexes := []mongo.IndexModel{
		{
			Keys: bson.D{{"user_id", 1}, {"paid", 1}},
		},
		{
			Keys: bson.D{{"partner_id", 1}, {"paid", 1}},
		},
	}

	// Auth OTP — TTL index auto-deletes documents 300s after created_at
	otpIndexes := []mongo.IndexModel{
		{
			Keys:    bson.D{{"created_at", 1}},
			Options: options.Index().SetExpireAfterSeconds(300),
		},
	}

	if _, err := rides.Indexes().CreateMany(ctx, rideIndexes); err != nil {
		return err
	}
	if _, err := payments.Indexes().CreateMany(ctx, paymentIndexes); err != nil {
		return err
	}
	if _, err := authOTP.Indexes().CreateMany(ctx, otpIndexes); err != nil {
		return err
	}

	log.Println("MongoDB indexes ensured on rides, payments, auth_otp")
	return nil
}
