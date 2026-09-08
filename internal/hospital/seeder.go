package hospital

import (
	"context"
	"time"

	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/places"
	"ambigo-backend/internal/translation"
)

// MaxCacheAge is how long Google Places data may be cached before it must be
// refreshed (Google Maps Platform ToS allows up to 30 days).
const MaxCacheAge = 30 * 24 * time.Hour

// ResidentSeedInterval is how often the background worker re-evaluates cities.
const ResidentSeedInterval = 24 * time.Hour

// DefaultMaxPerCategory is the default cap per category when city config is zero.
const DefaultMaxPerCategory = 40

// Text queries for split seeding — Emergency vs Non-Emergency.
// Google Places Text Search interprets these as natural language queries biased to the circle.
const emergencyTextQuery = "government hospital multi speciality hospital super speciality hospital"
const nonEmergencyTextQuery = "private hospital clinic nursing home general hospital"

// Seeder hydrates the hospitals collection from Google Places text-search,
// bucketing each result into H3 cells for ring lookups.
type Seeder struct {
	Places    *places.PlacesClient
	Hospitals *admin.HospitalStore
	Cities    *admin.HospitalCityStore
	Counters  *admin.CounterStore
}

func NewSeeder(pc *places.PlacesClient, hStore *admin.HospitalStore, cityStore *admin.HospitalCityStore, counterStore *admin.CounterStore) *Seeder {
	return &Seeder{Places: pc, Hospitals: hStore, Cities: cityStore, Counters: counterStore}
}

// SeedAll refreshes every enabled city that is due for a resync. It returns the
// total number of hospital documents changed (inserted or replaced).
// Background worker uses this (respects MaxCacheAge).
func (s *Seeder) SeedAll(ctx context.Context) (int, error) {
	return s.seedAllInternal(ctx, false)
}

// SeedAllForce refreshes every enabled city regardless of LastFetched age.
// Used by admin Sync Now (HandleSyncHospitals) to allow immediate re-seed after
// radius/cap changes.
func (s *Seeder) SeedAllForce(ctx context.Context) (int, error) {
	return s.seedAllInternal(ctx, true)
}

func (s *Seeder) seedAllInternal(ctx context.Context, force bool) (int, error) {
	if s.Places == nil || s.Places.APIKey == "" {
		return 0, nil
	}
	cities, err := s.Cities.ListEnabled(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, c := range cities {
		if !force {
			age := time.Since(c.LastFetched)
			if !c.LastFetched.IsZero() && age < MaxCacheAge {
				continue
			}
		}
		n, err := s.seedCity(ctx, c)
		if err != nil {
			logger.Log.Error().Err(err).Str("city", c.Name).Msg("Hospital seed failed")
			continue
		}
		total += n
		// A changed seed must trigger the app's cached-list refresh.
		if n > 0 {
			if err := s.Counters.IncrementCounter(ctx, "hospitals"); err != nil {
				logger.Log.Error().Err(err).Msg("Failed to bump hospitals counter after seed")
			}
		}
	}
	return total, nil
}

func clampCap(cap int) int {
	if cap == 0 {
		return DefaultMaxPerCategory
	}
	if cap < 5 {
		return 5
	}
	if cap > 60 {
		return 60
	}
	return cap
}

// fetchPaginated fetches up to cap places for query using Text Search pagination.
func (s *Seeder) fetchPaginated(ctx context.Context, query string, city admin.HospitalCity, cap int) ([]places.Place, error) {
	var all []places.Place
	var token string
	for len(all) < cap {
		remaining := cap - len(all)
		pageSize := 20
		if remaining < pageSize {
			pageSize = remaining
		}
		page, next, err := s.Places.SearchText(ctx, query, city.Lat, city.Lng, city.RadiusM, pageSize, token)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		all = append(all, page...)
		if next == "" || len(all) >= cap {
			break
		}
		token = next
	}
	if len(all) > cap {
		all = all[:cap]
	}
	return all, nil
}

func (s *Seeder) seedCity(ctx context.Context, city admin.HospitalCity) (int, error) {
	cap := clampCap(city.MaxPerCategory)

	emergencyPlaces, err := s.fetchPaginated(ctx, emergencyTextQuery, city, cap)
	if err != nil {
		return 0, err
	}
	nonEmergencyPlaces, err := s.fetchPaginated(ctx, nonEmergencyTextQuery, city, cap)
	if err != nil {
		return 0, err
	}

	// Deduplicate by place_id across both categories.
	seen := make(map[string]bool)
	changed := 0

	// Helper to upsert a batch with category override.
	process := func(list []places.Place, isEmergency bool) error {
		for _, p := range list {
			if seen[p.ID] {
				continue
			}
			seen[p.ID] = true

			existing, err := s.Hospitals.FindByPlaceID(ctx, p.ID)
			if err != nil {
				return err
			}

			var hType, hCat string
			if isEmergency {
				// Emergency bucket: always emergency category; type heuristic within bucket.
				hCat = admin.HospitalCategoryEmergency
				// Prefer government vs multi based on name, fallback to multi.
				hType = admin.ClassifyHospitalType(p.DisplayName, p.Types)
				if hType != admin.HospitalTypeGovernment && hType != admin.HospitalTypeMultiSpeciality {
					hType = admin.HospitalTypeMultiSpeciality
				}
			} else {
				hCat = admin.HospitalCategoryNonEmergency
				hType = admin.ClassifyHospitalType(p.DisplayName, p.Types)
				if hType == admin.HospitalTypeGovernment || hType == admin.HospitalTypeMultiSpeciality {
					// Non-emergency bucket should not be gov/multi; downgrade to private/clinic.
					if admin.IsClinicName(p.DisplayName) {
						hType = admin.HospitalTypeClinic
					} else {
						hType = admin.HospitalTypePrivate
					}
				}
				if hType == admin.HospitalTypeGeneral {
					hType = admin.HospitalTypePrivate
				}
			}

			if existing != nil {
				if existing.TypeLocked {
					hType = existing.HospitalType
					hCat = existing.Category
				}
				if existing.Name == nil {
					existing.Name = translation.Map{"en_US": p.DisplayName}
				}
				if existing.Address == nil {
					existing.Address = translation.Map{"en_US": p.FormattedAddr}
				}
				if len(existing.Location.Coordinates) == 0 {
					existing.Location = admin.GeoJSON{Type: "Point", Coordinates: []float64{p.Lng, p.Lat}}
				}
				if len(existing.H3Cells) == 0 {
					existing.H3Cells = admin.BuildH3Cells(p.Lng, p.Lat)
				}
				if existing.Services == nil {
					existing.Services = []string{}
				}
				existing.GoogleTypes = p.Types
				existing.HospitalType = hType
				existing.Category = hCat
				existing.FetchedAt = time.Now()

				c, err := s.Hospitals.UpsertByPlaceID(ctx, existing)
				if err != nil {
					return err
				}
				if c {
					changed++
				}
				continue
			}

			doc := &admin.Hospital{
				Name:         translation.Map{"en_US": p.DisplayName},
				Address:      translation.Map{"en_US": p.FormattedAddr},
				City:         translation.Map{"en_US": city.Name},
				Location:     admin.GeoJSON{Type: "Point", Coordinates: []float64{p.Lng, p.Lat}},
				Timing:       &admin.Timing{Start: "12:00 AM", End: "11:59 PM"},
				AlwaysOpen:   true,
				Services:     []string{},
				PlaceID:      p.ID,
				Source:       admin.HospitalSourceGoogle,
				FetchedAt:    time.Now(),
				H3Cells:      admin.BuildH3Cells(p.Lng, p.Lat),
				GoogleTypes:  p.Types,
				HospitalType: hType,
				Category:     hCat,
			}
			c, err := s.Hospitals.UpsertByPlaceID(ctx, doc)
			if err != nil {
				return err
			}
			if c {
				changed++
			}
		}
		return nil
	}

	if err := process(emergencyPlaces, true); err != nil {
		return changed, err
	}
	if err := process(nonEmergencyPlaces, false); err != nil {
		return changed, err
	}

	if err := s.Cities.MarkFetched(ctx, city.ID); err != nil {
		return changed, err
	}
	return changed, nil
}
