package admin

import (
	"context"
	"errors"

	"ambigo-backend/internal/ids"

	"github.com/jackc/pgx/v5"
)

// CreateMDAmbulance inserts one ambulance label under a hospital.
func (s *Store) CreateMDAmbulance(ctx context.Context, hospitalID, createdByMDID, label string) (*MDAmbulance, error) {
	if !ids.IsValid(hospitalID) {
		return nil, errors.New("invalid hospital id")
	}
	id := ids.New()
	var createdBy interface{}
	if createdByMDID != "" {
		if !ids.IsValid(createdByMDID) {
			return nil, errors.New("invalid md id")
		}
		createdBy = createdByMDID
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO md_ambulances (id, hospital_id, created_by_md_id, label) VALUES ($1::uuid, $2::uuid, $3::uuid, $4)
		 RETURNING id::text, hospital_id::text, created_by_md_id::text, label, created_at, updated_at`,
		id, hospitalID, createdBy, label)
	var a MDAmbulance
	var createdByNS *string
	if err := row.Scan(&a.ID, &a.HospitalID, &createdByNS, &a.Label, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	a.CreatedByMD = createdByNS
	return &a, nil
}

// ListMDAmbulances returns all ambulances for a hospital.
func (s *Store) ListMDAmbulances(ctx context.Context, hospitalID string) ([]MDAmbulance, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, hospital_id::text, created_by_md_id::text, label, created_at, updated_at
		 FROM md_ambulances WHERE hospital_id=$1::uuid ORDER BY created_at ASC`, hospitalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MDAmbulance{}
	for rows.Next() {
		var a MDAmbulance
		if err := rows.Scan(&a.ID, &a.HospitalID, &a.CreatedByMD, &a.Label, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetMDAmbulance fetches one ambulance, optionally scoped to a hospital.
func (s *Store) GetMDAmbulance(ctx context.Context, ambulanceID, hospitalID string) (*MDAmbulance, error) {
	q := `SELECT id::text, hospital_id::text, created_by_md_id::text, label, created_at, updated_at FROM md_ambulances WHERE id=$1::uuid`
	args := []interface{}{ambulanceID}
	if hospitalID != "" {
		q += ` AND hospital_id=$2::uuid`
		args = append(args, hospitalID)
	}
	var a MDAmbulance
	err := s.pool.QueryRow(ctx, q, args...).Scan(&a.ID, &a.HospitalID, &a.CreatedByMD, &a.Label, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

// AddMDAmbulanceDriver links a mobile under an ambulance as inactive.
// MD must explicitly activate it (activation enforces the single-active rules).
func (s *Store) AddMDAmbulanceDriver(ctx context.Context, ambulanceID, mobile string) (*MDAmbulanceDriver, error) {
	id := ids.New()
	row := s.pool.QueryRow(ctx,
		`INSERT INTO md_ambulance_drivers (id, ambulance_id, driver_mobile, active) VALUES ($1::uuid, $2::uuid, $3, false)
		 RETURNING id::text, ambulance_id::text, driver_mobile, active, created_at, updated_at`,
		id, ambulanceID, mobile)
	var d MDAmbulanceDriver
	if err := row.Scan(&d.ID, &d.AmbulanceID, &d.DriverMobile, &d.Active, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	return &d, nil
}

// ListMDAmbulanceDrivers returns all number links for an ambulance.
func (s *Store) ListMDAmbulanceDrivers(ctx context.Context, ambulanceID string) ([]MDAmbulanceDriver, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, ambulance_id::text, driver_mobile, active, created_at, updated_at
		 FROM md_ambulance_drivers WHERE ambulance_id=$1::uuid ORDER BY created_at ASC`, ambulanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MDAmbulanceDriver{}
	for rows.Next() {
		var d MDAmbulanceDriver
		if err := rows.Scan(&d.ID, &d.AmbulanceID, &d.DriverMobile, &d.Active, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// CountMDLinksForMobile reports how many MD links reference a mobile (0 = public driver).
func (s *Store) CountMDLinksForMobile(ctx context.Context, mobile string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM md_ambulance_drivers WHERE driver_mobile=$1`, mobile).Scan(&n)
	return n, err
}

// ActiveMDAmbulanceForMobile returns the single active ambulance link for a mobile, if any.
func (s *Store) ActiveMDAmbulanceForMobile(ctx context.Context, mobile string) (*MDAmbulanceDriver, error) {
	var d MDAmbulanceDriver
	err := s.pool.QueryRow(ctx,
		`SELECT id::text, ambulance_id::text, driver_mobile, active, created_at, updated_at
		 FROM md_ambulance_drivers WHERE driver_mobile=$1 AND active=true LIMIT 1`, mobile).
		Scan(&d.ID, &d.AmbulanceID, &d.DriverMobile, &d.Active, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &d, nil
}

// ListActiveDriverMobilesByHospital returns distinct mobiles with an active
// link under any ambulance of the hospital. Used for hospital-first dispatch.
func (s *Store) ListActiveDriverMobilesByHospital(ctx context.Context, hospitalID string) ([]string, error) {
	if !ids.IsValid(hospitalID) {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT d.driver_mobile FROM md_ambulance_drivers d
		 JOIN md_ambulances a ON a.id = d.ambulance_id
		 WHERE a.hospital_id=$1::uuid AND d.active=true`, hospitalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		if m != "" {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

// IsMobileBlockedByMD reports whether an MD-linked mobile has no active link.
// Public mobiles (zero links) are never blocked.
func (s *Store) IsMobileBlockedByMD(ctx context.Context, mobile string) (bool, error) {
	var total, active int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM md_ambulance_drivers WHERE driver_mobile=$1`, mobile).Scan(&total); err != nil {
		return false, err
	}
	if total == 0 {
		return false, nil
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM md_ambulance_drivers WHERE driver_mobile=$1 AND active=true`, mobile).Scan(&active); err != nil {
		return false, err
	}
	return active == 0, nil
}

// SetMDAmbulanceDriverActive activates or deactivates one link.
// Activation is transactional: it deactivates losers on both sides first,
// preserving one-active-per-number AND one-active-per-ambulance.
func (s *Store) SetMDAmbulanceDriverActive(ctx context.Context, ambulanceID, mobile string, active bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM md_ambulance_drivers WHERE ambulance_id=$1::uuid AND driver_mobile=$2)`, ambulanceID, mobile).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("number link not found under this ambulance")
	}
	if active {
		if _, err := tx.Exec(ctx, `UPDATE md_ambulance_drivers SET active=false, updated_at=now() WHERE driver_mobile=$1 AND active=true`, mobile); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE md_ambulance_drivers SET active=false, updated_at=now() WHERE ambulance_id=$1::uuid AND active=true`, ambulanceID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE md_ambulance_drivers SET active=true, updated_at=now() WHERE ambulance_id=$1::uuid AND driver_mobile=$2`, ambulanceID, mobile); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE md_ambulance_drivers SET active=false, updated_at=now() WHERE ambulance_id=$1::uuid AND driver_mobile=$2`, ambulanceID, mobile); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
