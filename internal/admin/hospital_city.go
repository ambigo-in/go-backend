package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"ambigo-backend/internal/ids"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HospitalCity configures the area for which hospitals are seeded from Google.
// Stored in the hospital_cities table so cities can be added at
// runtime without redeploying.
type HospitalCity struct {
	ID              string    `db:"id" json:"_id"`
	Name            string    `db:"name" json:"name"`
	Lat             float64   `db:"lat" json:"lat"`
	Lng             float64   `db:"lng" json:"lng"`
	RadiusM         int64     `db:"radius_m" json:"radius_m"`
	MaxPerCategory  int       `db:"max_per_category" json:"max_per_category"`
	LastFetched     time.Time `db:"last_fetched" json:"last_fetched,omitempty"`
	Enabled         bool      `db:"enabled" json:"enabled"`
}

type HospitalCityStore struct {
	pool *pgxpool.Pool
}

func NewHospitalCityStore(pool *pgxpool.Pool) *HospitalCityStore {
	return &HospitalCityStore{pool: pool}
}

func scanHospitalCity(row pgx.Row) (*HospitalCity, error) {
	var c HospitalCity
	var lastFetched sql.NullTime
	var maxPerCategory sql.NullInt32
	err := row.Scan(&c.ID, &c.Name, &c.Lat, &c.Lng, &c.RadiusM, &maxPerCategory, &lastFetched, &c.Enabled)
	if err != nil {
		return nil, err
	}
	if maxPerCategory.Valid {
		c.MaxPerCategory = int(maxPerCategory.Int32)
	} else {
		c.MaxPerCategory = 40
	}
	if lastFetched.Valid {
		c.LastFetched = lastFetched.Time
	}
	return &c, nil
}

const hospitalCityColumns = `id, name, lat, lng, radius_m, max_per_category, last_fetched, enabled`

// ListEnabled returns all active cities to seed.
func (s *HospitalCityStore) ListEnabled(ctx context.Context) ([]HospitalCity, error) {
	const q = `SELECT ` + hospitalCityColumns + ` FROM hospital_cities WHERE enabled=true`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []HospitalCity
	for rows.Next() {
		c, err := scanHospitalCity(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []HospitalCity{}
	}
	return list, nil
}

// MarkFetched records the last successful seed time for a city.
func (s *HospitalCityStore) MarkFetched(ctx context.Context, id string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid hospital_city id: %s", id)
	}
	const q = `UPDATE hospital_cities SET last_fetched=now() WHERE id=$1::uuid`
	_, err := s.pool.Exec(ctx, q, id)
	return err
}

// ListAll returns every configured city (enabled or not), newest first.
func (s *HospitalCityStore) ListAll(ctx context.Context) ([]HospitalCity, error) {
	const q = `SELECT ` + hospitalCityColumns + ` FROM hospital_cities`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []HospitalCity
	for rows.Next() {
		c, err := scanHospitalCity(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []HospitalCity{}
	}
	return list, nil
}

// GetByID returns a single city by its id.
func (s *HospitalCityStore) GetByID(ctx context.Context, id string) (*HospitalCity, error) {
	if !ids.IsValid(id) {
		return nil, fmt.Errorf("invalid hospital_city id: %s", id)
	}
	const q = `SELECT ` + hospitalCityColumns + ` FROM hospital_cities WHERE id=$1::uuid`
	c, err := scanHospitalCity(s.pool.QueryRow(ctx, q, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return c, nil
}

// Create inserts a new city.
func (s *HospitalCityStore) Create(ctx context.Context, c *HospitalCity) error {
	if ids.IsZero(c.ID) {
		c.ID = ids.New()
	}
	if c.MaxPerCategory < 5 {
		c.MaxPerCategory = 5
	}
	if c.MaxPerCategory > 60 {
		c.MaxPerCategory = 60
	}
	var lastFetched interface{}
	if c.LastFetched.IsZero() {
		lastFetched = nil
	} else {
		lastFetched = c.LastFetched
	}
	const q = `INSERT INTO hospital_cities (id, name, lat, lng, radius_m, max_per_category, last_fetched, enabled) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`
	_, err := s.pool.Exec(ctx, q, c.ID, c.Name, c.Lat, c.Lng, c.RadiusM, c.MaxPerCategory, lastFetched, c.Enabled)
	return err
}

// Update replaces a city's config by id.
func (s *HospitalCityStore) Update(ctx context.Context, c *HospitalCity) error {
	if ids.IsZero(c.ID) {
		return fmt.Errorf("hospital_city id is required")
	}
	if c.MaxPerCategory < 5 {
		c.MaxPerCategory = 5
	}
	if c.MaxPerCategory > 60 {
		c.MaxPerCategory = 60
	}
	var lastFetched interface{}
	if c.LastFetched.IsZero() {
		lastFetched = nil
	} else {
		lastFetched = c.LastFetched
	}
	const q = `UPDATE hospital_cities SET name=$1, lat=$2, lng=$3, radius_m=$4, max_per_category=$5, last_fetched=$6, enabled=$7 WHERE id=$8::uuid`
	_, err := s.pool.Exec(ctx, q, c.Name, c.Lat, c.Lng, c.RadiusM, c.MaxPerCategory, lastFetched, c.Enabled, c.ID)
	return err
}

// Delete removes a city by id.
func (s *HospitalCityStore) Delete(ctx context.Context, id string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid hospital_city id: %s", id)
	}
	const q = `DELETE FROM hospital_cities WHERE id=$1::uuid`
	_, err := s.pool.Exec(ctx, q, id)
	return err
}
