package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/ids"
	"ambigo-backend/internal/requestid"
)

var mdAmbulanceMobileRegex = regexp.MustCompile(`^[6-9]\d{9}$`)

// MDAmbulanceHandler serves the MD "Add Ambulance" module: MD creates
// ambulances, adds driver mobiles under each, and toggles active per link.
// Mobile is the only join to drivers; public drivers (zero links) bypass checks.
type MDAmbulanceHandler struct {
	AdminStore *admin.Store
	AuthStore  *auth.Store
	EventBus   *eventbus.InMemoryBus
}

func NewMDAmbulanceHandler(adminStore *admin.Store, authStore *auth.Store, eventBus *eventbus.InMemoryBus) *MDAmbulanceHandler {
	return &MDAmbulanceHandler{AdminStore: adminStore, AuthStore: authStore, EventBus: eventBus}
}

func mdHospitalID(r *http.Request) (mdID, hospitalID string, ok bool) {
	mdID, _ = r.Context().Value(middleware.UserIDKey).(string)
	hospitalID, _ = r.Context().Value(middleware.HospitalIDKey).(string)
	if !ids.IsValid(mdID) || !ids.IsValid(hospitalID) {
		return "", "", false
	}
	return mdID, hospitalID, true
}

// HandleCreateAmbulance creates one ambulance label under the MD's hospital.
func (h *MDAmbulanceHandler) HandleCreateAmbulance(w http.ResponseWriter, r *http.Request) {
	mdID, hospitalID, ok := mdHospitalID(r)
	if !ok {
		response.Error(w, "Hospital not linked to account", http.StatusBadRequest)
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	label := strings.TrimSpace(req.Label)
	if label == "" {
		response.Error(w, "Label required (e.g. Ambulance-1)", http.StatusBadRequest)
		return
	}
	if len(label) > 80 {
		response.Error(w, "Label too long", http.StatusBadRequest)
		return
	}
	a, err := h.AdminStore.CreateMDAmbulance(r.Context(), hospitalID, mdID, label)
	if err != nil {
		response.Error(w, "Failed to create ambulance", http.StatusInternalServerError)
		return
	}
	h.EventBus.PublishEvent(eventbus.ChannelAdminHospitalAdded, eventbus.AdminHospitalPayload{
		HospitalID: hospitalID, Name: "md_ambulance_created:" + a.ID, RequestID: requestid.FromContext(r.Context()),
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(a)
}

// HandleListAmbulances lists this hospital's ambulances with their numbers.
func (h *MDAmbulanceHandler) HandleListAmbulances(w http.ResponseWriter, r *http.Request) {
	_, hospitalID, ok := mdHospitalID(r)
	if !ok {
		response.Error(w, "Hospital not linked to account", http.StatusBadRequest)
		return
	}
	list, err := h.AdminStore.ListMDAmbulances(r.Context(), hospitalID)
	if err != nil {
		response.Error(w, "Failed to fetch", http.StatusInternalServerError)
		return
	}
	type row struct {
		admin.MDAmbulance
		Drivers []admin.MDAmbulanceDriver `json:"drivers"`
	}
	out := make([]row, 0, len(list))
	for _, a := range list {
		drivers, _ := h.AdminStore.ListMDAmbulanceDrivers(r.Context(), a.ID)
		if drivers == nil {
			drivers = []admin.MDAmbulanceDriver{}
		}
		out = append(out, row{MDAmbulance: a, Drivers: drivers})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// HandleAddNumber links a driver mobile under an ambulance as inactive.
// MD must explicitly activate it afterwards.
func (h *MDAmbulanceHandler) HandleAddNumber(w http.ResponseWriter, r *http.Request) {
	_, hospitalID, ok := mdHospitalID(r)
	if !ok {
		response.Error(w, "Hospital not linked to account", http.StatusBadRequest)
		return
	}
	var req struct {
		AmbulanceID string `json:"ambulance_id"`
		Mobile      string `json:"mobile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !ids.IsValid(req.AmbulanceID) || !mdAmbulanceMobileRegex.MatchString(strings.TrimSpace(req.Mobile)) {
		response.Error(w, "Valid ambulance_id and 10-digit mobile required", http.StatusBadRequest)
		return
	}
	mobile := strings.TrimSpace(req.Mobile)
	amb, err := h.AdminStore.GetMDAmbulance(r.Context(), req.AmbulanceID, hospitalID)
	if err != nil || amb == nil {
		response.Error(w, "Ambulance not found in your hospital", http.StatusNotFound)
		return
	}
	d, err := h.AdminStore.AddMDAmbulanceDriver(r.Context(), req.AmbulanceID, mobile)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			response.Error(w, "Number already added under this ambulance", http.StatusConflict)
			return
		}
		response.Error(w, "Failed to add number", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d)
}

// HandleSetNumberActive toggles one link. Activation deactivates losers on
// both sides (one-active-per-number AND one-active-per-ambulance) in one TX.
func (h *MDAmbulanceHandler) HandleSetNumberActive(w http.ResponseWriter, r *http.Request) {
	_, hospitalID, ok := mdHospitalID(r)
	if !ok {
		response.Error(w, "Hospital not linked to account", http.StatusBadRequest)
		return
	}
	var req struct {
		AmbulanceID string `json:"ambulance_id"`
		Mobile      string `json:"mobile"`
		Active      bool   `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !ids.IsValid(req.AmbulanceID) || !mdAmbulanceMobileRegex.MatchString(strings.TrimSpace(req.Mobile)) {
		response.Error(w, "Valid ambulance_id and 10-digit mobile required", http.StatusBadRequest)
		return
	}
	mobile := strings.TrimSpace(req.Mobile)
	amb, err := h.AdminStore.GetMDAmbulance(r.Context(), req.AmbulanceID, hospitalID)
	if err != nil || amb == nil {
		response.Error(w, "Ambulance not found in your hospital", http.StatusNotFound)
		return
	}
	if err := h.AdminStore.SetMDAmbulanceDriverActive(r.Context(), req.AmbulanceID, mobile, req.Active); err != nil {
		if strings.Contains(err.Error(), "not found") {
			response.Error(w, "Number link not found under this ambulance", http.StatusNotFound)
			return
		}
		response.Error(w, "Failed to update: "+err.Error(), http.StatusConflict)
		return
	}
	// Keep verified driver row in sync when its active link changes.
	if req.Active {
		if drv, _ := h.AuthStore.FindDriverByMobile(r.Context(), mobile); drv != nil {
			_ = h.AuthStore.SetDriverMDAmbulanceID(r.Context(), drv.ID, req.AmbulanceID)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Updated"})
}
