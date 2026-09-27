package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"ambigo-backend/internal/ids"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uber/h3-go/v4"
)

// V2 H3 region pricing. Borders are seeded offline from checked-in GeoJSON
// (AP + Telangana states + districts); regions are H3 cell sets. Per-ride
// lookup is a local set-membership check — zero external calls, ever.
var (
	errNoRegion = errors.New("no region contains pickup")
	errNoPrice  = errors.New("region has no price for type")
)

const (
	RegionCellRes  = 7
	RegionMaxCells = 20000
)

type Region struct {
	ID          string     `json:"_id"`
	Name        string     `json:"name"`
	OsmType     string     `json:"osm_type,omitempty"`
	OsmID       int64      `json:"osm_id"`
	DisplayName string     `json:"display_name,omitempty"`
	Cells       []string   `json:"cells"`
	CellRes     int        `json:"cell_res"`
	FetchedAt   *time.Time `json:"fetched_at,omitempty"`
	Level       string     `json:"level,omitempty"`
	ParentID    *string    `json:"parent_id,omitempty"`
	Source      string     `json:"source,omitempty"`
}

const regionCols = `id::text, name, COALESCE(osm_type,''), osm_id, COALESCE(display_name,''), cells, cell_res, fetched_at, COALESCE(level,''), parent_id::text, COALESCE(source,'')`

func scanRegion(row pgx.Row) (*Region, error) {
	var r Region
	if err := row.Scan(&r.ID, &r.Name, &r.OsmType, &r.OsmID, &r.DisplayName, &r.Cells, &r.CellRes, &r.FetchedAt, &r.Level, &r.ParentID, &r.Source); err != nil {
		return nil, err
	}
	return &r, nil
}

type RegionPrice struct {
	RegionID         string       `json:"region_id"`
	AmbTypeID        string       `json:"amb_type_id"`
	BaseFare         float64      `json:"base_fare"`
	PricingTier      []PricingTier `json:"pricing_tier"`
	DriverShare      float64      `json:"driver_share"`
	ListingThreshold float64      `json:"listing_threshold"`
	HelperIncluded   bool         `json:"helper_included"`
	OTPRequired      bool         `json:"otp_required"`
}

type RegionStore struct {
	pool *pgxpool.Pool

	// regionCache holds all regions in memory: 61 rows, ~55k H3 strings.
	// Per-ride lookups (fares, OTP, scrub) must never scan the table.
	// TTL-bounded (stale ≤60s across instances); writes invalidate locally.
	cacheMu  sync.Mutex
	cacheAt  time.Time
	cacheAll []Region
}

const regionCacheTTL = 60 * time.Second

func NewRegionStore(pool *pgxpool.Pool) *RegionStore {
	return &RegionStore{pool: pool}
}

func (s *RegionStore) invalidateRegionCache() {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.cacheAt = time.Time{}
	s.cacheAll = nil
}

func (s *RegionStore) cachedRegions(ctx context.Context) ([]Region, error) {
	s.cacheMu.Lock()
	if time.Since(s.cacheAt) < regionCacheTTL && s.cacheAll != nil {
		out := s.cacheAll
		s.cacheMu.Unlock()
		return out, nil
	}
	s.cacheMu.Unlock()
	rows, err := s.pool.Query(ctx, `SELECT `+regionCols+` FROM pricing_regions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Region
	for rows.Next() {
		r, err := scanRegion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.cacheMu.Lock()
	s.cacheAll = out
	s.cacheAt = time.Now()
	s.cacheMu.Unlock()
	return out, nil
}

// FillCellsForGeometry converts a GeoJSON geometry (Polygon/MultiPolygon,
// coordinates as [lng,lat]) to H3 cells via PolygonToCells. Tries res 7
// (cities); falls back to res 6 for large states exceeding the cap.
func FillCellsForGeometry(geomJSON []byte) ([]string, error) {
	cells, _, err := FillCellsForGeometryRes(geomJSON)
	return cells, err
}

func FillCellsForGeometryRes(geomJSON []byte) ([]string, int, error) {
	var g struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal(geomJSON, &g); err != nil {
		return nil, 0, err
	}
	toLoop := func(ring [][]float64) h3.GeoLoop {
		loop := make(h3.GeoLoop, 0, len(ring))
		for _, pt := range ring {
			if len(pt) < 2 {
				continue
			}
			loop = append(loop, h3.NewLatLng(pt[1], pt[0]))
		}
		return loop
	}
	var polys []h3.GeoPolygon
	switch g.Type {
	case "Polygon":
		var rings [][][]float64
		if err := json.Unmarshal(g.Coordinates, &rings); err != nil {
			return nil, 0, err
		}
		if len(rings) == 0 {
			return nil, 0, errors.New("empty polygon")
		}
		p := h3.GeoPolygon{GeoLoop: toLoop(rings[0])}
		for _, h := range rings[1:] {
			p.Holes = append(p.Holes, toLoop(h))
		}
		polys = append(polys, p)
	case "MultiPolygon":
		var multi [][][][]float64
		if err := json.Unmarshal(g.Coordinates, &multi); err != nil {
			return nil, 0, err
		}
		for _, rings := range multi {
			if len(rings) == 0 {
				continue
			}
			p := h3.GeoPolygon{GeoLoop: toLoop(rings[0])}
			for _, h := range rings[1:] {
				p.Holes = append(p.Holes, toLoop(h))
			}
			polys = append(polys, p)
		}
	default:
		return nil, 0, fmt.Errorf("unsupported geometry %s", g.Type)
	}
	fill := func(res int) ([]string, error) {
		seen := make(map[string]bool)
		var out []string
		for _, p := range polys {
			cells, err := h3.PolygonToCells(p, res)
			if err != nil {
				return nil, err
			}
			for _, c := range cells {
				s := c.String()
				if !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
				if len(out) > RegionMaxCells {
					return nil, fmt.Errorf("too large")
				}
			}
		}
		if len(out) == 0 {
			return nil, errors.New("no cells filled")
		}
		return out, nil
	}
	if out, err := fill(RegionCellRes); err == nil {
		return out, RegionCellRes, nil
	}
	// Large state (e.g. Andhra Pradesh): fall back to res 6.
	out, err := fill(RegionCellRes - 1)
	if err != nil {
		return nil, 0, fmt.Errorf("region too large even at res %d, split into districts", RegionCellRes-1)
	}
	return out, RegionCellRes - 1, nil
}

// FillCellsForCircle fallback when OSM has no boundary: GridDisk cover filtered by haversine.
func FillCellsForCircle(lat, lng, radiusM float64) ([]string, error) {
	edgeM, err := h3.HexagonEdgeLengthAvgM(RegionCellRes)
	if err != nil || edgeM <= 0 {
		edgeM = 1000
	}
	k := int(math.Ceil(radiusM/edgeM)) + 1
	if k < 1 {
		k = 1
	}
	if k > 100 {
		return nil, fmt.Errorf("radius too large for res %d", RegionCellRes)
	}
	center, err := h3.LatLngToCell(h3.NewLatLng(lat, lng), RegionCellRes)
	if err != nil {
		return nil, err
	}
	disk, err := h3.GridDisk(center, k)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range disk {
		ll, err := h3.CellToLatLng(c)
		if err != nil {
			continue
		}
		dLat := (ll.Lat - lat) * math.Pi / 180.0
		dLng := (ll.Lng - lng) * math.Pi / 180.0
		a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat*math.Pi/180.0)*math.Cos(ll.Lat*math.Pi/180.0)*math.Sin(dLng/2)*math.Sin(dLng/2)
		if 6371000*2*math.Atan2(math.Sqrt(a), math.Sqrt(1-a)) <= radiusM {
			out = append(out, c.String())
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no cells in circle")
	}
	return out, nil
}

// ---- CRUD ----

func (s *RegionStore) CreateRegion(ctx context.Context, name string, osmID int64, polygonJSON []byte, cells []string) (*Region, error) {
	return s.CreateRegionRes(ctx, name, osmID, polygonJSON, cells, RegionCellRes)
}

func (s *RegionStore) CreateRegionRes(ctx context.Context, name string, osmID int64, polygonJSON []byte, cells []string, res int) (*Region, error) {
	return s.CreateRegionFull(ctx, name, "", osmID, "", polygonJSON, cells, res, "circle", nil, "circle")
}

func (s *RegionStore) CreateRegionFull(ctx context.Context, name, osmType string, osmID int64, display string, polygonJSON []byte, cells []string, res int, level string, parentID *string, source string) (*Region, error) {
	if res <= 0 {
		res = RegionCellRes
	}
	r := &Region{ID: ids.New(), Name: name, OsmType: osmType, OsmID: osmID, DisplayName: display, Cells: cells, CellRes: res, Level: level, ParentID: parentID, Source: source}
	now := time.Now()
	r.FetchedAt = &now
	polyArg := []byte(`{}`)
	if len(polygonJSON) > 0 {
		polyArg = polygonJSON
	}
	var parentArg interface{}
	if parentID != nil && *parentID != "" {
		parentArg = *parentID
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO pricing_regions (id, name, osm_type, osm_id, display_name, polygon, cells, cell_res, fetched_at, level, parent_id, source) VALUES ($1::uuid, $2, $3, $4, $5, $6::jsonb, $7::text[], $8, now(), $9, $10::uuid, $11)`,
		r.ID, r.Name, r.OsmType, r.OsmID, r.DisplayName, polyArg, r.Cells, r.CellRes, r.Level, parentArg, r.Source)
	if err != nil {
		return nil, err
	}
	s.invalidateRegionCache()
	return r, nil
}

// SearchRegions is a local text search over seeded regions — instant, no network.
func (s *RegionStore) SearchRegions(ctx context.Context, query string) ([]Region, error) {
	q := "%" + strings.TrimSpace(query) + "%"
	rows, err := s.pool.Query(ctx, `SELECT `+regionCols+` FROM pricing_regions WHERE name ILIKE $1 OR display_name ILIKE $1 ORDER BY array_length(cells,1) ASC LIMIT 20`, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Region
	for rows.Next() {
		r, err := scanRegion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	if out == nil {
		out = []Region{}
	}
	return out, rows.Err()
}

func (s *RegionStore) ListRegions(ctx context.Context) ([]Region, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+regionCols+` FROM pricing_regions ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Region
	for rows.Next() {
		r, err := scanRegion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	if out == nil {
		out = []Region{}
	}
	return out, rows.Err()
}

func (s *RegionStore) DeleteRegion(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM pricing_regions WHERE id=$1::uuid`, id)
	if err == nil {
		s.invalidateRegionCache()
	}
	return err
}

// FindRegionForPickup returns smallest matching region (smallest wins on overlap).
// Matches in Go with per-region resolution so states (res 6) and cities (res 7)
// can coexist. Region count is small (<100), so full scan is cheap and avoids
// GIN resolution mismatch.
// PolygonFor returns the stored border polygon for local recompute.
func (s *RegionStore) PolygonFor(ctx context.Context, id string) ([]byte, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT polygon FROM pricing_regions WHERE id=$1::uuid`, id).Scan(&raw)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *RegionStore) GetRegion(ctx context.Context, id string) (*Region, error) {
	r, err := scanRegion(s.pool.QueryRow(ctx, `SELECT `+regionCols+` FROM pricing_regions WHERE id=$1::uuid`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

// RefreshRegionCells re-pulls the stored OSM object and rewrites cells/polygon.
func (s *RegionStore) RefreshRegionCells(ctx context.Context, id string, cells []string, res int, polygonJSON []byte) (*Region, error) {
	if res <= 0 {
		res = RegionCellRes
	}
	polyArg := []byte(`{}`)
	if len(polygonJSON) > 0 {
		polyArg = polygonJSON
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE pricing_regions SET cells=$2::text[], cell_res=$3, polygon=$4::jsonb, fetched_at=now() WHERE id=$1::uuid`,
		id, cells, res, polyArg)
	if err != nil {
		return nil, err
	}
	s.invalidateRegionCache()
	return s.GetRegion(ctx, id)
}

func (s *RegionStore) FindRegionForPickup(ctx context.Context, lat, lng float64) (*Region, error) {
	all, err := s.cachedRegions(ctx)
	if err != nil {
		return nil, err
	}
	var best *Region
	for i := range all {
		r := &all[i]
		res := r.CellRes
		if res <= 0 {
			res = RegionCellRes
		}
		cell, err := h3.LatLngToCell(h3.NewLatLng(lat, lng), res)
		if err != nil {
			continue
		}
		want := cell.String()
		for _, c := range r.Cells {
			if c == want {
				if best == nil || len(r.Cells) < len(best.Cells) {
					cp := *r
					best = &cp
				}
				break
			}
		}
	}
	return best, nil
}

func (s *RegionStore) UpsertRegionPrice(ctx context.Context, p *RegionPrice) error {
	tiersJSON, err := json.Marshal(p.PricingTier)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO region_prices (region_id, amb_type_id, base_fare, pricing_tier, driver_share, listing_threshold, helper_included, otp_required, updated_at)
		 VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, $5, $6, $7, $8, now())
		 ON CONFLICT (region_id, amb_type_id) DO UPDATE SET base_fare=EXCLUDED.base_fare, pricing_tier=EXCLUDED.pricing_tier, driver_share=EXCLUDED.driver_share, listing_threshold=EXCLUDED.listing_threshold, helper_included=EXCLUDED.helper_included, otp_required=EXCLUDED.otp_required, updated_at=now()`,
		p.RegionID, p.AmbTypeID, p.BaseFare, tiersJSON, p.DriverShare, p.ListingThreshold, p.HelperIncluded, p.OTPRequired)
	if err == nil {
		s.invalidateRegionCache()
	}
	return err
}

func (s *RegionStore) GetRegionPrice(ctx context.Context, regionID, ambTypeID string) (*RegionPrice, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT region_id::text, amb_type_id::text, base_fare, pricing_tier, driver_share, listing_threshold, helper_included, otp_required FROM region_prices WHERE region_id=$1::uuid AND amb_type_id=$2::uuid`,
		regionID, ambTypeID)
	var p RegionPrice
	var tiersJSON []byte
	if err := row.Scan(&p.RegionID, &p.AmbTypeID, &p.BaseFare, &tiersJSON, &p.DriverShare, &p.ListingThreshold, &p.HelperIncluded, &p.OTPRequired); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if len(tiersJSON) > 0 {
		_ = json.Unmarshal(tiersJSON, &p.PricingTier)
	}
	return &p, nil
}

// ResolvedPrice is the effective fare config for a pickup.
type ResolvedPrice struct {
	RegionID         *string
	BaseFare         float64
	Tiers            []PricingTier
	DriverShare      float64
	ListingThreshold float64
	HelperIncluded   bool
	OTPRequired      bool
	IsOverride       bool
}

// ResolvePriceForPickup finds the smallest H3 region containing the pickup
// and returns its price override for the ambulance type. Fallback chain:
// district price -> parent state price -> supplied global config.
func (s *RegionStore) ResolvePriceForPickup(ctx context.Context, lat, lng float64, ambTypeID string, globalBase float64, globalTiers []PricingTier, globalShare, globalListing float64, globalHelper, globalOTP bool) (*ResolvedPrice, error) {
	base := &ResolvedPrice{
		BaseFare: globalBase, Tiers: globalTiers, DriverShare: globalShare,
		ListingThreshold: globalListing, HelperIncluded: globalHelper, OTPRequired: globalOTP,
	}
	region, err := s.FindRegionForPickup(ctx, lat, lng)
	if err != nil {
		return base, err
	}
	if region == nil {
		return base, errNoRegion
	}
	rid := region.ID
	override, err := s.GetRegionPrice(ctx, region.ID, ambTypeID)
	if err != nil {
		return base, err
	}
	if override == nil && region.ParentID != nil && *region.ParentID != "" {
		override, err = s.GetRegionPrice(ctx, *region.ParentID, ambTypeID)
		if err != nil {
			return base, err
		}
	}
	if override == nil {
		return base, errNoPrice
	}
	return &ResolvedPrice{
		RegionID: &rid, BaseFare: override.BaseFare, Tiers: override.PricingTier,
		DriverShare: override.DriverShare, ListingThreshold: override.ListingThreshold,
		HelperIncluded: override.HelperIncluded, OTPRequired: override.OTPRequired, IsOverride: true,
	}, nil
}
