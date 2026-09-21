package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"ambigo-backend/internal/ids"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

// NewStore creates a Store backed by a pgx pool. The pool is shared across
// all admin tables (ambulance_types, admins) which now live in a single
// Postgres database (previously Data + Users mongo databases).
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// NewStoreWithPool is an alias kept for grep-ability; prefer NewStore.
func NewStoreWithPool(pool *pgxpool.Pool) *Store { return NewStore(pool) }

func (s *Store) CreateAmbulanceType(ctx context.Context, amb *AmbulanceType) error {
	if ids.IsZero(amb.ID) {
		amb.ID = ids.New()
	}
	pricingJSON, err := json.Marshal(amb.PricingTier)
	if err != nil {
		return fmt.Errorf("marshal pricing_tier: %w", err)
	}
	if pricingJSON == nil || string(pricingJSON) == "null" {
		pricingJSON = []byte("[]")
	}
	const q = `INSERT INTO ambulance_types (id, name, photo, helper_included, otp_required, listing_threshold, base_fare, driver_share, pricing_tier)
	           VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb)`
	_, err = s.pool.Exec(ctx, q, amb.ID, amb.Name, amb.Photo, amb.HelperIncluded, amb.OTPRequired, amb.ListingThreshold, amb.BaseFare, amb.DriverShare, pricingJSON)
	return err
}

func (s *Store) ListAmbulanceTypes(ctx context.Context) ([]AmbulanceType, error) {
	const q = `SELECT id, name, photo, helper_included, otp_required, listing_threshold, base_fare, driver_share, pricing_tier FROM ambulance_types`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []AmbulanceType
	for rows.Next() {
		var amb AmbulanceType
		var pricingBytes []byte
		if err := rows.Scan(&amb.ID, &amb.Name, &amb.Photo, &amb.HelperIncluded, &amb.OTPRequired, &amb.ListingThreshold, &amb.BaseFare, &amb.DriverShare, &pricingBytes); err != nil {
			return nil, err
		}
		if len(pricingBytes) > 0 {
			if err := json.Unmarshal(pricingBytes, &amb.PricingTier); err != nil {
				return nil, fmt.Errorf("unmarshal pricing_tier: %w", err)
			}
		}
		if amb.PricingTier == nil {
			amb.PricingTier = []PricingTier{}
		}
		list = append(list, amb)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []AmbulanceType{}
	}
	return list, nil
}

func (s *Store) GetAmbulanceTypeByID(ctx context.Context, idStr string) (*AmbulanceType, error) {
	if !ids.IsValid(idStr) {
		return nil, fmt.Errorf("invalid ambulance_type id: %s", idStr)
	}
	const q = `SELECT id, name, photo, helper_included, otp_required, listing_threshold, base_fare, driver_share, pricing_tier FROM ambulance_types WHERE id=$1::uuid`
	var amb AmbulanceType
	var pricingBytes []byte
	err := s.pool.QueryRow(ctx, q, idStr).Scan(&amb.ID, &amb.Name, &amb.Photo, &amb.HelperIncluded, &amb.OTPRequired, &amb.ListingThreshold, &amb.BaseFare, &amb.DriverShare, &pricingBytes)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if len(pricingBytes) > 0 {
		if err := json.Unmarshal(pricingBytes, &amb.PricingTier); err != nil {
			return nil, fmt.Errorf("unmarshal pricing_tier: %w", err)
		}
	}
	if amb.PricingTier == nil {
		amb.PricingTier = []PricingTier{}
	}
	return &amb, nil
}

func (s *Store) UpdateAmbulanceType(ctx context.Context, amb *AmbulanceType) error {
	if ids.IsZero(amb.ID) {
		return fmt.Errorf("ambulance_type id is required")
	}
	pricingJSON, err := json.Marshal(amb.PricingTier)
	if err != nil {
		return fmt.Errorf("marshal pricing_tier: %w", err)
	}
	if pricingJSON == nil || string(pricingJSON) == "null" {
		pricingJSON = []byte("[]")
	}
	const q = `UPDATE ambulance_types SET name=$1, photo=$2, helper_included=$3, otp_required=$4, listing_threshold=$5, base_fare=$6, driver_share=$7, pricing_tier=$8::jsonb WHERE id=$9::uuid`
	_, err = s.pool.Exec(ctx, q, amb.Name, amb.Photo, amb.HelperIncluded, amb.OTPRequired, amb.ListingThreshold, amb.BaseFare, amb.DriverShare, pricingJSON, amb.ID)
	return err
}

func (s *Store) DeleteAmbulanceType(ctx context.Context, id string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid ambulance_type id: %s", id)
	}
	const q = `DELETE FROM ambulance_types WHERE id=$1::uuid`
	_, err := s.pool.Exec(ctx, q, id)
	return err
}

func scanAdmin(row pgx.Row) (*Admin, error) {
	var a Admin
	var username sql.NullString
	var mobile sql.NullString
	err := row.Scan(&a.ID, &username, &a.HashedPassword, &a.Name, &a.Role, &a.Active, &mobile)
	if err != nil {
		return nil, err
	}
	if username.Valid {
		a.Username = username.String
	}
	if mobile.Valid {
		a.Mobile = mobile.String
	}
	return &a, nil
}

func (s *Store) FindAdminByUsername(ctx context.Context, username string) (*Admin, error) {
	const q = `SELECT id, username, hashed_password, name, role, active, mobile FROM admins WHERE username=$1`
	row := s.pool.QueryRow(ctx, q, username)
	a, err := scanAdmin(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return a, nil
}

func (s *Store) FindAdminByMobile(ctx context.Context, mobile string) (*Admin, error) {
	const q = `SELECT id, username, hashed_password, name, role, active, mobile FROM admins WHERE mobile=$1`
	row := s.pool.QueryRow(ctx, q, mobile)
	a, err := scanAdmin(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return a, nil
}

func (s *Store) FindAdminByID(ctx context.Context, id string) (*Admin, error) {
	if !ids.IsValid(id) {
		return nil, fmt.Errorf("invalid admin id: %s", id)
	}
	const q = `SELECT id, username, hashed_password, name, role, active, mobile FROM admins WHERE id=$1::uuid`
	row := s.pool.QueryRow(ctx, q, id)
	a, err := scanAdmin(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return a, nil
}

func (s *Store) UpdateAdminFCM(ctx context.Context, id string, fcmToken string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid admin id: %s", id)
	}
	const q = `UPDATE admins SET fcm_token=$1, updated_at=now() WHERE id=$2::uuid`
	_, err := s.pool.Exec(ctx, q, fcmToken, id)
	return err
}

// ListActiveAdminFCMTokens returns FCM tokens for active admins, used for
// stopped-vehicle emergency escalation (mirrors the driver-offer push path).
func (s *Store) ListActiveAdminFCMTokens(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT fcm_token FROM admins WHERE active=true AND fcm_token IS NOT NULL AND fcm_token<>''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		if t != "" {
			tokens = append(tokens, t)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tokens, nil
}

func (s *Store) UpdateAdminLocation(ctx context.Context, id string, location *GeoJSON) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid admin id: %s", id)
	}
	var locJSON []byte
	var err error
	if location != nil {
		locJSON, err = json.Marshal(location)
		if err != nil {
			return fmt.Errorf("marshal location: %w", err)
		}
	}
	const qWith = `UPDATE admins SET location=$1::jsonb, updated_at=now() WHERE id=$2::uuid`
	const qNull = `UPDATE admins SET location=NULL, updated_at=now() WHERE id=$1::uuid`
	if location == nil {
		_, err = s.pool.Exec(ctx, qNull, id)
	} else {
		_, err = s.pool.Exec(ctx, qWith, locJSON, id)
	}
	return err
}

func (s *Store) UpdateAdminPassword(ctx context.Context, id string, hashedPassword string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid admin id: %s", id)
	}
	const q = `UPDATE admins SET hashed_password=$1, updated_at=now() WHERE id=$2::uuid`
	_, err := s.pool.Exec(ctx, q, hashedPassword, id)
	return err
}
