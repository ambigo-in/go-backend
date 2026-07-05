package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/telephony"
)

type SharedHandler struct {
	Cloudshope    *telephony.CloudshopeService
	CounterStore  *admin.CounterStore
	AdminStore    *admin.Store
	HospitalStore *admin.HospitalStore
}

func NewSharedHandler(cs *telephony.CloudshopeService, cStore *admin.CounterStore, aStore *admin.Store, hStore *admin.HospitalStore) *SharedHandler {
	return &SharedHandler{
		Cloudshope:    cs,
		CounterStore:  cStore,
		AdminStore:    aStore,
		HospitalStore: hStore,
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

// HandleListHospitals returns the static hospital list
func (h *SharedHandler) HandleListHospitals(w http.ResponseWriter, r *http.Request) {
	list, err := h.HospitalStore.ListHospitals(r.Context())
	if err != nil {
		response.Error(w, "Failed to fetch list", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

