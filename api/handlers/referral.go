package handlers

import (
	"encoding/json"
	"net/http"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/referral"
)

// ReferralHandler handles HTTP requests for referral configuration and rewards.
type ReferralHandler struct {
	Store   *referral.Store
	Service *referral.Service
}

// NewReferralHandler creates a new ReferralHandler.
func NewReferralHandler(store *referral.Store, service *referral.Service) *ReferralHandler {
	return &ReferralHandler{Store: store, Service: service}
}

// HandleGetConfig returns all referral type configurations (admin endpoint).
func (h *ReferralHandler) HandleGetConfig(w http.ResponseWriter, r *http.Request) {
	configs, err := h.Store.ListConfigs(r.Context())
	if err != nil {
		response.Error(w, "Failed to fetch referral configs", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(configs)
}

// HandleSaveConfig saves all referral type configurations (admin endpoint).
func (h *ReferralHandler) HandleSaveConfig(w http.ResponseWriter, r *http.Request) {
	var configs []referral.Config
	if err := json.NewDecoder(r.Body).Decode(&configs); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	// Validate types
	for _, cfg := range configs {
		if !referral.ValidTypes[cfg.Type] {
			response.Error(w, "Invalid referral type: "+cfg.Type, http.StatusBadRequest)
			return
		}
	}

	if err := h.Store.SaveConfigs(r.Context(), configs); err != nil {
		response.Error(w, "Failed to save referral configs", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Referral configs saved"})
}

// HandleGetRewards returns the rewards summary for the authenticated user or driver.
func (h *ReferralHandler) HandleGetRewards(w http.ResponseWriter, r *http.Request) {
	entityID, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	role, ok := r.Context().Value(middleware.UserRoleKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Normalize role for referral purposes
	if role == "unvrf_driver" {
		role = "driver"
	}

	rewards, err := h.Service.GetRewards(r.Context(), entityID, role)
	if err != nil {
		response.Error(w, "Failed to fetch rewards: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rewards)
}
