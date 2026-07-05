package handlers

import (
	"encoding/json"
	"net/http"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/auth"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type VerificationHandler struct {
	AuthStore *auth.Store
}

func NewVerificationHandler(authStore *auth.Store) *VerificationHandler {
	return &VerificationHandler{
		AuthStore: authStore,
	}
}

// HandleCheckVerification returns true if the driver is fully verified, false if unverified
// Mirrors the V1 "/check" endpoint
func (h *VerificationHandler) HandleCheckVerification(w http.ResponseWriter, r *http.Request) {
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	objID, _ := primitive.ObjectIDFromHex(uidStr)

	// First, check the active drivers collection
	driver, err := h.AuthStore.FindDriverByID(r.Context(), objID)
	if err != nil {
		response.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	if driver != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(true) // True = verified
		return
	}

	// Next, check the unverified drivers collection
	unverified, err := h.AuthStore.FindUnverifiedDriverByID(r.Context(), objID)
	if err != nil {
		response.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	if unverified != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(false) // False = unverified
		return
	}

	response.Error(w, "User not found", http.StatusNotFound)
}

// HandleUpdateVerification handles the document upload pipeline for drivers
func (h *VerificationHandler) HandleUpdateVerification(w http.ResponseWriter, r *http.Request) {
	uidStr, _ := r.Context().Value(middleware.UserIDKey).(string)
	objID, _ := primitive.ObjectIDFromHex(uidStr)

	var payload auth.UnverifiedDriver
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &payload) {
		return
	}

	// We must ensure we apply this to the current user
	payload.ID = objID

	err := h.AuthStore.UpdateUnverifiedDriver(r.Context(), &payload)
	if err != nil {
		response.Error(w, "Failed to update driver details", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Details updated successfully and recheck initialized"})
}
