package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"ambigo-backend/internal/ids"
	"ambigo-backend/internal/location"
	"ambigo-backend/internal/translation"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GeoJSON is the standard GeoJSON Point (lng-first coordinates).
type GeoJSON struct {
	Type        string    `db:"type" json:"type"`
	Coordinates []float64 `db:"coordinates" json:"coordinates"`
}

// Timing is the daily open/close window (24h string format, e.g. "10:00 AM").
type Timing struct {
	Start string `db:"start" json:"start"`
	End   string `db:"end" json:"end"`
}

// HospitalSource distinguishes admin-curated hospitals from Google-seeded ones.
const (
	HospitalSourceAdmin  = "admin"
	HospitalSourceGoogle = "google"
)

// Hospital types used for the Emergency / Non-Emergency split.
const (
	HospitalTypeGovernment      = "government"
	HospitalTypeMultiSpeciality = "multi_speciality"
	HospitalTypePrivate         = "private"
	HospitalTypeClinic          = "clinic"
	HospitalTypeGeneral         = "general"
)

// Hospital categories exposed to the app.
const (
	HospitalCategoryEmergency    = "emergency"
	HospitalCategoryNonEmergency = "non_emergency"
)

// IsEmergencyCategory reports whether a category should appear in Emergency.
func IsEmergencyCategory(category string) bool {
	return category == HospitalCategoryEmergency
}

// HospitalResolution / ring used to bucket hospitals into H3 cells for fast
// ring lookups (~25-30km coverage with ring-2 at resolution 5).
const (
	HospitalH3Resolution = 5
	HospitalH3Ring       = 2
)

// Hospital is the shared hospital document. Extra fields (place_id, h3_cells,
// source, fetched_at) are ignored by the mobile app but power the H3 lookup.
type Hospital struct {
	ID           string         `db:"id" json:"_id"`
	Name         translation.Map `db:"name" json:"name"`
	Address      translation.Map `db:"address" json:"address"`
	City         translation.Map `db:"city" json:"city"`
	Location     GeoJSON        `db:"location" json:"location"`
	Timing       *Timing        `db:"timing" json:"timing,omitempty"`
	AlwaysOpen   bool           `db:"always_open" json:"always_open"`
	Services     []string       `db:"services" json:"services"`
	PlaceID      string         `db:"place_id" json:"place_id,omitempty"`
	H3Cells      []string       `db:"h3_cells" json:"h3_cells,omitempty"`
	Source       string         `db:"source" json:"source,omitempty"`
	FetchedAt    time.Time      `db:"fetched_at" json:"fetched_at,omitempty"`
	DistanceKm   float64        `db:"-" json:"distance_km,omitempty"`
	HospitalType string         `db:"hospital_type" json:"hospital_type,omitempty"`
	Category     string         `db:"category" json:"category,omitempty"`
	GoogleTypes  []string       `db:"google_types" json:"google_types,omitempty"`
	TypeLocked   bool           `db:"type_locked" json:"type_locked,omitempty"`
}

// DuplicateMergeRadiusKm bounds coordinate-based duplicate matching.
// A Google result or MD pin within this distance of a known hospital row
// is treated as the same building (hospital campuses).
const DuplicateMergeRadiusKm = 0.15

func hospitalHaversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const R = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180.0
	dLng := (lng2 - lng1) * math.Pi / 180.0
	lat1Rad := lat1 * math.Pi / 180.0
	lat2Rad := lat2 * math.Pi / 180.0
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1Rad)*math.Cos(lat2Rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// FindNearby returns hospitals within radiusKm of lng/lat, nearest first.
// It fetches the H3 bucket then filters by exact distance in Go.
func (s *HospitalStore) FindNearby(ctx context.Context, lng, lat, radiusKm float64) ([]Hospital, error) {
	cell := location.GetH3CellAtResolution(lat, lng, HospitalH3Resolution)
	if cell == "" {
		return []Hospital{}, nil
	}
	cells, err := location.GetNeighborCellsAtRing(cell, HospitalH3Ring)
	if err != nil {
		return nil, err
	}
	list, err := s.FindByCells(ctx, cells)
	if err != nil {
		return nil, err
	}
	out := make([]Hospital, 0, len(list))
	for _, h := range list {
		if len(h.Location.Coordinates) != 2 {
			continue
		}
		h.DistanceKm = hospitalHaversineKm(lat, lng, h.Location.Coordinates[1], h.Location.Coordinates[0])
		if h.DistanceKm <= radiusKm {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DistanceKm < out[j].DistanceKm })
	return out, nil
}

// HospitalDisplayName returns the en_US name, falling back to any locale.
func HospitalDisplayName(name translation.Map) string {
	if s := strings.TrimSpace(name["en_US"]); s != "" {
		return s
	}
	for _, s := range name {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func normalizeHospitalName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// HospitalNamesMatch reports whether two hospital names plausibly denote the
// same building (normalized containment either way, min 4 chars). Guards
// against merging distinct neighboring clinics that share coordinates area.
func HospitalNamesMatch(a, b string) bool {
	na, nb := normalizeHospitalName(a), normalizeHospitalName(b)
	if len(na) < 4 || len(nb) < 4 {
		return false
	}
	return strings.Contains(na, nb) || strings.Contains(nb, na)
}

// PickMergeTarget selects the best duplicate-merge survivor from nearby rows
// matching name. Rows already carrying a Google place_id win (future seeds
// find them by place_id, preventing re-duplication); otherwise nearest first.
func PickMergeTarget(candidates []Hospital, name string) *Hospital {
	var best *Hospital
	for i := range candidates {
		if !HospitalNamesMatch(HospitalDisplayName(candidates[i].Name), name) {
			continue
		}
		if best == nil {
			best = &candidates[i]
			continue
		}
		if best.PlaceID == "" && candidates[i].PlaceID != "" {
			best = &candidates[i]
		}
	}
	return best
}

// BuildH3Cells computes the H3 cell(s) covering the hospital's location.
func BuildH3Cells(lng, lat float64) []string {
	cell := location.GetH3CellAtResolution(lat, lng, HospitalH3Resolution)
	if cell == "" {
		return []string{}
	}
	return []string{cell}
}

type HospitalStore struct {
	pool *pgxpool.Pool
}

func NewHospitalStore(pool *pgxpool.Pool) *HospitalStore {
	return &HospitalStore{pool: pool}
}

func marshalJSON(v interface{}) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if string(b) == "null" {
		return nil, nil
	}
	return b, nil
}

func scanHospital(row pgx.Row) (*Hospital, error) {
	var h Hospital
	var (
		id            string
		nameBytes     []byte
		addressBytes  []byte
		cityBytes     []byte
		locationBytes []byte
		timingBytes   []byte
		alwaysOpen    bool
		services      []string
		placeID       sql.NullString
		h3Cells       []string
		source        sql.NullString
		fetchedAt     sql.NullTime
		hospitalType  sql.NullString
		category      sql.NullString
		googleTypes   []string
		typeLocked    bool
	)
	err := row.Scan(
		&id,
		&nameBytes,
		&addressBytes,
		&cityBytes,
		&locationBytes,
		&timingBytes,
		&alwaysOpen,
		&services,
		&placeID,
		&h3Cells,
		&source,
		&fetchedAt,
		&hospitalType,
		&category,
		&googleTypes,
		&typeLocked,
	)
	if err != nil {
		return nil, err
	}
	h.ID = id
	if len(nameBytes) > 0 {
		if err := json.Unmarshal(nameBytes, &h.Name); err != nil {
			return nil, fmt.Errorf("unmarshal hospital name: %w", err)
		}
	}
	if len(addressBytes) > 0 {
		if err := json.Unmarshal(addressBytes, &h.Address); err != nil {
			return nil, fmt.Errorf("unmarshal hospital address: %w", err)
		}
	}
	if len(cityBytes) > 0 {
		if err := json.Unmarshal(cityBytes, &h.City); err != nil {
			return nil, fmt.Errorf("unmarshal hospital city: %w", err)
		}
	}
	if len(locationBytes) > 0 {
		if err := json.Unmarshal(locationBytes, &h.Location); err != nil {
			return nil, fmt.Errorf("unmarshal hospital location: %w", err)
		}
	}
	if len(timingBytes) > 0 && string(timingBytes) != "null" {
		var t Timing
		if err := json.Unmarshal(timingBytes, &t); err != nil {
			return nil, fmt.Errorf("unmarshal hospital timing: %w", err)
		}
		h.Timing = &t
	}
	h.AlwaysOpen = alwaysOpen
	if services != nil {
		h.Services = services
	} else {
		h.Services = []string{}
	}
	if placeID.Valid {
		h.PlaceID = placeID.String
	}
	if h3Cells != nil {
		h.H3Cells = h3Cells
	} else {
		h.H3Cells = []string{}
	}
	if source.Valid {
		h.Source = source.String
	}
	if fetchedAt.Valid {
		h.FetchedAt = fetchedAt.Time
	}
	if hospitalType.Valid {
		h.HospitalType = hospitalType.String
	}
	if category.Valid {
		h.Category = category.String
	}
	if googleTypes != nil {
		h.GoogleTypes = googleTypes
	} else {
		h.GoogleTypes = []string{}
	}
	h.TypeLocked = typeLocked
	if h.Name == nil {
		h.Name = translation.Map{}
	}
	if h.Address == nil {
		h.Address = translation.Map{}
	}
	if h.City == nil {
		h.City = translation.Map{}
	}
	return &h, nil
}

const hospitalColumns = `id, name, address, city, location, timing, always_open, services, place_id, h3_cells, source, fetched_at, hospital_type, category, google_types, type_locked`

func (s *HospitalStore) CreateHospital(ctx context.Context, h *Hospital) error {
	if ids.IsZero(h.ID) {
		h.ID = ids.New()
	}
	if len(h.H3Cells) == 0 {
		if len(h.Location.Coordinates) == 2 {
			h.H3Cells = BuildH3Cells(h.Location.Coordinates[0], h.Location.Coordinates[1])
		}
	}
	if h.Services == nil {
		h.Services = []string{}
	}
	if h.GoogleTypes == nil {
		h.GoogleTypes = []string{}
	}
	if h.H3Cells == nil {
		h.H3Cells = []string{}
	}
	nameJSON, err := json.Marshal(h.Name)
	if err != nil {
		return fmt.Errorf("marshal hospital name: %w", err)
	}
	addressJSON, err := json.Marshal(h.Address)
	if err != nil {
		return fmt.Errorf("marshal hospital address: %w", err)
	}
	cityJSON, err := json.Marshal(h.City)
	if err != nil {
		return fmt.Errorf("marshal hospital city: %w", err)
	}
	locationJSON, err := json.Marshal(h.Location)
	if err != nil {
		return fmt.Errorf("marshal hospital location: %w", err)
	}
	var timingJSON []byte
	if h.Timing != nil {
		timingJSON, err = json.Marshal(h.Timing)
		if err != nil {
			return fmt.Errorf("marshal hospital timing: %w", err)
		}
	}
	var placeID interface{}
	if h.PlaceID == "" {
		placeID = nil
	} else {
		placeID = h.PlaceID
	}
	var fetchedAt interface{}
	if h.FetchedAt.IsZero() {
		fetchedAt = nil
	} else {
		fetchedAt = h.FetchedAt
	}
	var hospitalType interface{}
	if h.HospitalType == "" {
		hospitalType = nil
	} else {
		hospitalType = h.HospitalType
	}
	var category interface{}
	if h.Category == "" {
		category = nil
	} else {
		category = h.Category
	}
	var source interface{}
	if h.Source == "" {
		source = HospitalSourceAdmin
	} else {
		source = h.Source
	}
	const q = `INSERT INTO hospitals (id, name, address, city, location, timing, always_open, services, place_id, h3_cells, source, fetched_at, hospital_type, category, google_types, type_locked)
	           VALUES ($1,$2::jsonb,$3::jsonb,$4::jsonb,$5::jsonb,$6::jsonb,$7,$8::text[],$9,$10::text[],$11,$12,$13,$14,$15::text[],$16)`
	_, err = s.pool.Exec(ctx, q,
		h.ID,
		nameJSON,
		addressJSON,
		cityJSON,
		locationJSON,
		timingJSON,
		h.AlwaysOpen,
		h.Services,
		placeID,
		h.H3Cells,
		source,
		fetchedAt,
		hospitalType,
		category,
		h.GoogleTypes,
		h.TypeLocked,
	)
	return err
}

func (s *HospitalStore) ListHospitals(ctx context.Context) ([]Hospital, error) {
	return s.ListHospitalsPaginated(ctx, 50, "")
}

func (s *HospitalStore) ListHospitalsPaginated(ctx context.Context, limit int, cursor string) ([]Hospital, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	var q string
	var rows pgx.Rows
	var err error
	if cursor != "" && ids.IsValid(cursor) {
		q = `SELECT ` + hospitalColumns + ` FROM hospitals WHERE (created_at, id) < ((SELECT created_at FROM hospitals WHERE id=$2::uuid), $2::uuid) ORDER BY created_at DESC, id DESC LIMIT $1`
		rows, err = s.pool.Query(ctx, q, limit, cursor)
	} else {
		q = `SELECT ` + hospitalColumns + ` FROM hospitals ORDER BY created_at DESC, id DESC LIMIT $1`
		rows, err = s.pool.Query(ctx, q, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Hospital
	for rows.Next() {
		h, err := scanHospital(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Hospital{}
	}
	return list, nil
}

func (s *HospitalStore) ListHospitalsWithOffset(ctx context.Context, limit int, offset int) ([]Hospital, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	q := `SELECT ` + hospitalColumns + ` FROM hospitals ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2`
	rows, err := s.pool.Query(ctx, q, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Hospital
	for rows.Next() {
		h, err := scanHospital(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Hospital{}
	}
	return list, nil
}

// FindByCells returns hospitals bucketed into any of the given H3 cells.
func (s *HospitalStore) FindByCells(ctx context.Context, cells []string) ([]Hospital, error) {
	if len(cells) == 0 {
		return []Hospital{}, nil
	}
	q := `SELECT ` + hospitalColumns + ` FROM hospitals WHERE h3_cells && $1::text[]`
	rows, err := s.pool.Query(ctx, q, cells)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Hospital
	for rows.Next() {
		h, err := scanHospital(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Hospital{}
	}
	return list, nil
}

// FindByPlaceID looks up a hospital by its Google place_id (dedup key).
func (s *HospitalStore) FindByPlaceID(ctx context.Context, placeID string) (*Hospital, error) {
	if placeID == "" {
		return nil, nil
	}
	q := `SELECT ` + hospitalColumns + ` FROM hospitals WHERE place_id=$1`
	h, err := scanHospital(s.pool.QueryRow(ctx, q, placeID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return h, nil
}

// UpsertByPlaceID inserts or replaces a Google-sourced hospital keyed by
// place_id. Returns true when the document was inserted or modified.
func (s *HospitalStore) UpsertByPlaceID(ctx context.Context, h *Hospital) (changed bool, err error) {
	if len(h.H3Cells) == 0 {
		if len(h.Location.Coordinates) == 2 {
			h.H3Cells = BuildH3Cells(h.Location.Coordinates[0], h.Location.Coordinates[1])
		}
	}
	if h.PlaceID == "" {
		// No place_id — cannot upsert, fallback to plain insert.
		if ids.IsZero(h.ID) {
			h.ID = ids.New()
		}
		if err := s.CreateHospital(ctx, h); err != nil {
			return false, err
		}
		return true, nil
	}
	if ids.IsZero(h.ID) {
		h.ID = ids.New()
	}
	if h.Services == nil {
		h.Services = []string{}
	}
	if h.GoogleTypes == nil {
		h.GoogleTypes = []string{}
	}
	if h.H3Cells == nil {
		h.H3Cells = []string{}
	}
	nameJSON, err := json.Marshal(h.Name)
	if err != nil {
		return false, fmt.Errorf("marshal hospital name: %w", err)
	}
	addressJSON, err := json.Marshal(h.Address)
	if err != nil {
		return false, fmt.Errorf("marshal hospital address: %w", err)
	}
	cityJSON, err := json.Marshal(h.City)
	if err != nil {
		return false, fmt.Errorf("marshal hospital city: %w", err)
	}
	locationJSON, err := json.Marshal(h.Location)
	if err != nil {
		return false, fmt.Errorf("marshal hospital location: %w", err)
	}
	var timingJSON []byte
	if h.Timing != nil {
		timingJSON, err = json.Marshal(h.Timing)
		if err != nil {
			return false, fmt.Errorf("marshal hospital timing: %w", err)
		}
	}
	var fetchedAt interface{}
	if h.FetchedAt.IsZero() {
		fetchedAt = nil
	} else {
		fetchedAt = h.FetchedAt
	}
	var hospitalType interface{}
	if h.HospitalType == "" {
		hospitalType = nil
	} else {
		hospitalType = h.HospitalType
	}
	var category interface{}
	if h.Category == "" {
		category = nil
	} else {
		category = h.Category
	}
	var source interface{}
	if h.Source == "" {
		source = HospitalSourceAdmin
	} else {
		source = h.Source
	}
	const q = `INSERT INTO hospitals (id, name, address, city, location, timing, always_open, services, place_id, h3_cells, source, fetched_at, hospital_type, category, google_types, type_locked)
	           VALUES ($1,$2::jsonb,$3::jsonb,$4::jsonb,$5::jsonb,$6::jsonb,$7,$8::text[],$9,$10::text[],$11,$12,$13,$14,$15::text[],$16)
	           ON CONFLICT (place_id) DO UPDATE SET
	             name=EXCLUDED.name,
	             address=EXCLUDED.address,
	             city=EXCLUDED.city,
	             location=EXCLUDED.location,
	             timing=EXCLUDED.timing,
	             always_open=EXCLUDED.always_open,
	             services=EXCLUDED.services,
	             h3_cells=EXCLUDED.h3_cells,
	             source=EXCLUDED.source,
	             fetched_at=EXCLUDED.fetched_at,
	             hospital_type=EXCLUDED.hospital_type,
	             category=EXCLUDED.category,
	             google_types=EXCLUDED.google_types,
	             type_locked=EXCLUDED.type_locked`
	tag, err := s.pool.Exec(ctx, q,
		h.ID,
		nameJSON,
		addressJSON,
		cityJSON,
		locationJSON,
		timingJSON,
		h.AlwaysOpen,
		h.Services,
		h.PlaceID,
		h.H3Cells,
		source,
		fetchedAt,
		hospitalType,
		category,
		h.GoogleTypes,
		h.TypeLocked,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *HospitalStore) UpdateHospital(ctx context.Context, h *Hospital) error {
	if ids.IsZero(h.ID) {
		return fmt.Errorf("hospital id is required")
	}
	nameJSON, err := json.Marshal(h.Name)
	if err != nil {
		return fmt.Errorf("marshal hospital name: %w", err)
	}
	addressJSON, err := json.Marshal(h.Address)
	if err != nil {
		return fmt.Errorf("marshal hospital address: %w", err)
	}
	cityJSON, err := json.Marshal(h.City)
	if err != nil {
		return fmt.Errorf("marshal hospital city: %w", err)
	}
	locationJSON, err := json.Marshal(h.Location)
	if err != nil {
		return fmt.Errorf("marshal hospital location: %w", err)
	}
	var timingJSON []byte
	if h.Timing != nil {
		timingJSON, err = json.Marshal(h.Timing)
		if err != nil {
			return fmt.Errorf("marshal hospital timing: %w", err)
		}
	}
	var placeID interface{}
	if h.PlaceID == "" {
		placeID = nil
	} else {
		placeID = h.PlaceID
	}
	var fetchedAt interface{}
	if h.FetchedAt.IsZero() {
		fetchedAt = nil
	} else {
		fetchedAt = h.FetchedAt
	}
	var hospitalType interface{}
	if h.HospitalType == "" {
		hospitalType = nil
	} else {
		hospitalType = h.HospitalType
	}
	var category interface{}
	if h.Category == "" {
		category = nil
	} else {
		category = h.Category
	}
	var source interface{}
	if h.Source == "" {
		source = nil
	} else {
		source = h.Source
	}
	const q = `UPDATE hospitals SET name=$1::jsonb, address=$2::jsonb, city=$3::jsonb, location=$4::jsonb, timing=$5::jsonb, always_open=$6, services=$7::text[], place_id=$8, h3_cells=$9::text[], source=$10, fetched_at=$11, hospital_type=$12, category=$13, google_types=$14::text[], type_locked=$15 WHERE id=$16::uuid`
	_, err = s.pool.Exec(ctx, q,
		nameJSON,
		addressJSON,
		cityJSON,
		locationJSON,
		timingJSON,
		h.AlwaysOpen,
		h.Services,
		placeID,
		h.H3Cells,
		source,
		fetchedAt,
		hospitalType,
		category,
		h.GoogleTypes,
		h.TypeLocked,
		h.ID,
	)
	return err
}

func (s *HospitalStore) FindByID(ctx context.Context, id string) (*Hospital, error) {
	if !ids.IsValid(id) {
		return nil, fmt.Errorf("invalid hospital id: %s", id)
	}
	q := `SELECT ` + hospitalColumns + ` FROM hospitals WHERE id=$1::uuid`
	h, err := scanHospital(s.pool.QueryRow(ctx, q, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return h, nil
}

func (s *HospitalStore) DeleteHospital(ctx context.Context, id string) error {
	if !ids.IsValid(id) {
		return fmt.Errorf("invalid hospital id: %s", id)
	}
	const q = `DELETE FROM hospitals WHERE id=$1::uuid`
	_, err := s.pool.Exec(ctx, q, id)
	return err
}
