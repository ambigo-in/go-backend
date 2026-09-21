package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/ids"
)

type DriverAttendantHandler struct {
	AuthStore *auth.Store
}

func NewDriverAttendantHandler(authStore *auth.Store) *DriverAttendantHandler {
	return &DriverAttendantHandler{AuthStore: authStore}
}

var driverAttendantMobileRegex = regexp.MustCompile(`^[6-9]\d{9}$`)

func (h *DriverAttendantHandler) HandleDriverCreateAttendant(w http.ResponseWriter, r *http.Request) {
	driverIDStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !ids.IsValid(driverIDStr) {
		response.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}
	driverID := driverIDStr
	var req struct {
		Name   string `json:"name"`
		Mobile string `json:"mobile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		response.Error(w, "Name required", http.StatusBadRequest)
		return
	}
	if !driverAttendantMobileRegex.MatchString(req.Mobile) {
		response.Error(w, "Invalid mobile", http.StatusBadRequest)
		return
	}
	// Enforce one active attendant per driver - deactivate old
	_ = h.AuthStore.DeactivateAttendantsForDriver(r.Context(), driverID)

	existing, _ := h.AuthStore.FindAmbulanceAttendantByMobile(r.Context(), req.Mobile)
	if existing != nil {
		existing.Name = req.Name
		existing.AssignedDriverID = &driverID
		existing.Active = true
		if err := h.AuthStore.UpdateAmbulanceAttendant(r.Context(), existing); err != nil {
			response.Error(w, "Failed to update paramedic", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"detail": "Paramedic updated", "id": existing.ID})
		return
	}
	att := &auth.AmbulanceAttendant{
		Name:             req.Name,
		Mobile:           req.Mobile,
		AssignedDriverID: &driverID,
	}
	if err := h.AuthStore.CreateAmbulanceAttendant(r.Context(), att); err != nil {
		response.Error(w, "Failed to create paramedic", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Paramedic added", "id": att.ID})
}

func (h *DriverAttendantHandler) HandleDriverListAttendants(w http.ResponseWriter, r *http.Request) {
	driverIDStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !ids.IsValid(driverIDStr) {
		response.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}
	driverID := driverIDStr
	list, err := h.AuthStore.ListAttendantsByDriver(r.Context(), driverID)
	if err != nil {
		response.Error(w, "Failed to fetch", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func (h *DriverAttendantHandler) HandleDriverDeleteAttendant(w http.ResponseWriter, r *http.Request) {
	driverIDStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !ids.IsValid(driverIDStr) {
		response.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}
	driverID := driverIDStr
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		response.Error(w, "ID required", http.StatusBadRequest)
		return
	}
	if !ids.IsValid(req.ID) {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	attID := req.ID
	if err := h.AuthStore.DeleteAttendantForDriver(r.Context(), driverID, attID); err != nil {
		response.Error(w, "Delete failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Paramedic removed"})
}
