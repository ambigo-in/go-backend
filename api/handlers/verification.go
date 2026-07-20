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

	objID, err := primitive.ObjectIDFromHex(uidStr)
	if err != nil {
		response.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

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

	// V15: Don't leak user existence — return same 404 regardless
	response.Error(w, "Not found", http.StatusNotFound)
}

// HandleUpdateVerification handles the document upload pipeline for drivers
func (h *VerificationHandler) HandleUpdateVerification(w http.ResponseWriter, r *http.Request) {
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	objID, err := primitive.ObjectIDFromHex(uidStr)
	if err != nil {
		response.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	req, ok := response.DecodeJSONBody[auth.VerificationUpdateRequest](w, r, 0)
	if !ok {
		return
	}

	driver, err := h.AuthStore.FindUnverifiedDriverByID(r.Context(), objID)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if driver == nil {
		response.Error(w, "Driver not found", http.StatusNotFound)
		return
	}

	driver.PortraitImage = req.PortraitImage
	driver.POIImage = req.POIImage
	driver.DLImage = req.DLImage
	driver.RCImage = req.RCImage
	driver.AmbFront = req.AmbFront
	driver.AmbInside = req.AmbInside
	driver.UnderProgress = true
	driver.ErrorMessage = nil

	err = h.AuthStore.UpdateUnverifiedDriver(r.Context(), driver)
	if err != nil {
		response.Error(w, "Failed to update driver details", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Details updated successfully and recheck initialized"})
}
