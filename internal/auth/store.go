package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"ambigo-backend/internal/ids"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrTokenAlreadyRevoked = fmt.Errorf("token already revoked")
	ErrNoLiveToken         = fmt.Errorf("no live token in chain")
	ErrBrokenChain         = fmt.Errorf("broken token chain")
	ErrCycleDetected       = fmt.Errorf("cycle detected in token chain")
)

const (
	otpExpiry           = 5 * time.Minute
	maxOTPAttempts      = 5
	otpLockoutDuration  = 1 * time.Hour
	refreshTokenExpiry  = 30 * 24 * time.Hour
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// ---- helpers ----

func marshalJSONB(v interface{}) []byte {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return nil
	}
	return b
}

func scanUserRow(row pgx.Row) (*User, error) {
	var u User
	var myReferralCode *string
	var locationData []byte
	var fcmToken, jwtToken *string
	var id string
	err := row.Scan(&id, &u.Name, &u.Mobile, &u.ReferralCode, &myReferralCode, &locationData, &fcmToken, &jwtToken)
	if err != nil {
		return nil, err
	}
	u.ID = id
	if myReferralCode != nil {
		u.MyReferralCode = *myReferralCode
	}
	if len(locationData) > 0 && string(locationData) != "null" {
		var loc GeoJSONPoint
		if err := json.Unmarshal(locationData, &loc); err == nil {
			u.Location = &loc
		}
	}
	u.FCMToken = fcmToken
	u.JWTToken = jwtToken
	return &u, nil
}

func scanDriverRow(row pgx.Row) (*Driver, error) {
	var d Driver
	var walletDetailsData []byte
	var locationData []byte
	var detailsData []byte
	var myReferralCode *string
	var fcmToken, jwtToken *string
	var lastLocationUpdate *time.Time
	var id string
	err := row.Scan(&id, &d.Name, &d.Mobile, &d.Photo, &d.VehicleType, &d.VehicleReg, &walletDetailsData, &d.WalletBalance, &d.ReferralCode, &myReferralCode, &locationData, &fcmToken, &jwtToken, &lastLocationUpdate, &detailsData)
	if err != nil {
		return nil, err
	}
	d.ID = id
	if myReferralCode != nil {
		d.MyReferralCode = *myReferralCode
	}
	if len(walletDetailsData) > 0 && string(walletDetailsData) != "null" {
		_ = json.Unmarshal(walletDetailsData, &d.WalletDetails)
	}
	if len(locationData) > 0 && string(locationData) != "null" {
		var loc GeoJSONPoint
		if err := json.Unmarshal(locationData, &loc); err == nil {
			d.Location = &loc
		}
	}
	if len(detailsData) > 0 && string(detailsData) != "null" {
		var det DriverDetails
		if err := json.Unmarshal(detailsData, &det); err == nil {
			d.Details = &det
		}
	}
	d.FCMToken = fcmToken
	d.JWTToken = jwtToken
	d.LastLocationUpdate = lastLocationUpdate
	return &d, nil
}

func scanUnverifiedDriverRow(row pgx.Row) (*UnverifiedDriver, error) {
	var d UnverifiedDriver
	var locationData []byte
	var errorMessage *string
	var fcmToken, jwtToken *string
	var id string
	err := row.Scan(&id, &d.Name, &d.Mobile, &d.PortraitImage, &d.POIImage, &d.DLImage, &d.RCImage, &d.AmbFront, &d.AmbInside, &d.VehicleType, &d.UnderProgress, &errorMessage, &fcmToken, &jwtToken, &locationData)
	if err != nil {
		return nil, err
	}
	d.ID = id
	d.ErrorMessage = errorMessage
	d.FCMToken = fcmToken
	d.JWTToken = jwtToken
	if len(locationData) > 0 && string(locationData) != "null" {
		var loc GeoJSONPoint
		if err := json.Unmarshal(locationData, &loc); err == nil {
			d.Location = &loc
		}
	}
	return &d, nil
}

func scanAuthOTPRow(row pgx.Row) (*AuthOTP, error) {
	var o AuthOTP
	var id string
	err := row.Scan(&id, &o.Number, &o.OTP, &o.CreatedAt)
	if err != nil {
		return nil, err
	}
	o.ID = id
	return &o, nil
}

func scanRefreshTokenRow(row pgx.Row) (*RefreshToken, error) {
	var rt RefreshToken
	var sessionID, deviceID, deviceName *string
	var revokedAt *time.Time
	var revokedReason *string
	var supersededBy *string
	var id string
	err := row.Scan(&id, &rt.UserID, &rt.Role, &rt.TokenHash, &sessionID, &deviceID, &deviceName, &rt.CreatedAt, &rt.ExpiresAt, &rt.Revoked, &revokedAt, &revokedReason, &supersededBy)
	if err != nil {
		return nil, err
	}
	rt.ID = id
	if sessionID != nil {
		rt.SessionID = *sessionID
	}
	if deviceID != nil {
		rt.DeviceID = *deviceID
	}
	if deviceName != nil {
		rt.DeviceName = *deviceName
	}
	rt.RevokedAt = revokedAt
	if revokedReason != nil {
		rt.RevokedReason = *revokedReason
	}
	if supersededBy != nil && *supersededBy != "" && !ids.IsZero(*supersededBy) {
		rt.SupersededBy = supersededBy
	}
	return &rt, nil
}

func scanOTPAttemptRow(row pgx.Row) (*OTPAttempt, error) {
	var a OTPAttempt
	err := row.Scan(&a.Mobile, &a.Attempts, &a.LockedUntil, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func scanHospitalMDRow(row pgx.Row) (*HospitalMD, error) {
	var md HospitalMD
	var hospitalPendingID, hospitalID *string
	var username, passwordHash *string
	var jwtToken, fcmToken *string
	var id string
	err := row.Scan(&id, &hospitalPendingID, &hospitalID, &md.Name, &md.Email, &md.Mobile, &md.OfficialNumber, &username, &passwordHash, &md.Status, &jwtToken, &fcmToken, &md.CreatedAt)
	if err != nil {
		return nil, err
	}
	md.ID = id
	if hospitalPendingID != nil && *hospitalPendingID != "" {
		md.HospitalPendingID = hospitalPendingID
	}
	if hospitalID != nil && *hospitalID != "" {
		md.HospitalID = hospitalID
	}
	md.Username = username
	md.PasswordHash = passwordHash
	md.JWTToken = jwtToken
	md.FCMToken = fcmToken
	return &md, nil
}

func scanHospitalReceptionistRow(row pgx.Row) (*HospitalReceptionist, error) {
	var r HospitalReceptionist
	var mobile *string
	var jwtToken *string
	var email *string
	var status sql.NullString
	var mustChange sql.NullBool
	var invitedAt sql.NullTime
	var id, hospitalID, createdByMDID string
	err := row.Scan(&id, &hospitalID, &createdByMDID, &r.Name, &r.Username, &r.PasswordHash, &mobile, &r.Active, &r.CreatedAt, &jwtToken, &email, &status, &mustChange, &invitedAt)
	if err != nil {
		return nil, err
	}
	r.ID = id
	r.HospitalID = hospitalID
	r.CreatedByMDID = createdByMDID
	r.Mobile = mobile
	r.JWTToken = jwtToken
	r.Email = email
	if status.Valid {
		r.Status = status.String
	} else if r.Active {
		r.Status = "active"
	} else {
		r.Status = "invited"
	}
	if mustChange.Valid {
		r.MustChangePassword = mustChange.Bool
	} else {
		r.MustChangePassword = r.Status == "invited"
	}
	if invitedAt.Valid {
		r.InvitedAt = invitedAt.Time
	} else {
		r.InvitedAt = r.CreatedAt
	}
	return &r, nil
}

func scanAmbulanceAttendantRow(row pgx.Row) (*AmbulanceAttendant, error) {
	var a AmbulanceAttendant
	var assignedDriverID *string
	var jwtToken, fcmToken *string
	var id string
	err := row.Scan(&id, &a.Name, &a.Mobile, &assignedDriverID, &jwtToken, &fcmToken, &a.Active, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	a.ID = id
	if assignedDriverID != nil && *assignedDriverID != "" {
		a.AssignedDriverID = assignedDriverID
	}
	a.JWTToken = jwtToken
	a.FCMToken = fcmToken
	return &a, nil
}

// ---- OTP ----

func (s *Store) GenerateAndStoreOTP(ctx context.Context, mobile string) (string, error) {
	max := big.NewInt(1000000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	otpStr := fmt.Sprintf("%06d", n.Int64())

	tag, err := s.pool.Exec(ctx, `UPDATE auth_otp SET otp=$2, created_at=now() WHERE number=$1`, mobile, otpStr)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		_, err = s.pool.Exec(ctx, `INSERT INTO auth_otp (id, number, otp, created_at) VALUES ($1::uuid, $2, $3, now())`, ids.New(), mobile, otpStr)
		if err != nil {
			return "", err
		}
	}
	return otpStr, nil
}

func (s *Store) VerifyOTP(ctx context.Context, mobile string, providedOTP string) (bool, error) {
	var otp string
	var createdAt time.Time
	err := s.pool.QueryRow(ctx, `SELECT otp, created_at FROM auth_otp WHERE number=$1 ORDER BY created_at DESC LIMIT 1`, mobile).Scan(&otp, &createdAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if time.Since(createdAt) > otpExpiry {
		return false, nil
	}
	return otp == providedOTP, nil
}

// ---- OTP Account Lockout (V13) ----

func (s *Store) IncrementFailedOTP(ctx context.Context, mobile string) error {
	var attempts int
	err := s.pool.QueryRow(ctx, `INSERT INTO otp_attempts (mobile, attempts, updated_at) VALUES ($1, 1, now()) ON CONFLICT (mobile) DO UPDATE SET attempts = otp_attempts.attempts + 1, updated_at = now() RETURNING attempts`, mobile).Scan(&attempts)
	if err != nil {
		return err
	}
	if attempts >= maxOTPAttempts {
		lockedUntil := time.Now().Add(otpLockoutDuration)
		_, _ = s.pool.Exec(ctx, `UPDATE otp_attempts SET locked_until=$2, attempts=0, updated_at=now() WHERE mobile=$1`, mobile, lockedUntil)
	}
	return nil
}

func (s *Store) ResetFailedOTP(ctx context.Context, mobile string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM otp_attempts WHERE mobile=$1`, mobile)
	return err
}

func (s *Store) IsOTPLocked(ctx context.Context, mobile string) (bool, error) {
	var attempt OTPAttempt
	err := s.pool.QueryRow(ctx, `SELECT mobile, attempts, locked_until, updated_at FROM otp_attempts WHERE mobile=$1`, mobile).Scan(&attempt.Mobile, &attempt.Attempts, &attempt.LockedUntil, &attempt.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if attempt.LockedUntil != nil && time.Now().Before(*attempt.LockedUntil) {
		return true, nil
	}
	if attempt.LockedUntil != nil && time.Now().After(*attempt.LockedUntil) {
		_, _ = s.pool.Exec(ctx, `DELETE FROM otp_attempts WHERE mobile=$1`, mobile)
		return false, nil
	}
	return false, nil
}

// ---- Refresh Tokens (V5, V8, V18) ----

func NewSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	return hex.EncodeToString(b)
}

func (s *Store) CreateRefreshToken(ctx context.Context, userID, role, sessionID, deviceID, deviceName string) (string, *RefreshToken, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", nil, err
	}
	tokenStr := hex.EncodeToString(tokenBytes)
	hash := sha256.Sum256(tokenBytes)
	tokenHash := hex.EncodeToString(hash[:])

	now := time.Now()
	rt := &RefreshToken{
		ID:        ids.New(),
		UserID:    userID,
		Role:      role,
		TokenHash: tokenHash,
		SessionID: sessionID,
		DeviceID:  deviceID,
		DeviceName: deviceName,
		CreatedAt: now,
		ExpiresAt: now.Add(refreshTokenExpiry),
		Revoked:   false,
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO refresh_tokens (id, user_id, role, token_hash, session_id, device_id, device_name, created_at, expires_at, revoked) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, false)`, rt.ID, rt.UserID, rt.Role, rt.TokenHash, rt.SessionID, rt.DeviceID, rt.DeviceName, rt.CreatedAt, rt.ExpiresAt)
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

	row := s.pool.QueryRow(ctx, `SELECT id::text, user_id, role, token_hash, session_id, device_id, device_name, created_at, expires_at, revoked, revoked_at, revoked_reason, superseded_by::text FROM refresh_tokens WHERE token_hash=$1`, tokenHash)
	rt, err := scanRefreshTokenRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
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
	return rt, nil
}

func (s *Store) LookupRefreshTokenByHash(ctx context.Context, tokenStr string) (*RefreshToken, error) {
	raw, err := hex.DecodeString(tokenStr)
	if err != nil {
		return nil, fmt.Errorf("invalid token format")
	}
	hash := sha256.Sum256(raw)
	tokenHash := hex.EncodeToString(hash[:])

	row := s.pool.QueryRow(ctx, `SELECT id::text, user_id, role, token_hash, session_id, device_id, device_name, created_at, expires_at, revoked, revoked_at, revoked_reason, superseded_by::text FROM refresh_tokens WHERE token_hash=$1`, tokenHash)
	rt, err := scanRefreshTokenRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return rt, nil
}

func (s *Store) RotateById(ctx context.Context, oldToken *RefreshToken, deviceID, deviceName string) (*RefreshToken, string, error) {
	if oldToken.Revoked {
		return nil, "", ErrTokenAlreadyRevoked
	}
	if time.Now().After(oldToken.ExpiresAt) {
		return nil, "", ErrTokenAlreadyRevoked
	}

	newTokenStr, newToken, err := s.CreateRefreshToken(ctx, oldToken.UserID, oldToken.Role, oldToken.SessionID, deviceID, deviceName)
	if err != nil {
		return nil, "", err
	}

	now := time.Now()
	tag, err := s.pool.Exec(ctx, `UPDATE refresh_tokens SET revoked=true, revoked_at=$2, revoked_reason='rotated', superseded_by=$3::uuid WHERE id=$1::uuid AND revoked=false`, oldToken.ID, now, newToken.ID)
	if err != nil {
		_, _ = s.pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE id=$1::uuid`, newToken.ID)
		return nil, "", err
	}
	if tag.RowsAffected() == 0 {
		_, _ = s.pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE id=$1::uuid`, newToken.ID)
		return nil, "", ErrTokenAlreadyRevoked
	}

	return newToken, newTokenStr, nil
}

func (s *Store) findRefreshTokenByID(ctx context.Context, id string) (*RefreshToken, error) {
	if !ids.IsValid(id) {
		return nil, fmt.Errorf("invalid id: %s", id)
	}
	row := s.pool.QueryRow(ctx, `SELECT id::text, user_id, role, token_hash, session_id, device_id, device_name, created_at, expires_at, revoked, revoked_at, revoked_reason, superseded_by::text FROM refresh_tokens WHERE id=$1::uuid`, id)
	return scanRefreshTokenRow(row)
}

func (s *Store) FindLiveInChain(ctx context.Context, startingFrom *RefreshToken) (*RefreshToken, error) {
	if startingFrom == nil {
		return nil, fmt.Errorf("FindLiveInChain: startingFrom is nil")
	}
	if !ids.IsValid(startingFrom.ID) {
		return nil, fmt.Errorf("invalid id: %s", startingFrom.ID)
	}
	// Bounded single-query chain walk: depth capped to 10, cycle-safe via visited array.
	const q = `
	WITH RECURSIVE chain AS (
		SELECT rt.*, 1 AS depth, ARRAY[rt.id] AS visited
		FROM refresh_tokens rt
		WHERE rt.id = $1::uuid
		UNION ALL
		SELECT rt.*, c.depth + 1, c.visited || rt.id
		FROM refresh_tokens rt
		JOIN chain c ON rt.id = c.superseded_by
		WHERE c.depth < 10
		  AND c.superseded_by IS NOT NULL
		  AND NOT rt.id = ANY(c.visited)
	)
	SELECT id::text, user_id, role, token_hash, session_id, device_id, device_name, created_at, expires_at, revoked, revoked_at, revoked_reason, superseded_by::text
	FROM chain
	WHERE NOT revoked
	  AND expires_at > now()
	  AND id <> $1::uuid
	ORDER BY depth ASC
	LIMIT 1`
	row := s.pool.QueryRow(ctx, q, startingFrom.ID)
	rt, err := scanRefreshTokenRow(row)
	if err == nil {
		return rt, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	// No live token found — distinguish broken chain / cycle vs clean end.
	const gapQ = `
	WITH RECURSIVE chain AS (
		SELECT rt.id, rt.superseded_by, 1 AS depth, ARRAY[rt.id] AS visited
		FROM refresh_tokens rt WHERE rt.id = $1::uuid
		UNION ALL
		SELECT rt.id, rt.superseded_by, c.depth+1, c.visited || rt.id
		FROM refresh_tokens rt JOIN chain c ON rt.id = c.superseded_by
		WHERE c.depth < 10 AND c.superseded_by IS NOT NULL AND NOT rt.id = ANY(c.visited)
	)
	SELECT
		EXISTS(SELECT 1 FROM chain c WHERE c.superseded_by IS NOT NULL AND NOT EXISTS (SELECT 1 FROM refresh_tokens rt WHERE rt.id = c.superseded_by)) AS has_gap,
		EXISTS(SELECT 1 FROM chain c JOIN refresh_tokens rt ON rt.id = c.superseded_by WHERE rt.id = ANY(c.visited)) AS has_cycle`
	var hasGap, hasCycle bool
	if err := s.pool.QueryRow(ctx, gapQ, startingFrom.ID).Scan(&hasGap, &hasCycle); err == nil {
		if hasCycle {
			return nil, ErrCycleDetected
		}
		if hasGap {
			return nil, ErrBrokenChain
		}
	}
	return nil, ErrNoLiveToken
}

func (s *Store) RevokeRefreshToken(ctx context.Context, tokenStr, reason string) error {
	raw, err := hex.DecodeString(tokenStr)
	if err != nil {
		return fmt.Errorf("invalid token format")
	}
	hash := sha256.Sum256(raw)
	tokenHash := hex.EncodeToString(hash[:])

	_, err = s.pool.Exec(ctx, `UPDATE refresh_tokens SET revoked=true, revoked_at=now(), revoked_reason=$2 WHERE token_hash=$1`, tokenHash, reason)
	return err
}

func (s *Store) RevokeAllUserRefreshTokens(ctx context.Context, userID, reason string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE refresh_tokens SET revoked=true, revoked_at=now(), revoked_reason=$2 WHERE user_id=$1 AND revoked=false`, userID, reason)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *Store) ListUserSessions(ctx context.Context, userID string) ([]RefreshToken, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, user_id, role, token_hash, session_id, device_id, device_name, created_at, expires_at, revoked, revoked_at, revoked_reason, superseded_by::text FROM refresh_tokens WHERE user_id=$1 AND revoked=false`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []RefreshToken
	for rows.Next() {
		var rt RefreshToken
		var sessionID, deviceID, deviceName *string
		var revokedAt *time.Time
		var revokedReason *string
		var supersededBy *string
		var id string
		if err := rows.Scan(&id, &rt.UserID, &rt.Role, &rt.TokenHash, &sessionID, &deviceID, &deviceName, &rt.CreatedAt, &rt.ExpiresAt, &rt.Revoked, &revokedAt, &revokedReason, &supersededBy); err != nil {
			return nil, err
		}
		rt.ID = id
		if sessionID != nil {
			rt.SessionID = *sessionID
		}
		if deviceID != nil {
			rt.DeviceID = *deviceID
		}
		if deviceName != nil {
			rt.DeviceName = *deviceName
		}
		rt.RevokedAt = revokedAt
		if revokedReason != nil {
			rt.RevokedReason = *revokedReason
		}
		if supersededBy != nil && *supersededBy != "" && !ids.IsZero(*supersededBy) {
			rt.SupersededBy = supersededBy
		}
		tokens = append(tokens, rt)
	}
	if tokens == nil {
		tokens = []RefreshToken{}
	}
	return tokens, rows.Err()
}

func (s *Store) CurrentSessions(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT session_id, role, user_id FROM refresh_tokens WHERE revoked=false AND user_id <> '' ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var sidPtr *string
		var r, uid string
		if err := rows.Scan(&sidPtr, &r, &uid); err != nil {
			continue
		}
		if sidPtr == nil || *sidPtr == "" {
			continue
		}
		sid := *sidPtr
		key := r + ":" + uid
		if _, seen := result[key]; !seen {
			result[key] = sid
		}
	}
	return result, rows.Err()
}

func (s *Store) RevokeSessionByDeviceID(ctx context.Context, userID, deviceID, reason string) error {
	_, err := s.pool.Exec(ctx, `UPDATE refresh_tokens SET revoked=true, revoked_at=now(), revoked_reason=$3 WHERE user_id=$1 AND device_id=$2 AND revoked=false`, userID, deviceID, reason)
	return err
}

func (s *Store) CleanupExpiredRefreshTokens(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE expires_at < now() - interval '7 days'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *Store) CleanupExpiredOTPs(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM auth_otp WHERE created_at < now() - interval '5 minutes'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ---- Logout (V4) ----

func (s *Store) ClearUserJWT(ctx context.Context, userID string) error {
	if !ids.IsValid(userID) {
		return fmt.Errorf("invalid id: %s", userID)
	}
	_, err := s.pool.Exec(ctx, `UPDATE users SET jwt_token=NULL WHERE id=$1::uuid`, userID)
	return err
}

func (s *Store) ClearDriverJWT(ctx context.Context, driverID string) error {
	if !ids.IsValid(driverID) {
		return fmt.Errorf("invalid id: %s", driverID)
	}
	_, err := s.pool.Exec(ctx, `UPDATE drivers SET jwt_token=NULL WHERE id=$1::uuid`, driverID)
	return err
}

func (s *Store) ClearUnverifiedDriverJWT(ctx context.Context, driverID string) error {
	if !ids.IsValid(driverID) {
		return fmt.Errorf("invalid id: %s", driverID)
	}
	_, err := s.pool.Exec(ctx, `UPDATE unverified_drivers SET jwt_token=NULL WHERE id=$1::uuid`, driverID)
	return err
}

// ---- Referral Code Validation (V17) ----

func (s *Store) ValidateReferralCode(ctx context.Context, code string) (bool, error) {
	if code == "" {
		return false, nil
	}
	var count int64
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM referrals_legacy WHERE value=$1`, code).Scan(&count)
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

// ---- Existing methods below ----

func (s *Store) FindUserByMobile(ctx context.Context, mobile string) (*User, error) {
	row := s.pool.QueryRow(ctx, `SELECT id::text, name, mobile, referral_code, my_referral_code, location, fcm_token, jwt_token FROM users WHERE mobile=$1`, mobile)
	u, err := scanUserRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return u, nil
}

func (s *Store) FindDriverByMobile(ctx context.Context, mobile string) (*Driver, error) {
	row := s.pool.QueryRow(ctx, `SELECT id::text, name, mobile, photo, vehicle_type, vehicle_registration, wallet_details, wallet_balance, referral_code, my_referral_code, location, fcm_token, jwt_token, last_location_update, details FROM drivers WHERE mobile=$1`, mobile)
	d, err := scanDriverRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return d, nil
}

func (s *Store) FindUnverifiedDriverByMobile(ctx context.Context, mobile string) (*UnverifiedDriver, error) {
	row := s.pool.QueryRow(ctx, `SELECT id::text, name, mobile, portrait_image, poi_image, dl_image, rc_image, amb_front, amb_inside, vehicle_type, under_progress, error_message, fcm_token, jwt_token, location FROM unverified_drivers WHERE mobile=$1`, mobile)
	d, err := scanUnverifiedDriverRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return d, nil
}

func (s *Store) CreateUser(ctx context.Context, name, mobile, referralCode string) (*User, error) {
	user := &User{
		ID:           ids.New(),
		Name:         name,
		Mobile:       mobile,
		ReferralCode: referralCode,
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO users (id, name, mobile, referral_code) VALUES ($1::uuid, $2, $3, $4)`, user.ID, user.Name, user.Mobile, user.ReferralCode)
	return user, err
}

func (s *Store) CreateUnverifiedDriver(ctx context.Context, name, mobile string) (*UnverifiedDriver, error) {
	driver := &UnverifiedDriver{
		ID:            ids.New(),
		Name:          name,
		Mobile:        mobile,
		UnderProgress: false,
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO unverified_drivers (id, name, mobile, under_progress) VALUES ($1::uuid, $2, $3, $4)`, driver.ID, driver.Name, driver.Mobile, driver.UnderProgress)
	return driver, err
}

func (s *Store) UpdateUserJWT(ctx context.Context, userID string, token string) error {
	if !ids.IsValid(userID) {
		return fmt.Errorf("invalid id: %s", userID)
	}
	_, err := s.pool.Exec(ctx, `UPDATE users SET jwt_token=$2 WHERE id=$1::uuid`, userID, token)
	return err
}

func (s *Store) UpdateDriverJWT(ctx context.Context, driverID string, token string) error {
	if !ids.IsValid(driverID) {
		return fmt.Errorf("invalid id: %s", driverID)
	}
	_, err := s.pool.Exec(ctx, `UPDATE drivers SET jwt_token=$2 WHERE id=$1::uuid`, driverID, token)
	return err
}

func (s *Store) UpdateUnverifiedDriverJWT(ctx context.Context, driverID string, token string) error {
	if !ids.IsValid(driverID) {
		return fmt.Errorf("invalid id: %s", driverID)
	}
	_, err := s.pool.Exec(ctx, `UPDATE unverified_drivers SET jwt_token=$2 WHERE id=$1::uuid`, driverID, token)
	return err
}

func (s *Store) FindUserByID(ctx context.Context, id string) (*User, error) {
	if !ids.IsValid(id) {
		return nil, fmt.Errorf("invalid id: %s", id)
	}
	row := s.pool.QueryRow(ctx, `SELECT id::text, name, mobile, referral_code, my_referral_code, location, fcm_token, jwt_token FROM users WHERE id=$1::uuid`, id)
	u, err := scanUserRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return u, nil
}

func (s *Store) FindDriverByID(ctx context.Context, id string) (*Driver, error) {
	if !ids.IsValid(id) {
		return nil, fmt.Errorf("invalid id: %s", id)
	}
	row := s.pool.QueryRow(ctx, `SELECT id::text, name, mobile, photo, vehicle_type, vehicle_registration, wallet_details, wallet_balance, referral_code, my_referral_code, location, fcm_token, jwt_token, last_location_update, details FROM drivers WHERE id=$1::uuid`, id)
	d, err := scanDriverRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return d, nil
}

func (s *Store) GetDriverFCMToken(ctx context.Context, driverID string) (*string, error) {
	if !ids.IsValid(driverID) {
		return nil, fmt.Errorf("invalid id: %s", driverID)
	}
	var fcmToken *string
	err := s.pool.QueryRow(ctx, `SELECT fcm_token FROM drivers WHERE id=$1::uuid`, driverID).Scan(&fcmToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return fcmToken, nil
}

func (s *Store) FindUnverifiedDriverByID(ctx context.Context, id string) (*UnverifiedDriver, error) {
	if !ids.IsValid(id) {
		return nil, fmt.Errorf("invalid id: %s", id)
	}
	row := s.pool.QueryRow(ctx, `SELECT id::text, name, mobile, portrait_image, poi_image, dl_image, rc_image, amb_front, amb_inside, vehicle_type, under_progress, error_message, fcm_token, jwt_token, location FROM unverified_drivers WHERE id=$1::uuid`, id)
	d, err := scanUnverifiedDriverRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return d, nil
}

func (s *Store) UpdateUserFCM(ctx context.Context, id string, token string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `UPDATE users SET fcm_token=$2 WHERE id=$1::uuid`, id, token)
	return err
}

func (s *Store) UpdateDriverFCM(ctx context.Context, id string, token string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `UPDATE drivers SET fcm_token=$2 WHERE id=$1::uuid`, id, token)
	return err
}

func (s *Store) UpdateUnverifiedDriverFCM(ctx context.Context, id string, token string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `UPDATE unverified_drivers SET fcm_token=$2 WHERE id=$1::uuid`, id, token)
	return err
}

func (s *Store) UpdateUnverifiedDriver(ctx context.Context, driver *UnverifiedDriver) error {
	if !ids.IsValid(driver.ID) {
		return fmt.Errorf("invalid id: %s", driver.ID)
	}
	// Build dynamic update preserving original behavior: set under_progress true, error_message NULL, and only overwrite image fields if non-empty
	locationData := marshalJSONB(driver.Location)
	_, err := s.pool.Exec(ctx, `
		UPDATE unverified_drivers SET
			under_progress = true,
			error_message = NULL,
			portrait_image = CASE WHEN $2 <> '' THEN $2 ELSE portrait_image END,
			poi_image = CASE WHEN $3 <> '' THEN $3 ELSE poi_image END,
			dl_image = CASE WHEN $4 <> '' THEN $4 ELSE dl_image END,
			rc_image = CASE WHEN $5 <> '' THEN $5 ELSE rc_image END,
			amb_front = CASE WHEN $6 <> '' THEN $6 ELSE amb_front END,
			amb_inside = CASE WHEN $7 <> '' THEN $7 ELSE amb_inside END,
			location = COALESCE($8::jsonb, location)
		WHERE id=$1::uuid`, driver.ID, driver.PortraitImage, driver.POIImage, driver.DLImage, driver.RCImage, driver.AmbFront, driver.AmbInside, locationData)
	return err
}

func (s *Store) ApproveDriver(ctx context.Context, driver *Driver) error {
	walletDetailsData := marshalJSONB(driver.WalletDetails)
	if walletDetailsData == nil {
		walletDetailsData = []byte("{}")
	}
	locationData := marshalJSONB(driver.Location)
	detailsData := marshalJSONB(driver.Details)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `INSERT INTO drivers (id, name, mobile, photo, vehicle_type, vehicle_registration, wallet_details, wallet_balance, referral_code, my_referral_code, location, fcm_token, jwt_token, last_location_update, details) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11::jsonb, $12, $13, $14, $15::jsonb)`,
		driver.ID, driver.Name, driver.Mobile, driver.Photo, driver.VehicleType, driver.VehicleReg, walletDetailsData, driver.WalletBalance, driver.ReferralCode, driver.MyReferralCode, locationData, driver.FCMToken, driver.JWTToken, driver.LastLocationUpdate, detailsData)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM unverified_drivers WHERE id=$1::uuid`, driver.ID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ListDrivers(ctx context.Context, skip int64) ([]Driver, int64, error) {
	var total int64
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM drivers`).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, name, mobile, photo, vehicle_type, vehicle_registration, wallet_details, wallet_balance, referral_code, my_referral_code, location, fcm_token, jwt_token, last_location_update, details FROM drivers ORDER BY created_at DESC, id DESC OFFSET $1 LIMIT 20`, skip)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var drivers []Driver
	for rows.Next() {
		var d Driver
		var walletDetailsData []byte
		var locationData []byte
		var detailsData []byte
		var myReferralCode *string
		var fcmToken, jwtToken *string
		var lastLocationUpdate *time.Time
		var id string
		if err := rows.Scan(&id, &d.Name, &d.Mobile, &d.Photo, &d.VehicleType, &d.VehicleReg, &walletDetailsData, &d.WalletBalance, &d.ReferralCode, &myReferralCode, &locationData, &fcmToken, &jwtToken, &lastLocationUpdate, &detailsData); err != nil {
			return nil, 0, err
		}
		d.ID = id
		if myReferralCode != nil {
			d.MyReferralCode = *myReferralCode
		}
		if len(walletDetailsData) > 0 && string(walletDetailsData) != "null" {
			_ = json.Unmarshal(walletDetailsData, &d.WalletDetails)
		}
		if len(locationData) > 0 && string(locationData) != "null" {
			var loc GeoJSONPoint
			if err := json.Unmarshal(locationData, &loc); err == nil {
				d.Location = &loc
			}
		}
		if len(detailsData) > 0 && string(detailsData) != "null" {
			var det DriverDetails
			if err := json.Unmarshal(detailsData, &det); err == nil {
				d.Details = &det
			}
		}
		d.FCMToken = fcmToken
		d.JWTToken = jwtToken
		d.LastLocationUpdate = lastLocationUpdate
		drivers = append(drivers, d)
	}
	if drivers == nil {
		drivers = []Driver{}
	}
	return drivers, total, rows.Err()
}

func (s *Store) InsertDriver(ctx context.Context, driver *Driver) error {
	driver.ID = ids.New()
	walletDetailsData := marshalJSONB(driver.WalletDetails)
	if walletDetailsData == nil {
		walletDetailsData = []byte("{}")
	}
	locationData := marshalJSONB(driver.Location)
	detailsData := marshalJSONB(driver.Details)
	_, err := s.pool.Exec(ctx, `INSERT INTO drivers (id, name, mobile, photo, vehicle_type, vehicle_registration, wallet_details, wallet_balance, referral_code, my_referral_code, location, fcm_token, jwt_token, last_location_update, details) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11::jsonb, $12, $13, $14, $15::jsonb)`,
		driver.ID, driver.Name, driver.Mobile, driver.Photo, driver.VehicleType, driver.VehicleReg, walletDetailsData, driver.WalletBalance, driver.ReferralCode, driver.MyReferralCode, locationData, driver.FCMToken, driver.JWTToken, driver.LastLocationUpdate, detailsData)
	return err
}

func (s *Store) UpdateDriver(ctx context.Context, driver *Driver) error {
	if !ids.IsValid(driver.ID) {
		return fmt.Errorf("invalid id: %s", driver.ID)
	}
	walletDetailsData := marshalJSONB(driver.WalletDetails)
	if walletDetailsData == nil {
		walletDetailsData = []byte("{}")
	}
	locationData := marshalJSONB(driver.Location)
	detailsData := marshalJSONB(driver.Details)
	_, err := s.pool.Exec(ctx, `UPDATE drivers SET name=$2, mobile=$3, photo=$4, vehicle_type=$5, vehicle_registration=$6, wallet_details=$7::jsonb, wallet_balance=$8, referral_code=$9, my_referral_code=$10, location=$11::jsonb, fcm_token=$12, jwt_token=$13, last_location_update=$14, details=$15::jsonb WHERE id=$1::uuid`,
		driver.ID, driver.Name, driver.Mobile, driver.Photo, driver.VehicleType, driver.VehicleReg, walletDetailsData, driver.WalletBalance, driver.ReferralCode, driver.MyReferralCode, locationData, driver.FCMToken, driver.JWTToken, driver.LastLocationUpdate, detailsData)
	return err
}

func (s *Store) DeleteDriver(ctx context.Context, id string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM drivers WHERE id=$1::uuid`, id)
	return err
}

func (s *Store) ListUnverifiedDrivers(ctx context.Context) ([]UnverifiedDriver, error) {
	return s.ListUnverifiedDriversPaginated(ctx, 50, "")
}

func (s *Store) ListUnverifiedDriversPaginated(ctx context.Context, limit int, cursor string) ([]UnverifiedDriver, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	if cursor != "" && ids.IsValid(cursor) {
		rows, err = s.pool.Query(ctx, `SELECT id::text, name, mobile, portrait_image, poi_image, dl_image, rc_image, amb_front, amb_inside, vehicle_type, under_progress, error_message, fcm_token, jwt_token, location FROM unverified_drivers WHERE under_progress=true AND (created_at, id) < ((SELECT created_at FROM unverified_drivers WHERE id=$2::uuid), $2::uuid) ORDER BY created_at DESC, id DESC LIMIT $1`, limit, cursor)
	} else {
		rows, err = s.pool.Query(ctx, `SELECT id::text, name, mobile, portrait_image, poi_image, dl_image, rc_image, amb_front, amb_inside, vehicle_type, under_progress, error_message, fcm_token, jwt_token, location FROM unverified_drivers WHERE under_progress=true ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var drivers []UnverifiedDriver
	for rows.Next() {
		var d UnverifiedDriver
		var locationData []byte
		var errorMessage *string
		var fcmToken, jwtToken *string
		var id string
		if err := rows.Scan(&id, &d.Name, &d.Mobile, &d.PortraitImage, &d.POIImage, &d.DLImage, &d.RCImage, &d.AmbFront, &d.AmbInside, &d.VehicleType, &d.UnderProgress, &errorMessage, &fcmToken, &jwtToken, &locationData); err != nil {
			return nil, err
		}
		d.ID = id
		d.ErrorMessage = errorMessage
		d.FCMToken = fcmToken
		d.JWTToken = jwtToken
		if len(locationData) > 0 && string(locationData) != "null" {
			var loc GeoJSONPoint
			if err := json.Unmarshal(locationData, &loc); err == nil {
				d.Location = &loc
			}
		}
		drivers = append(drivers, d)
	}
	if drivers == nil {
		drivers = []UnverifiedDriver{}
	}
	return drivers, rows.Err()
}

func (s *Store) ListUnverifiedDriversWithOffset(ctx context.Context, limit int, offset int) ([]UnverifiedDriver, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, name, mobile, portrait_image, poi_image, dl_image, rc_image, amb_front, amb_inside, vehicle_type, under_progress, error_message, fcm_token, jwt_token, location FROM unverified_drivers WHERE under_progress=true ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var drivers []UnverifiedDriver
	for rows.Next() {
		var d UnverifiedDriver
		var locationData []byte
		var errorMessage *string
		var fcmToken, jwtToken *string
		var id string
		if err := rows.Scan(&id, &d.Name, &d.Mobile, &d.PortraitImage, &d.POIImage, &d.DLImage, &d.RCImage, &d.AmbFront, &d.AmbInside, &d.VehicleType, &d.UnderProgress, &errorMessage, &fcmToken, &jwtToken, &locationData); err != nil {
			return nil, err
		}
		d.ID = id
		d.ErrorMessage = errorMessage
		d.FCMToken = fcmToken
		d.JWTToken = jwtToken
		if len(locationData) > 0 && string(locationData) != "null" {
			var loc GeoJSONPoint
			if err := json.Unmarshal(locationData, &loc); err == nil {
				d.Location = &loc
			}
		}
		drivers = append(drivers, d)
	}
	if drivers == nil {
		drivers = []UnverifiedDriver{}
	}
	return drivers, rows.Err()
}

func (s *Store) ListAllUnverifiedDrivers(ctx context.Context) ([]UnverifiedDriver, error) {
	return s.ListAllUnverifiedDriversPaginated(ctx, 50, "")
}

func (s *Store) ListAllUnverifiedDriversPaginated(ctx context.Context, limit int, cursor string) ([]UnverifiedDriver, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	if cursor != "" && ids.IsValid(cursor) {
		rows, err = s.pool.Query(ctx, `SELECT id::text, name, mobile, portrait_image, poi_image, dl_image, rc_image, amb_front, amb_inside, vehicle_type, under_progress, error_message, fcm_token, jwt_token, location FROM unverified_drivers WHERE (created_at, id) < ((SELECT created_at FROM unverified_drivers WHERE id=$2::uuid), $2::uuid) ORDER BY created_at DESC, id DESC LIMIT $1`, limit, cursor)
	} else {
		rows, err = s.pool.Query(ctx, `SELECT id::text, name, mobile, portrait_image, poi_image, dl_image, rc_image, amb_front, amb_inside, vehicle_type, under_progress, error_message, fcm_token, jwt_token, location FROM unverified_drivers ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var drivers []UnverifiedDriver
	for rows.Next() {
		var d UnverifiedDriver
		var locationData []byte
		var errorMessage *string
		var fcmToken, jwtToken *string
		var id string
		if err := rows.Scan(&id, &d.Name, &d.Mobile, &d.PortraitImage, &d.POIImage, &d.DLImage, &d.RCImage, &d.AmbFront, &d.AmbInside, &d.VehicleType, &d.UnderProgress, &errorMessage, &fcmToken, &jwtToken, &locationData); err != nil {
			return nil, err
		}
		d.ID = id
		d.ErrorMessage = errorMessage
		d.FCMToken = fcmToken
		d.JWTToken = jwtToken
		if len(locationData) > 0 && string(locationData) != "null" {
			var loc GeoJSONPoint
			if err := json.Unmarshal(locationData, &loc); err == nil {
				d.Location = &loc
			}
		}
		drivers = append(drivers, d)
	}
	if drivers == nil {
		drivers = []UnverifiedDriver{}
	}
	return drivers, rows.Err()
}

func (s *Store) ListAllUnverifiedDriversWithOffset(ctx context.Context, limit int, offset int) ([]UnverifiedDriver, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, name, mobile, portrait_image, poi_image, dl_image, rc_image, amb_front, amb_inside, vehicle_type, under_progress, error_message, fcm_token, jwt_token, location FROM unverified_drivers ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var drivers []UnverifiedDriver
	for rows.Next() {
		var d UnverifiedDriver
		var locationData []byte
		var errorMessage *string
		var fcmToken, jwtToken *string
		var id string
		if err := rows.Scan(&id, &d.Name, &d.Mobile, &d.PortraitImage, &d.POIImage, &d.DLImage, &d.RCImage, &d.AmbFront, &d.AmbInside, &d.VehicleType, &d.UnderProgress, &errorMessage, &fcmToken, &jwtToken, &locationData); err != nil {
			return nil, err
		}
		d.ID = id
		d.ErrorMessage = errorMessage
		d.FCMToken = fcmToken
		d.JWTToken = jwtToken
		if len(locationData) > 0 && string(locationData) != "null" {
			var loc GeoJSONPoint
			if err := json.Unmarshal(locationData, &loc); err == nil {
				d.Location = &loc
			}
		}
		drivers = append(drivers, d)
	}
	if drivers == nil {
		drivers = []UnverifiedDriver{}
	}
	return drivers, rows.Err()
}

func (s *Store) ListAllUnverifiedDriversForMigration(ctx context.Context) ([]UnverifiedDriver, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, name, mobile, portrait_image, poi_image, dl_image, rc_image, amb_front, amb_inside, vehicle_type, under_progress, error_message, fcm_token, jwt_token, location FROM unverified_drivers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var drivers []UnverifiedDriver
	for rows.Next() {
		u, err := scanUnverifiedDriverRow(rows)
		if err != nil {
			continue
		}
		drivers = append(drivers, *u)
	}
	if drivers == nil {
		drivers = []UnverifiedDriver{}
	}
	return drivers, rows.Err()
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	return s.ListUsersPaginated(ctx, 50, "")
}

func (s *Store) ListUsersWithOffset(ctx context.Context, limit int, offset int) ([]User, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, name, mobile, referral_code, my_referral_code, location, fcm_token, jwt_token FROM users ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		var myReferralCode *string
		var locationData []byte
		var fcmToken, jwtToken *string
		var id string
		if err := rows.Scan(&id, &u.Name, &u.Mobile, &u.ReferralCode, &myReferralCode, &locationData, &fcmToken, &jwtToken); err != nil {
			return nil, err
		}
		u.ID = id
		if myReferralCode != nil {
			u.MyReferralCode = *myReferralCode
		}
		if len(locationData) > 0 && string(locationData) != "null" {
			var loc GeoJSONPoint
			if err := json.Unmarshal(locationData, &loc); err == nil {
				u.Location = &loc
			}
		}
		u.FCMToken = fcmToken
		u.JWTToken = jwtToken
		users = append(users, u)
	}
	if users == nil {
		users = []User{}
	}
	return users, rows.Err()
}

func (s *Store) ListUsersPaginated(ctx context.Context, limit int, cursor string) ([]User, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	if cursor != "" && ids.IsValid(cursor) {
		rows, err = s.pool.Query(ctx, `SELECT id::text, name, mobile, referral_code, my_referral_code, location, fcm_token, jwt_token FROM users WHERE (created_at, id) < ((SELECT created_at FROM users WHERE id=$2::uuid), $2::uuid) ORDER BY created_at DESC, id DESC LIMIT $1`, limit, cursor)
	} else {
		rows, err = s.pool.Query(ctx, `SELECT id::text, name, mobile, referral_code, my_referral_code, location, fcm_token, jwt_token FROM users ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		var myReferralCode *string
		var locationData []byte
		var fcmToken, jwtToken *string
		var id string
		if err := rows.Scan(&id, &u.Name, &u.Mobile, &u.ReferralCode, &myReferralCode, &locationData, &fcmToken, &jwtToken); err != nil {
			return nil, err
		}
		u.ID = id
		if myReferralCode != nil {
			u.MyReferralCode = *myReferralCode
		}
		if len(locationData) > 0 && string(locationData) != "null" {
			var loc GeoJSONPoint
			if err := json.Unmarshal(locationData, &loc); err == nil {
				u.Location = &loc
			}
		}
		u.FCMToken = fcmToken
		u.JWTToken = jwtToken
		users = append(users, u)
	}
	if users == nil {
		users = []User{}
	}
	return users, rows.Err()
}

func (s *Store) RejectUnverifiedDriver(ctx context.Context, id string, errorMessage string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `UPDATE unverified_drivers SET under_progress=false, error_message=$2 WHERE id=$1::uuid`, id, errorMessage)
	return err
}

// ---- Referral Code Management (V20) ----

func (s *Store) SetUserReferralCode(ctx context.Context, userID string, code string) error {
	if !ids.IsValid(userID) {
		return fmt.Errorf("invalid id: %s", userID)
	}
	_, err := s.pool.Exec(ctx, `UPDATE users SET my_referral_code=$2 WHERE id=$1::uuid`, userID, code)
	return err
}

func (s *Store) SetDriverReferralCode(ctx context.Context, driverID string, code string) error {
	if !ids.IsValid(driverID) {
		return fmt.Errorf("invalid id: %s", driverID)
	}
	_, err := s.pool.Exec(ctx, `UPDATE drivers SET my_referral_code=$2 WHERE id=$1::uuid`, driverID, code)
	return err
}

func (s *Store) FindUserByReferralCode(ctx context.Context, code string) (*User, error) {
	row := s.pool.QueryRow(ctx, `SELECT id::text, name, mobile, referral_code, my_referral_code, location, fcm_token, jwt_token FROM users WHERE my_referral_code=$1`, code)
	u, err := scanUserRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return u, nil
}

func (s *Store) FindDriverByReferralCode(ctx context.Context, code string) (*Driver, error) {
	row := s.pool.QueryRow(ctx, `SELECT id::text, name, mobile, photo, vehicle_type, vehicle_registration, wallet_details, wallet_balance, referral_code, my_referral_code, location, fcm_token, jwt_token, last_location_update, details FROM drivers WHERE my_referral_code=$1`, code)
	d, err := scanDriverRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return d, nil
}

func (s *Store) GetUserFCMToken(ctx context.Context, userID string) (*string, error) {
	if !ids.IsValid(userID) {
		return nil, fmt.Errorf("invalid id: %s", userID)
	}
	var fcmToken *string
	err := s.pool.QueryRow(ctx, `SELECT fcm_token FROM users WHERE id=$1::uuid`, userID).Scan(&fcmToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return fcmToken, nil
}

// ---- Hospital MD (Phase 1) ----

func (s *Store) CreateHospitalMD(ctx context.Context, md *HospitalMD) error {
	md.ID = ids.New()
	md.CreatedAt = time.Now()
	if md.Status == "" {
		md.Status = "pending"
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO hospital_mds (id, hospital_pending_id, hospital_id, name, email, mobile, official_number, username, password_hash, status, jwt_token, fcm_token, created_at) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		md.ID, md.HospitalPendingID, md.HospitalID, md.Name, md.Email, md.Mobile, md.OfficialNumber, md.Username, md.PasswordHash, md.Status, md.JWTToken, md.FCMToken, md.CreatedAt)
	return err
}

func (s *Store) FindHospitalMDByMobile(ctx context.Context, mobile string) (*HospitalMD, error) {
	row := s.pool.QueryRow(ctx, `SELECT id::text, hospital_pending_id::text, hospital_id::text, name, email, mobile, official_number, username, password_hash, status, jwt_token, fcm_token, created_at FROM hospital_mds WHERE mobile=$1`, mobile)
	md, err := scanHospitalMDRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return md, nil
}

func (s *Store) FindHospitalMDByUsername(ctx context.Context, username string) (*HospitalMD, error) {
	row := s.pool.QueryRow(ctx, `SELECT id::text, hospital_pending_id::text, hospital_id::text, name, email, mobile, official_number, username, password_hash, status, jwt_token, fcm_token, created_at FROM hospital_mds WHERE username=$1`, username)
	md, err := scanHospitalMDRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return md, nil
}

func (s *Store) FindHospitalMDByID(ctx context.Context, id string) (*HospitalMD, error) {
	if !ids.IsValid(id) {
		return nil, fmt.Errorf("invalid id: %s", id)
	}
	row := s.pool.QueryRow(ctx, `SELECT id::text, hospital_pending_id::text, hospital_id::text, name, email, mobile, official_number, username, password_hash, status, jwt_token, fcm_token, created_at FROM hospital_mds WHERE id=$1::uuid`, id)
	md, err := scanHospitalMDRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return md, nil
}

func (s *Store) FindHospitalMDByHospitalID(ctx context.Context, hospitalID string) (*HospitalMD, error) {
	if !ids.IsValid(hospitalID) {
		return nil, fmt.Errorf("invalid id: %s", hospitalID)
	}
	row := s.pool.QueryRow(ctx, `SELECT id::text, hospital_pending_id::text, hospital_id::text, name, email, mobile, official_number, username, password_hash, status, jwt_token, fcm_token, created_at FROM hospital_mds WHERE hospital_id=$1::uuid`, hospitalID)
	md, err := scanHospitalMDRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return md, nil
}

func (s *Store) UpdateHospitalMD(ctx context.Context, md *HospitalMD) error {
	if !ids.IsValid(md.ID) {
		return fmt.Errorf("invalid id: %s", md.ID)
	}
	_, err := s.pool.Exec(ctx, `UPDATE hospital_mds SET hospital_pending_id=$2::uuid, hospital_id=$3::uuid, name=$4, email=$5, mobile=$6, official_number=$7, username=$8, password_hash=$9, status=$10, jwt_token=$11, fcm_token=$12 WHERE id=$1::uuid`,
		md.ID, md.HospitalPendingID, md.HospitalID, md.Name, md.Email, md.Mobile, md.OfficialNumber, md.Username, md.PasswordHash, md.Status, md.JWTToken, md.FCMToken)
	return err
}

func (s *Store) UpdateHospitalMDJWT(ctx context.Context, id string, token string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `UPDATE hospital_mds SET jwt_token=$2 WHERE id=$1::uuid`, id, token)
	return err
}

func (s *Store) ClearHospitalMDJWT(ctx context.Context, id string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `UPDATE hospital_mds SET jwt_token=NULL WHERE id=$1::uuid`, id)
	return err
}

func (s *Store) SetHospitalMDHospitalID(ctx context.Context, mdID string, hospitalID string) error {
	if !ids.IsValid(mdID) {
		return fmt.Errorf("invalid id: %s", mdID)
	}
	if !ids.IsValid(hospitalID) {
		return fmt.Errorf("invalid id: %s", hospitalID)
	}
	_, err := s.pool.Exec(ctx, `UPDATE hospital_mds SET hospital_id=$2::uuid, status='active' WHERE id=$1::uuid`, mdID, hospitalID)
	return err
}

func (s *Store) ListHospitalMDs(ctx context.Context) ([]HospitalMD, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, hospital_pending_id::text, hospital_id::text, name, email, mobile, official_number, username, password_hash, status, jwt_token, fcm_token, created_at FROM hospital_mds ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []HospitalMD
	for rows.Next() {
		var md HospitalMD
		var id, hpID, hID, username, pwHash, jwtToken, fcmToken *string
		if err := rows.Scan(&id, &hpID, &hID, &md.Name, &md.Email, &md.Mobile, &md.OfficialNumber, &username, &pwHash, &md.Status, &jwtToken, &fcmToken, &md.CreatedAt); err != nil {
			return nil, err
		}
		md.ID = *id
		md.HospitalPendingID = hpID
		md.HospitalID = hID
		md.Username = username
		md.PasswordHash = pwHash
		md.JWTToken = jwtToken
		md.FCMToken = fcmToken
		list = append(list, md)
	}
	if list == nil {
		list = []HospitalMD{}
	}
	return list, nil
}

func (s *Store) BanHospitalMD(ctx context.Context, id string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `UPDATE hospital_mds SET status='banned', jwt_token=NULL WHERE id=$1::uuid`, id)
	return err
}

func (s *Store) UnbanHospitalMD(ctx context.Context, id string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `UPDATE hospital_mds SET status='active' WHERE id=$1::uuid`, id)
	return err
}

func (s *Store) DeleteHospitalMD(ctx context.Context, id string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM hospital_mds WHERE id=$1::uuid`, id)
	return err
}

// ---- Hospital Receptionist (Phase 2) ----

func (s *Store) CreateHospitalReceptionist(ctx context.Context, r *HospitalReceptionist) error {
	r.ID = ids.New()
	r.CreatedAt = time.Now()
	r.InvitedAt = time.Now()
	r.Active = true
	if r.Status == "" {
		r.Status = "invited"
	}
	r.MustChangePassword = true
	_, err := s.pool.Exec(ctx, `INSERT INTO hospital_receptionists (id, hospital_id, created_by_md_id, name, username, password_hash, mobile, active, jwt_token, created_at, email, status, must_change_password, invited_at) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		r.ID, r.HospitalID, r.CreatedByMDID, r.Name, r.Username, r.PasswordHash, r.Mobile, r.Active, r.JWTToken, r.CreatedAt, r.Email, r.Status, r.MustChangePassword, r.InvitedAt)
	return err
}

func (s *Store) FindHospitalReceptionistByUsername(ctx context.Context, username string) (*HospitalReceptionist, error) {
	row := s.pool.QueryRow(ctx, `SELECT id::text, hospital_id::text, created_by_md_id::text, name, username, password_hash, mobile, active, created_at, jwt_token, email, status, must_change_password, invited_at FROM hospital_receptionists WHERE username=$1`, username)
	r, err := scanHospitalReceptionistRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

func (s *Store) FindHospitalReceptionistByEmail(ctx context.Context, email string) (*HospitalReceptionist, error) {
	row := s.pool.QueryRow(ctx, `SELECT id::text, hospital_id::text, created_by_md_id::text, name, username, password_hash, mobile, active, created_at, jwt_token, email, status, must_change_password, invited_at FROM hospital_receptionists WHERE email=$1`, email)
	r, err := scanHospitalReceptionistRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

func (s *Store) FindHospitalReceptionistByID(ctx context.Context, id string) (*HospitalReceptionist, error) {
	if !ids.IsValid(id) {
		return nil, fmt.Errorf("invalid id: %s", id)
	}
	row := s.pool.QueryRow(ctx, `SELECT id::text, hospital_id::text, created_by_md_id::text, name, username, password_hash, mobile, active, created_at, jwt_token, email, status, must_change_password, invited_at FROM hospital_receptionists WHERE id=$1::uuid`, id)
	r, err := scanHospitalReceptionistRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

func (s *Store) ListReceptionistsByHospital(ctx context.Context, hospitalID string) ([]HospitalReceptionist, error) {
	if !ids.IsValid(hospitalID) {
		return nil, fmt.Errorf("invalid id: %s", hospitalID)
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, hospital_id::text, created_by_md_id::text, name, username, password_hash, mobile, active, created_at, jwt_token, email, status, must_change_password, invited_at FROM hospital_receptionists WHERE hospital_id=$1::uuid`, hospitalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []HospitalReceptionist
	for rows.Next() {
		r, err := scanHospitalReceptionistRow(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *r)
	}
	if list == nil {
		list = []HospitalReceptionist{}
	}
	return list, rows.Err()
}

func (s *Store) DeleteHospitalReceptionist(ctx context.Context, id string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM hospital_receptionists WHERE id=$1::uuid`, id)
	return err
}

func (s *Store) UpdateHospitalReceptionistJWT(ctx context.Context, id string, token string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `UPDATE hospital_receptionists SET jwt_token=$2 WHERE id=$1::uuid`, id, token)
	return err
}

func (s *Store) ClearHospitalReceptionistJWT(ctx context.Context, id string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `UPDATE hospital_receptionists SET jwt_token=NULL WHERE id=$1::uuid`, id)
	return err
}

func (s *Store) UpdateReceptionistPassword(ctx context.Context, id string, hash string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `UPDATE hospital_receptionists SET password_hash=$2, must_change_password=false, status='active', active=true WHERE id=$1::uuid`, id, hash)
	return err
}

func (s *Store) UpdateReceptionistResendInvite(ctx context.Context, id string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `UPDATE hospital_receptionists SET invited_at=now(), status='invited' WHERE id=$1::uuid`, id)
	return err
}

func (s *Store) SetReceptionistTempPassword(ctx context.Context, id string, hash string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `UPDATE hospital_receptionists SET password_hash=$2, must_change_password=true, status='invited', active=true, invited_at=now() WHERE id=$1::uuid`, id, hash)
	return err
}

// ---- Ambulance Attendant (chat) ----

func (s *Store) CreateAmbulanceAttendant(ctx context.Context, a *AmbulanceAttendant) error {
	a.ID = ids.New()
	a.CreatedAt = time.Now()
	a.Active = true
	_, err := s.pool.Exec(ctx, `INSERT INTO ambulance_attendants (id, name, mobile, assigned_driver_id, jwt_token, fcm_token, active, created_at) VALUES ($1::uuid, $2, $3, $4::uuid, $5, $6, $7, $8)`,
		a.ID, a.Name, a.Mobile, a.AssignedDriverID, a.JWTToken, a.FCMToken, a.Active, a.CreatedAt)
	return err
}

func (s *Store) FindAmbulanceAttendantByMobile(ctx context.Context, mobile string) (*AmbulanceAttendant, error) {
	row := s.pool.QueryRow(ctx, `SELECT id::text, name, mobile, assigned_driver_id::text, jwt_token, fcm_token, active, created_at FROM ambulance_attendants WHERE mobile=$1`, mobile)
	a, err := scanAmbulanceAttendantRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return a, nil
}

func (s *Store) FindAmbulanceAttendantByID(ctx context.Context, id string) (*AmbulanceAttendant, error) {
	if !ids.IsValid(id) {
		return nil, fmt.Errorf("invalid id: %s", id)
	}
	row := s.pool.QueryRow(ctx, `SELECT id::text, name, mobile, assigned_driver_id::text, jwt_token, fcm_token, active, created_at FROM ambulance_attendants WHERE id=$1::uuid`, id)
	a, err := scanAmbulanceAttendantRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return a, nil
}

func (s *Store) UpdateAmbulanceAttendantJWT(ctx context.Context, id string, token string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `UPDATE ambulance_attendants SET jwt_token=$2 WHERE id=$1::uuid`, id, token)
	return err
}

func (s *Store) ClearAmbulanceAttendantJWT(ctx context.Context, id string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid id: %s", id)
	}
	_, err := s.pool.Exec(ctx, `UPDATE ambulance_attendants SET jwt_token=NULL WHERE id=$1::uuid`, id)
	return err
}

func (s *Store) ListAttendantsByDriver(ctx context.Context, driverID string) ([]AmbulanceAttendant, error) {
	if !ids.IsValid(driverID) {
		return nil, fmt.Errorf("invalid id: %s", driverID)
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, name, mobile, assigned_driver_id::text, jwt_token, fcm_token, active, created_at FROM ambulance_attendants WHERE assigned_driver_id=$1::uuid AND active=true`, driverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []AmbulanceAttendant
	for rows.Next() {
		var a AmbulanceAttendant
		var assignedDriverID *string
		var jwtToken, fcmToken *string
		var id string
		if err := rows.Scan(&id, &a.Name, &a.Mobile, &assignedDriverID, &jwtToken, &fcmToken, &a.Active, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.ID = id
		if assignedDriverID != nil && *assignedDriverID != "" {
			a.AssignedDriverID = assignedDriverID
		}
		a.JWTToken = jwtToken
		a.FCMToken = fcmToken
		list = append(list, a)
	}
	if list == nil {
		list = []AmbulanceAttendant{}
	}
	return list, rows.Err()
}

func (s *Store) DeactivateAttendantsForDriver(ctx context.Context, driverID string) error {
	if !ids.IsValid(driverID) {
		return fmt.Errorf("invalid id: %s", driverID)
	}
	_, err := s.pool.Exec(ctx, `UPDATE ambulance_attendants SET active=false WHERE assigned_driver_id=$1::uuid AND active=true`, driverID)
	return err
}

func (s *Store) DeleteAttendantForDriver(ctx context.Context, driverID, attendantID string) error {
	if !ids.IsValid(driverID) {
		return fmt.Errorf("invalid id: %s", driverID)
	}
	if !ids.IsValid(attendantID) {
		return fmt.Errorf("invalid id: %s", attendantID)
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM ambulance_attendants WHERE id=$1::uuid AND assigned_driver_id=$2::uuid`, attendantID, driverID)
	return err
}

func (s *Store) UpdateAmbulanceAttendant(ctx context.Context, a *AmbulanceAttendant) error {
	if !ids.IsValid(a.ID) {
		return fmt.Errorf("invalid id: %s", a.ID)
	}
	_, err := s.pool.Exec(ctx, `UPDATE ambulance_attendants SET name=$2, mobile=$3, assigned_driver_id=$4::uuid, jwt_token=$5, fcm_token=$6, active=$7 WHERE id=$1::uuid`,
		a.ID, a.Name, a.Mobile, a.AssignedDriverID, a.JWTToken, a.FCMToken, a.Active)
	return err
}
