package handlers

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/hospital"
	"ambigo-backend/internal/location"
	"ambigo-backend/internal/telephony"
)

type SharedHandler struct {
	Cloudshope    *telephony.CloudshopeService
	CounterStore  *admin.CounterStore
	AdminStore    *admin.Store
	HospitalStore *admin.HospitalStore
	Seeder        *hospital.Seeder
}

func NewSharedHandler(cs *telephony.CloudshopeService, cStore *admin.CounterStore, aStore *admin.Store, hStore *admin.HospitalStore, seeder *hospital.Seeder) *SharedHandler {
	return &SharedHandler{
		Cloudshope:    cs,
		CounterStore:  cStore,
		AdminStore:    aStore,
		HospitalStore: hStore,
		Seeder:        seeder,
	}
}

func (h *SharedHandler) HandleCallMask(w http.ResponseWriter, r *http.Request) {
	_, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		FromNumber string `json:"from_number" validate:"required"`
		ToNumber   string `json:"to_number" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	maskedNumber, err := h.Cloudshope.InitiateCallMasking(req.FromNumber, req.ToNumber)
	if err != nil {
		response.Error(w, "Error placing the call, please try again!", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"detail": maskedNumber,
	})
}

// HandleCheckAmbulanceUpdates checks the ambulance_type counter
func (h *SharedHandler) HandleCheckAmbulanceUpdates(w http.ResponseWriter, r *http.Request) {
	count, err := h.CounterStore.GetCounter(r.Context(), "ambulance_type")
	if err != nil {
		response.Error(w, "Error fetching counter", http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(fmt.Sprintf("%d", count))
}

// HandleListAmbulanceTypes lists all active ambulance types
func (h *SharedHandler) HandleListAmbulanceTypes(w http.ResponseWriter, r *http.Request) {
	list, err := h.AdminStore.ListAmbulanceTypes(r.Context())
	if err != nil {
		response.Error(w, "Failed to fetch list", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// HandleCheckHospitalUpdates checks the hospitals counter
func (h *SharedHandler) HandleCheckHospitalUpdates(w http.ResponseWriter, r *http.Request) {
	count, err := h.CounterStore.GetCounter(r.Context(), "hospitals")
	if err != nil {
		response.Error(w, "Error fetching counter", http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(fmt.Sprintf("%d", count))
}

// HandleListHospitals returns hospitals. When the request body includes
// lat/lng, it returns hospitals within the H3 ring around that point sorted by
// distance; otherwise it returns the full list capped at 50 (keyset paginated).
func (h *SharedHandler) HandleListHospitals(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Lat      *float64 `json:"lat"`
		Lng      *float64 `json:"lng"`
		RadiusKm *float64 `json:"radius_km"`
		Limit    *int     `json:"limit"`
		Cursor   string   `json:"cursor"`
		AfterID  string   `json:"after_id"`
		Skip     *int     `json:"skip"`
		Offset   *int     `json:"offset"`
	}
	// Body is optional; ignore decode errors so an empty body works.
	_ = json.NewDecoder(r.Body).Decode(&req)

	limit := 50
	cursor := req.Cursor
	offset := 0
	if cursor == "" {
		cursor = req.AfterID
	}
	if req.Limit != nil && *req.Limit > 0 && *req.Limit <= 50 {
		limit = *req.Limit
	}
	if req.Skip != nil && *req.Skip >= 0 {
		offset = *req.Skip
	} else if req.Offset != nil && *req.Offset >= 0 {
		offset = *req.Offset
	}
	// Also allow query params for pagination.
	if qv := r.URL.Query().Get("limit"); qv != "" {
		if n, err2 := strconv.Atoi(qv); err2 == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	if qv := r.URL.Query().Get("skip"); qv != "" {
		if n, err2 := strconv.Atoi(qv); err2 == nil && n >= 0 {
			offset = n
		}
	} else if qv := r.URL.Query().Get("offset"); qv != "" {
		if n, err2 := strconv.Atoi(qv); err2 == nil && n >= 0 {
			offset = n
		}
	}
	if qv := r.URL.Query().Get("cursor"); qv != "" {
		cursor = qv
	} else if qv := r.URL.Query().Get("after_id"); qv != "" {
		cursor = qv
	}

	var list []admin.Hospital
	var err error

	if req.Lat != nil && req.Lng != nil {
		cell := location.GetH3CellAtResolution(*req.Lat, *req.Lng, admin.HospitalH3Resolution)
		cells, cerr := location.GetNeighborCellsAtRing(cell, admin.HospitalH3Ring)
		if cerr != nil {
			response.Error(w, "Failed to resolve location", http.StatusBadRequest)
			return
		}
		list, err = h.HospitalStore.FindByCells(r.Context(), cells)
	} else {
		if offset > 0 && cursor == "" {
			list, err = h.HospitalStore.ListHospitalsWithOffset(r.Context(), limit, offset)
		} else if cursor != "" || req.Limit != nil || offset > 0 {
			list, err = h.HospitalStore.ListHospitalsPaginated(r.Context(), limit, cursor)
		} else {
			list, err = h.HospitalStore.ListHospitals(r.Context())
		}
	}
	if err != nil {
		response.Error(w, "Failed to fetch list", http.StatusInternalServerError)
		return
	}

	if req.Lat != nil && req.Lng != nil {
		radiusKm := 30.0
		if req.RadiusKm != nil {
			radiusKm = *req.RadiusKm
		}
		nearby := make([]admin.Hospital, 0, len(list))
		for _, ho := range list {
			// Hospital.Location.Coordinates is [lng, lat].
			if len(ho.Location.Coordinates) < 2 {
				continue
			}
			d := haversineKm(*req.Lat, *req.Lng, ho.Location.Coordinates[1], ho.Location.Coordinates[0])
			if d <= radiusKm {
				ho.DistanceKm = d
				nearby = append(nearby, ho)
			}
		}
		sort.Slice(nearby, func(i, j int) bool { return nearby[i].DistanceKm < nearby[j].DistanceKm })
		list = nearby
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const R = 6371.0
	dLat := (lat2 - lat1) * (math.Pi / 180.0)
	dLng := (lng2 - lng1) * (math.Pi / 180.0)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*(math.Pi/180.0))*math.Cos(lat2*(math.Pi/180.0))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// HandleSyncHospitals forces a Google re-seed of all configured cities (admin
// triggered). Bypasses MaxCacheAge to allow immediate re-seed after radius/cap changes.
func (h *SharedHandler) HandleSyncHospitals(w http.ResponseWriter, r *http.Request) {
	if h.Seeder == nil {
		response.Error(w, "Hospital seeding not configured", http.StatusServiceUnavailable)
		return
	}
	n, err := h.Seeder.SeedAllForce(r.Context())
	if err != nil {
		response.Error(w, "Hospital sync failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"detail": "Hospital sync completed", "changed": n})
}
