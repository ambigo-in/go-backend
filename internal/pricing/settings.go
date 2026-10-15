package pricing

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Global surcharge settings (single row, id=1): percent-extra values applied
// on top of base+distance fare. Defaults mirror the previously hardcoded
// engine multipliers (SOS +50%, night +20%).
const (
	DefaultEmergencyPct = 50.0
	DefaultNightPct     = 20.0
)

type SurchargeSettings struct {
	EmergencyPct float64   `json:"emergency_pct" validate:"gte=0,lte=500"`
	NightPct     float64   `json:"night_pct" validate:"gte=0,lte=500"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

// Multipliers converts percent-extra values to engine multipliers
// (50 -> 1.5x). Falls back to defaults on nonsense input.
func (s *SurchargeSettings) Multipliers() (emgMult, nightMult float64) {
	emgMult, nightMult = 1+DefaultEmergencyPct/100.0, 1+DefaultNightPct/100.0
	if s == nil {
		return emgMult, nightMult
	}
	if s.EmergencyPct >= 0 && s.EmergencyPct <= 500 {
		emgMult = 1 + s.EmergencyPct/100.0
	}
	if s.NightPct >= 0 && s.NightPct <= 500 {
		nightMult = 1 + s.NightPct/100.0
	}
	return emgMult, nightMult
}

type SettingsStore struct {
	pool *pgxpool.Pool

	// Hot fare path (every booking + estimate) must not hit the DB.
	// TTL-bounded; writes invalidate locally.
	cacheMu  sync.Mutex
	cacheAt  time.Time
	cached   *SurchargeSettings
}

const settingsCacheTTL = 60 * time.Second

func NewSettingsStore(pool *pgxpool.Pool) *SettingsStore {
	return &SettingsStore{pool: pool}
}

// Get returns the global surcharge settings, falling back to built-in
// defaults when the table is missing/empty or the DB errors — fare
// computation must never block on this lookup.
func (s *SettingsStore) Get(ctx context.Context) *SurchargeSettings {
	def := &SurchargeSettings{EmergencyPct: DefaultEmergencyPct, NightPct: DefaultNightPct}
	if s == nil || s.pool == nil {
		return def
	}
	s.cacheMu.Lock()
	if time.Since(s.cacheAt) < settingsCacheTTL && s.cached != nil {
		out := *s.cached
		s.cacheMu.Unlock()
		return &out
	}
	s.cacheMu.Unlock()

	var emg, night float64
	var updated time.Time
	err := s.pool.QueryRow(ctx, `SELECT emergency_pct, night_pct, updated_at FROM pricing_settings WHERE id=1`).Scan(&emg, &night, &updated)
	if err != nil {
		return def
	}
	got := &SurchargeSettings{EmergencyPct: emg, NightPct: night, UpdatedAt: updated}
	s.cacheMu.Lock()
	s.cached = got
	s.cacheAt = time.Now()
	s.cacheMu.Unlock()
	out := *got
	return &out
}

// Upsert saves the global surcharge settings and invalidates the cache.
func (s *SettingsStore) Upsert(ctx context.Context, emgPct, nightPct float64) (*SurchargeSettings, error) {
	var out SurchargeSettings
	err := s.pool.QueryRow(ctx, `
		INSERT INTO pricing_settings (id, emergency_pct, night_pct, updated_at)
		VALUES (1, $1, $2, now())
		ON CONFLICT (id) DO UPDATE SET emergency_pct=EXCLUDED.emergency_pct, night_pct=EXCLUDED.night_pct, updated_at=now()
		RETURNING emergency_pct, night_pct, updated_at`,
		emgPct, nightPct).Scan(&out.EmergencyPct, &out.NightPct, &out.UpdatedAt)
	if err != nil {
		return nil, err
	}
	s.cacheMu.Lock()
	s.cached = &out
	s.cacheAt = time.Now()
	s.cacheMu.Unlock()
	return &out, nil
}
