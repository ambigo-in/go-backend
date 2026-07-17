package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var (
	ErrTokenAlreadyRevoked = fmt.Errorf("token already revoked")
	ErrNoLiveToken         = fmt.Errorf("no live token in chain")
	ErrBrokenChain         = fmt.Errorf("broken token chain")
	ErrCycleDetected       = fmt.Errorf("cycle detected in token chain")
)

const (
	otpExpiry          = 5 * time.Minute
	maxOTPAttempts     = 5
	otpLockoutDuration  = 1 * time.Hour
	refreshTokenExpiry  = 30 * 24 * time.Hour
)

type Store struct {
	authOTP           *mongo.Collection
	users             *mongo.Collection
	drivers           *mongo.Collection
	referrals         *mongo.Collection
	unverifiedDrivers *mongo.Collection
	refreshTokens     *mongo.Collection
	otpAttempts       *mongo.Collection
}

func NewStore(usersDB, recordsDB *mongo.Database) *Store {
	return &Store{
		authOTP:           usersDB.Collection("auth_otp"),
		users:             usersDB.Collection("users"),
		drivers:           usersDB.Collection("drivers"),
		referrals:         recordsDB.Collection("referrals"),
		unverifiedDrivers: usersDB.Collection("unverified_drivers"),
		refreshTokens:     recordsDB.Collection("refresh_tokens"),
		otpAttempts:       usersDB.Collection("otp_attempts"),
	}
}

// ---- OTP ----

func (s *Store) GenerateAndStoreOTP(ctx context.Context, mobile string) (string, error) {
	max := big.NewInt(1000000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	otpStr := fmt.Sprintf("%06d", n.Int64())

	filter := bson.M{"number": mobile}
	update := bson.M{
		"$set": bson.M{
			"otp":        otpStr,
			"created_at": time.Now(),
		},
	}
	opts := options.Update().SetUpsert(true)
	_, err = s.authOTP.UpdateOne(ctx, filter, update, opts)
	return otpStr, err
}

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

	// V2: Check OTP expiry in application code
	if time.Since(record.CreatedAt) > otpExpiry {
		return false, nil
	}

	return record.OTP == providedOTP, nil
}

// ---- OTP Account Lockout (V13) ----

func (s *Store) IncrementFailedOTP(ctx context.Context, mobile string) error {
	now := time.Now()
	filter := bson.M{"mobile": mobile}
	update := bson.M{
		"$inc":  bson.M{"attempts": 1},
		"$set":  bson.M{"updated_at": now},
		"$setOnInsert": bson.M{"mobile": mobile},
	}
	opts := options.Update().SetUpsert(true)

	res, err := s.otpAttempts.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		return err
	}

	// After update, fetch to check if we need to lock
	if res.ModifiedCount > 0 || res.UpsertedCount > 0 {
		var attempt OTPAttempt
		_ = s.otpAttempts.FindOne(ctx, filter).Decode(&attempt)
		if attempt.Attempts >= maxOTPAttempts {
			lockedUntil := now.Add(otpLockoutDuration)
			_, _ = s.otpAttempts.UpdateOne(ctx, filter, bson.M{
				"$set": bson.M{"locked_until": lockedUntil, "attempts": 0, "updated_at": now},
			})
		}
	}
	return nil
}

func (s *Store) ResetFailedOTP(ctx context.Context, mobile string) error {
	_, err := s.otpAttempts.DeleteOne(ctx, bson.M{"mobile": mobile})
	return err
}

func (s *Store) IsOTPLocked(ctx context.Context, mobile string) (bool, error) {
	var attempt OTPAttempt
	err := s.otpAttempts.FindOne(ctx, bson.M{"mobile": mobile}).Decode(&attempt)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return false, nil
		}
		return false, err
	}
	if attempt.LockedUntil != nil && time.Now().Before(*attempt.LockedUntil) {
		return true, nil
	}
	// Lock expired, reset
	if attempt.LockedUntil != nil && time.Now().After(*attempt.LockedUntil) {
		_, _ = s.otpAttempts.DeleteOne(ctx, bson.M{"mobile": mobile})
		return false, nil
	}
	return false, nil
}

// ---- Refresh Tokens (V5, V8, V18) ----

func (s *Store) CreateRefreshToken(ctx context.Context, userID, role, deviceID, deviceName string) (string, *RefreshToken, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", nil, err
	}
	tokenStr := hex.EncodeToString(tokenBytes)
	hash := sha256.Sum256(tokenBytes)
	tokenHash := hex.EncodeToString(hash[:])

	now := time.Now()
	rt := &RefreshToken{
		ID:         primitive.NewObjectID(),
		UserID:     userID,
		Role:       role,
		TokenHash:  tokenHash,
		DeviceID:   deviceID,
		DeviceName: deviceName,
		CreatedAt:  now,
		ExpiresAt:  now.Add(refreshTokenExpiry),
		Revoked:    false,
	}
	_, err := s.refreshTokens.InsertOne(ctx, rt)
	if err != nil {
		return "", nil, err
	}
	return tokenStr, rt, nil
}

func (s *Store) ValidateRefreshToken(ctx context.Context, tokenStr string) (*RefreshToken, error) {
	raw, err := hex.DecodeString(tokenStr)
	if err != nil {
		return nil, fmt.Errorf("invalid token format")
	}
	hash := sha256.Sum256(raw)
	tokenHash := hex.EncodeToString(hash[:])

	var rt RefreshToken
	err = s.refreshTokens.FindOne(ctx, bson.M{"token_hash": tokenHash}).Decode(&rt)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	if rt.Revoked {
		return nil, nil
	}
	if time.Now().After(rt.ExpiresAt) {
		return nil, nil
	}
	return &rt, nil
}

// LookupRefreshTokenByHash finds a refresh token by its hash, returning the document
// regardless of whether it is revoked or expired. Returns nil if not found.
func (s *Store) LookupRefreshTokenByHash(ctx context.Context, tokenStr string) (*RefreshToken, error) {
	raw, err := hex.DecodeString(tokenStr)
	if err != nil {
		return nil, fmt.Errorf("invalid token format")
	}
	hash := sha256.Sum256(raw)
	tokenHash := hex.EncodeToString(hash[:])

	var rt RefreshToken
	err = s.refreshTokens.FindOne(ctx, bson.M{"token_hash": tokenHash}).Decode(&rt)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &rt, nil
}

// RotateById atomically revokes an existing (non-revoked, non-expired) refresh token
// by its _id and creates a new token linked via superseded_by.
// Returns the new token and its raw string, or an error if the old token was already revoked.
func (s *Store) RotateById(ctx context.Context, oldToken *RefreshToken, deviceID, deviceName string) (*RefreshToken, string, error) {
	if oldToken.Revoked {
		return nil, "", ErrTokenAlreadyRevoked
	}
	if time.Now().After(oldToken.ExpiresAt) {
		return nil, "", ErrTokenAlreadyRevoked
	}

	// Create new token first
	newTokenStr, newToken, err := s.CreateRefreshToken(ctx, oldToken.UserID, oldToken.Role, deviceID, deviceName)
	if err != nil {
		return nil, "", err
	}

	// Atomically revoke old token and link to new one
	result, err := s.refreshTokens.UpdateOne(
		ctx,
		bson.M{"_id": oldToken.ID, "revoked": false},
		bson.M{"$set": bson.M{"revoked": true, "superseded_by": newToken.ID}},
	)
	if err != nil {
		// Clean up orphaned new token
		_, _ = s.refreshTokens.DeleteOne(ctx, bson.M{"_id": newToken.ID})
		return nil, "", err
	}
	if result.ModifiedCount == 0 {
		// Old token was already revoked by a concurrent request
		_, _ = s.refreshTokens.DeleteOne(ctx, bson.M{"_id": newToken.ID})
		return nil, "", ErrTokenAlreadyRevoked
	}

	return newToken, newTokenStr, nil
}

// FindLiveInChain follows the superseded_by chain starting from a revoked token
// to find the current live (non-revoked, non-expired) token at the end of the chain.
func (s *Store) FindLiveInChain(ctx context.Context, startingFrom *RefreshToken) (*RefreshToken, error) {
	if startingFrom == nil {
		return nil, fmt.Errorf("FindLiveInChain: startingFrom is nil")
	}
	current := startingFrom
	visited := make(map[primitive.ObjectID]bool)

	for current.SupersededBy != primitive.NilObjectID {
		if visited[current.SupersededBy] {
			return nil, ErrCycleDetected
		}
		visited[current.SupersededBy] = true

		var next RefreshToken
		err := s.refreshTokens.FindOne(ctx, bson.M{"_id": current.SupersededBy}).Decode(&next)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				return nil, ErrBrokenChain
			}
			return nil, err
		}

		if !next.Revoked && time.Now().Before(next.ExpiresAt) {
			return &next, nil
		}

		current = &next
	}

	return nil, ErrNoLiveToken
}

func (s *Store) RevokeRefreshToken(ctx context.Context, tokenStr string) error {
	raw, err := hex.DecodeString(tokenStr)
	if err != nil {
		return fmt.Errorf("invalid token format")
	}
	hash := sha256.Sum256(raw)
	tokenHash := hex.EncodeToString(hash[:])

	_, err = s.refreshTokens.UpdateOne(ctx, bson.M{"token_hash": tokenHash}, bson.M{
		"$set": bson.M{"revoked": true},
	})
	return err
}

func (s *Store) RevokeAllUserRefreshTokens(ctx context.Context, userID string) error {
	_, err := s.refreshTokens.UpdateMany(ctx, bson.M{"user_id": userID, "revoked": false}, bson.M{
		"$set": bson.M{"revoked": true},
	})
	return err
}

func (s *Store) ListUserSessions(ctx context.Context, userID string) ([]RefreshToken, error) {
	cursor, err := s.refreshTokens.Find(ctx, bson.M{"user_id": userID, "revoked": false})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var tokens []RefreshToken
	if err = cursor.All(ctx, &tokens); err != nil {
		return nil, err
	}
	if tokens == nil {
		tokens = []RefreshToken{}
	}
	return tokens, nil
}

func (s *Store) RevokeSessionByDeviceID(ctx context.Context, userID, deviceID string) error {
	_, err := s.refreshTokens.UpdateMany(ctx, bson.M{"user_id": userID, "device_id": deviceID, "revoked": false}, bson.M{
		"$set": bson.M{"revoked": true},
	})
	return err
}

// ---- Logout (V4) ----

func (s *Store) ClearUserJWT(ctx context.Context, userID primitive.ObjectID) error {
	_, err := s.users.UpdateOne(ctx, bson.M{"_id": userID}, bson.M{"$unset": bson.M{"jwt_token": ""}})
	return err
}

func (s *Store) ClearDriverJWT(ctx context.Context, driverID primitive.ObjectID) error {
	_, err := s.drivers.UpdateOne(ctx, bson.M{"_id": driverID}, bson.M{"$unset": bson.M{"jwt_token": ""}})
	return err
}

func (s *Store) ClearUnverifiedDriverJWT(ctx context.Context, driverID primitive.ObjectID) error {
	_, err := s.unverifiedDrivers.UpdateOne(ctx, bson.M{"_id": driverID}, bson.M{"$unset": bson.M{"jwt_token": ""}})
	return err
}

// ---- Referral Code Validation (V17) ----

func (s *Store) ValidateReferralCode(ctx context.Context, code string) (bool, error) {
	if code == "" {
		return false, nil
	}
	count, err := s.referrals.CountDocuments(ctx, bson.M{"value": code})
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ---- Mobile Validation (V11) ----

func IsValidIndianMobile(mobile string) bool {
	if len(mobile) != 10 {
		return false
	}
	for _, c := range mobile {
		if c < '0' || c > '9' {
			return false
		}
	}
	return mobile[0] >= '6' && mobile[0] <= '9'
}

// ---- Existing methods below (unchanged) ----

func (s *Store) FindUserByMobile(ctx context.Context, mobile string) (*User, error) {
	var user User
	err := s.users.FindOne(ctx, bson.M{"mobile": mobile}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &user, nil
}

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

func (s *Store) CreateUser(ctx context.Context, name, mobile, referralCode string) (*User, error) {
	user := &User{
		ID:           primitive.NewObjectID(),
		Name:         name,
		Mobile:       mobile,
		ReferralCode: referralCode,
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

func (s *Store) UpdateUserJWT(ctx context.Context, userID primitive.ObjectID, token string) error {
	filter := bson.M{"_id": userID}
	update := bson.M{"$set": bson.M{"jwt_token": token}}
	_, err := s.users.UpdateOne(ctx, filter, update)
	return err
}

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

func (s *Store) UpdateUserFCM(ctx context.Context, id primitive.ObjectID, token string) error {
	filter := bson.M{"_id": id}
	update := bson.M{"$set": bson.M{"fcm_token": token}}
	_, err := s.users.UpdateOne(ctx, filter, update)
	return err
}

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
	_, err := s.unverifiedDrivers.UpdateOne(ctx, filter, update)
	return err
}

func (s *Store) ApproveDriver(ctx context.Context, driver *Driver) error {
	_, err := s.drivers.InsertOne(ctx, driver)
	if err != nil {
		return err
	}
	_, err = s.unverifiedDrivers.DeleteOne(ctx, bson.M{"_id": driver.ID})
	return err
}

func (s *Store) ListDrivers(ctx context.Context, skip int64) ([]Driver, int64, error) {
	total, err := s.drivers.CountDocuments(ctx, bson.M{})
	if err != nil {
		return nil, 0, err
	}
	projection := bson.D{{Key: "details", Value: 0}}
	opts := options.Find().SetSkip(skip).SetLimit(20).SetSort(bson.M{"_id": -1}).SetProjection(projection)
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

func (s *Store) InsertDriver(ctx context.Context, driver *Driver) error {
	driver.ID = primitive.NewObjectID()
	_, err := s.drivers.InsertOne(ctx, driver)
	return err
}

func (s *Store) UpdateDriver(ctx context.Context, driver *Driver) error {
	_, err := s.drivers.ReplaceOne(ctx, bson.M{"_id": driver.ID}, driver)
	return err
}

func (s *Store) DeleteDriver(ctx context.Context, id primitive.ObjectID) error {
	_, err := s.drivers.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (s *Store) ListUnverifiedDrivers(ctx context.Context) ([]UnverifiedDriver, error) {
	projection := bson.D{
		{Key: "portrait_image", Value: 0},
		{Key: "poi_image", Value: 0},
		{Key: "dl_image", Value: 0},
		{Key: "rc_image", Value: 0},
		{Key: "amb_front", Value: 0},
		{Key: "amb_inside", Value: 0},
	}
	opts := options.Find().SetProjection(projection)
	cursor, err := s.unverifiedDrivers.Find(ctx, bson.M{"under_progress": true}, opts)
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

func (s *Store) ListAllUnverifiedDrivers(ctx context.Context) ([]UnverifiedDriver, error) {
	projection := bson.D{
		{Key: "portrait_image", Value: 0},
		{Key: "poi_image", Value: 0},
		{Key: "dl_image", Value: 0},
		{Key: "rc_image", Value: 0},
		{Key: "amb_front", Value: 0},
		{Key: "amb_inside", Value: 0},
	}
	opts := options.Find().SetProjection(projection)
	cursor, err := s.unverifiedDrivers.Find(ctx, bson.M{}, opts)
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

func (s *Store) RejectUnverifiedDriver(ctx context.Context, id primitive.ObjectID, errorMessage string) error {
	_, err := s.unverifiedDrivers.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$set": bson.M{"under_progress": false, "error_message": errorMessage},
	})
	return err
}

// ---- Referral Code Management (V20) ----

// SetUserReferralCode sets the user's own shareable referral code.
func (s *Store) SetUserReferralCode(ctx context.Context, userID primitive.ObjectID, code string) error {
	_, err := s.users.UpdateOne(ctx, bson.M{"_id": userID}, bson.M{
		"$set": bson.M{"my_referral_code": code},
	})
	return err
}

// SetDriverReferralCode sets the driver's own shareable referral code.
func (s *Store) SetDriverReferralCode(ctx context.Context, driverID primitive.ObjectID, code string) error {
	_, err := s.drivers.UpdateOne(ctx, bson.M{"_id": driverID}, bson.M{
		"$set": bson.M{"my_referral_code": code},
	})
	return err
}

// FindUserByReferralCode finds a user by their personal referral code (my_referral_code).
func (s *Store) FindUserByReferralCode(ctx context.Context, code string) (*User, error) {
	var user User
	err := s.users.FindOne(ctx, bson.M{"my_referral_code": code}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &user, nil
}

// FindDriverByReferralCode finds a driver by their personal referral code (my_referral_code).
func (s *Store) FindDriverByReferralCode(ctx context.Context, code string) (*Driver, error) {
	var driver Driver
	err := s.drivers.FindOne(ctx, bson.M{"my_referral_code": code}).Decode(&driver)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &driver, nil
}

// GetUserFCMToken retrieves a user's FCM token for push notifications.
func (s *Store) GetUserFCMToken(ctx context.Context, userID string) (*string, error) {
	objID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return nil, err
	}
	var user User
	err = s.users.FindOne(ctx, bson.M{"_id": objID}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return user.FCMToken, nil
}
