package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type Store struct {
	authOTP           *mongo.Collection
	users             *mongo.Collection
	drivers           *mongo.Collection
	referrals         *mongo.Collection
	unverifiedDrivers *mongo.Collection
}

func NewStore(usersDB, recordsDB *mongo.Database) *Store {
	return &Store{
		authOTP:           usersDB.Collection("auth_otp"),
		users:             usersDB.Collection("users"),
		drivers:           usersDB.Collection("drivers"),
		referrals:         recordsDB.Collection("referrals"),
		unverifiedDrivers: usersDB.Collection("unverified_drivers"),
	}
}

// GenerateAndStoreOTP creates a 6-digit OTP and stores it in MongoDB
func (s *Store) GenerateAndStoreOTP(ctx context.Context, mobile string) (string, error) {
	// Generate random 6 digit OTP
	max := big.NewInt(1000000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	otpStr := fmt.Sprintf("%06d", n.Int64())

	// Upsert the OTP into DB
	filter := bson.M{"number": mobile}
	update := bson.M{
		"$set": bson.M{
			"otp":        otpStr,
			"created_at": time.Now(),
		},
	}
	// Upsert = true ensures we overwrite any existing OTP or create a new one
	opts := options.Update().SetUpsert(true)

	_, err = s.authOTP.UpdateOne(ctx, filter, update, opts)
	return otpStr, err
}

// VerifyOTP checks if the provided OTP matches the one in MongoDB
func (s *Store) VerifyOTP(ctx context.Context, mobile string, providedOTP string) (bool, error) {
	filter := bson.M{"number": mobile}
	var record AuthOTP

	err := s.authOTP.FindOne(ctx, filter).Decode(&record)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return false, nil
		}
		return false, err
	}

	return record.OTP == providedOTP, nil
}

// FindUserByMobile looks up a user by their mobile number
func (s *Store) FindUserByMobile(ctx context.Context, mobile string) (*User, error) {
	var user User
	err := s.users.FindOne(ctx, bson.M{"mobile": mobile}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil // User not found
		}
		return nil, err
	}
	return &user, nil
}

// FindDriverByMobile looks up a driver by their mobile number
func (s *Store) FindDriverByMobile(ctx context.Context, mobile string) (*Driver, error) {
	var driver Driver
	err := s.drivers.FindOne(ctx, bson.M{"mobile": mobile}).Decode(&driver)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &driver, nil
}

func (s *Store) FindUnverifiedDriverByMobile(ctx context.Context, mobile string) (*UnverifiedDriver, error) {
	var driver UnverifiedDriver
	err := s.unverifiedDrivers.FindOne(ctx, bson.M{"mobile": mobile}).Decode(&driver)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &driver, nil
}

// CreateUser inserts a new User into the database
func (s *Store) CreateUser(ctx context.Context, name, mobile, referralCode string) (*User, error) {
	user := &User{
		ID:           primitive.NewObjectID(),
		Name:         name,
		Mobile:       mobile,
		ReferralCode: referralCode, // In a full implementation, we'd auto-generate this
	}

	_, err := s.users.InsertOne(ctx, user)
	return user, err
}

func (s *Store) CreateUnverifiedDriver(ctx context.Context, name, mobile string) (*UnverifiedDriver, error) {
	driver := &UnverifiedDriver{
		ID:            primitive.NewObjectID(),
		Name:          name,
		Mobile:        mobile,
		UnderProgress: false,
	}

	_, err := s.unverifiedDrivers.InsertOne(ctx, driver)
	return driver, err
}

// UpdateUserJWT updates the JWT token for a specific user
func (s *Store) UpdateUserJWT(ctx context.Context, userID primitive.ObjectID, token string) error {
	filter := bson.M{"_id": userID}
	update := bson.M{"$set": bson.M{"jwt_token": token}}
	_, err := s.users.UpdateOne(ctx, filter, update)
	return err
}

// UpdateDriverJWT updates the JWT token for a specific driver
func (s *Store) UpdateDriverJWT(ctx context.Context, driverID primitive.ObjectID, token string) error {
	filter := bson.M{"_id": driverID}
	update := bson.M{"$set": bson.M{"jwt_token": token}}
	_, err := s.drivers.UpdateOne(ctx, filter, update)
	return err
}

func (s *Store) UpdateUnverifiedDriverJWT(ctx context.Context, driverID primitive.ObjectID, token string) error {
	filter := bson.M{"_id": driverID}
	update := bson.M{"$set": bson.M{"jwt_token": token}}
	_, err := s.unverifiedDrivers.UpdateOne(ctx, filter, update)
	return err
}

// -----------------------------------------------------
// PROFILE & VERIFICATION METHODS
// -----------------------------------------------------

// FindUserByID looks up a user by their MongoDB ObjectID
func (s *Store) FindUserByID(ctx context.Context, id primitive.ObjectID) (*User, error) {
	var user User
	err := s.users.FindOne(ctx, bson.M{"_id": id}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &user, nil
}

// FindDriverByID looks up a verified driver by their ObjectID
func (s *Store) FindDriverByID(ctx context.Context, id primitive.ObjectID) (*Driver, error) {
	var driver Driver
	err := s.drivers.FindOne(ctx, bson.M{"_id": id}).Decode(&driver)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &driver, nil
}

// GetDriverFCMToken retrieves the FCM token for a verified driver by string ID.
func (s *Store) GetDriverFCMToken(ctx context.Context, driverID string) (*string, error) {
	objID, err := primitive.ObjectIDFromHex(driverID)
	if err != nil {
		return nil, err
	}
	var driver Driver
	err = s.drivers.FindOne(ctx, bson.M{"_id": objID}).Decode(&driver)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return driver.FCMToken, nil
}

// FindUnverifiedDriverByID looks up an unverified driver by their ObjectID
func (s *Store) FindUnverifiedDriverByID(ctx context.Context, id primitive.ObjectID) (*UnverifiedDriver, error) {
	var driver UnverifiedDriver
	err := s.unverifiedDrivers.FindOne(ctx, bson.M{"_id": id}).Decode(&driver)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &driver, nil
}

// UpdateUserFCM updates the Firebase Cloud Messaging token for a user
func (s *Store) UpdateUserFCM(ctx context.Context, id primitive.ObjectID, token string) error {
	filter := bson.M{"_id": id}
	update := bson.M{"$set": bson.M{"fcm_token": token}}
	_, err := s.users.UpdateOne(ctx, filter, update)
	return err
}

// UpdateDriverFCM updates the FCM token for a driver
func (s *Store) UpdateDriverFCM(ctx context.Context, id primitive.ObjectID, token string) error {
	filter := bson.M{"_id": id}
	update := bson.M{"$set": bson.M{"fcm_token": token}}
	_, err := s.drivers.UpdateOne(ctx, filter, update)
	return err
}

func (s *Store) UpdateUnverifiedDriverFCM(ctx context.Context, id primitive.ObjectID, token string) error {
	filter := bson.M{"_id": id}
	update := bson.M{"$set": bson.M{"fcm_token": token}}
	_, err := s.unverifiedDrivers.UpdateOne(ctx, filter, update)
	return err
}

// UpdateUnverifiedDriver details handles the initial onboarding upload flow
func (s *Store) UpdateUnverifiedDriver(ctx context.Context, driver *UnverifiedDriver) error {
	filter := bson.M{"_id": driver.ID}
	setFields := bson.M{
		"under_progress": true,
		"error_message":  nil,
	}
	if driver.PortraitImage != "" {
		setFields["portrait_image"] = driver.PortraitImage
	}
	if driver.POIImage != "" {
		setFields["poi_image"] = driver.POIImage
	}
	if driver.DLImage != "" {
		setFields["dl_image"] = driver.DLImage
	}
	if driver.RCImage != "" {
		setFields["rc_image"] = driver.RCImage
	}
	if driver.AmbFront != "" {
		setFields["amb_front"] = driver.AmbFront
	}
	if driver.AmbInside != "" {
		setFields["amb_inside"] = driver.AmbInside
	}
	update := bson.M{"$set": setFields}
	opts := options.Update().SetUpsert(true)
	_, err := s.unverifiedDrivers.UpdateOne(ctx, filter, update, opts)
	return err
}

// ApproveDriver securely transitions an unverified driver to the active verified drivers pool
func (s *Store) ApproveDriver(ctx context.Context, driver *Driver) error {
	// 1. Insert into verified drivers
	_, err := s.drivers.InsertOne(ctx, driver)
	if err != nil {
		return err
	}

	// 2. Delete from unverified pool
	_, err = s.unverifiedDrivers.DeleteOne(ctx, bson.M{"_id": driver.ID})
	return err
}

// ListDrivers returns a paginated list of verified drivers sorted by newest first
func (s *Store) ListDrivers(ctx context.Context, skip int64) ([]Driver, int64, error) {
	total, err := s.drivers.CountDocuments(ctx, bson.M{})
	if err != nil {
		return nil, 0, err
	}

	opts := options.Find().SetSkip(skip).SetLimit(20).SetSort(bson.M{"_id": -1})
	cursor, err := s.drivers.Find(ctx, bson.M{}, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close(ctx)

	var drivers []Driver
	if err = cursor.All(ctx, &drivers); err != nil {
		return nil, 0, err
	}
	if drivers == nil {
		drivers = []Driver{}
	}
	return drivers, total, nil
}

// InsertDriver creates a new verified driver
func (s *Store) InsertDriver(ctx context.Context, driver *Driver) error {
	driver.ID = primitive.NewObjectID()
	_, err := s.drivers.InsertOne(ctx, driver)
	return err
}

// UpdateDriver replaces an existing verified driver document
func (s *Store) UpdateDriver(ctx context.Context, driver *Driver) error {
	_, err := s.drivers.ReplaceOne(ctx, bson.M{"_id": driver.ID}, driver)
	return err
}

// DeleteDriver removes a verified driver by ObjectID
func (s *Store) DeleteDriver(ctx context.Context, id primitive.ObjectID) error {
	_, err := s.drivers.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

// ListUnverifiedDrivers returns unverified drivers under progress (pending review)
func (s *Store) ListUnverifiedDrivers(ctx context.Context) ([]UnverifiedDriver, error) {
	cursor, err := s.unverifiedDrivers.Find(ctx, bson.M{"under_progress": true})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var drivers []UnverifiedDriver
	if err = cursor.All(ctx, &drivers); err != nil {
		return nil, err
	}
	if drivers == nil {
		drivers = []UnverifiedDriver{}
	}
	return drivers, nil
}

// ListAllUnverifiedDrivers returns all unverified drivers including pending, rejected, and in-progress
func (s *Store) ListAllUnverifiedDrivers(ctx context.Context) ([]UnverifiedDriver, error) {
	cursor, err := s.unverifiedDrivers.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var drivers []UnverifiedDriver
	if err = cursor.All(ctx, &drivers); err != nil {
		return nil, err
	}
	if drivers == nil {
		drivers = []UnverifiedDriver{}
	}
	return drivers, nil
}

// ListUsers returns all registered users sorted by newest first
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	cursor, err := s.users.Find(ctx, bson.M{}, options.Find().SetSort(bson.M{"_id": -1}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var users []User
	if err = cursor.All(ctx, &users); err != nil {
		return nil, err
	}
	if users == nil {
		users = []User{}
	}
	return users, nil
}

// RejectUnverifiedDriver sets the error_message and clears under_progress flag
func (s *Store) RejectUnverifiedDriver(ctx context.Context, id primitive.ObjectID, errorMessage string) error {
	_, err := s.unverifiedDrivers.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$set": bson.M{"under_progress": false, "error_message": errorMessage},
	})
	return err
}
