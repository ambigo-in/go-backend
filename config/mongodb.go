package config

import (
	"context"
	"time"

	"ambigo-backend/internal/logger"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// InitMongoDB connects to MongoDB and returns the client.
// V2 uses four databases (Users, Rides, Records, Data) mapped from the V1 layout.
func InitMongoDB(uri string) (*mongo.Client, error) {
	logger.Log.Info().Msg("Connecting to MongoDB...")

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

	logger.Log.Info().Msg("Successfully connected to MongoDB!")
	return client, nil
}

// EnsureIndexes creates all required indexes for the production collections.
// Must be called once on boot before any store operations.
func EnsureIndexes(client *mongo.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	users := client.Database("Users")
	rides := client.Database("Rides").Collection("rides")
	payments := client.Database("Records").Collection("payments")

	// Users — unique index on mobile for user lookup
	if _, err := users.Collection("users").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{"mobile", 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return err
	}

	// Drivers — unique index on mobile for driver lookup
	if _, err := users.Collection("drivers").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{"mobile", 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return err
	}

	// Unverified drivers — index on mobile
	if _, err := users.Collection("unverified_drivers").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{"mobile", 1}},
	}); err != nil {
		return err
	}

	// Admin — sparse unique index on username (admins can also log in via mobile)
	if _, err := users.Collection("admin").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{"username", 1}},
		Options: options.Index().SetUnique(true).SetSparse(true),
	}); err != nil {
		return err
	}

	// Refresh tokens — index on token_hash for fast lookup, user_id for listing sessions
	recordsDB := client.Database("Records")
	if _, err := recordsDB.Collection("refresh_tokens").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{"token_hash", 1}}},
		{Keys: bson.D{{"user_id", 1}}},
	}); err != nil {
		return err
	}

	// Auth OTP — index on number for OTP lookup, plus TTL on created_at
	authOTP := users.Collection("auth_otp")
	if _, err := authOTP.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{"number", 1}}},
		{Keys: bson.D{{"created_at", 1}}, Options: options.Index().SetExpireAfterSeconds(300)},
	}); err != nil {
		return err
	}

	// Rides — queries: GetRideHistory (user_id+time, driver_id+time), CancelStaleSearchingRides (status+time)
	if _, err := rides.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{"user_id", 1}, {"time.created_at", -1}}},
		{Keys: bson.D{{"driver_id", 1}, {"time.created_at", -1}}},
		{Keys: bson.D{{"status", 1}, {"time.created_at", -1}}},
	}); err != nil {
		return err
	}

	// Payments — queries by user/partner, ride_id, razorpay_order_id
	if _, err := payments.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{"user_id", 1}, {"paid", 1}}},
		{Keys: bson.D{{"partner_id", 1}, {"paid", 1}}},
		{Keys: bson.D{{"ride_id", 1}}},
		{Keys: bson.D{{"razorpay_order_id", 1}}},
	}); err != nil {
		return err
	}

	// Wallet — index on driver_id for listing transactions
	if _, err := client.Database("Records").Collection("wallet").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{"driver_id", 1}},
	}); err != nil {
		return err
	}

	// Feedback — index on driver_id for listing feedback
	if _, err := client.Database("Records").Collection("feedback").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{"driver_id", 1}},
	}); err != nil {
		return err
	}

	logger.Log.Info().Msg("MongoDB indexes ensured on all collections")
	return nil
}
