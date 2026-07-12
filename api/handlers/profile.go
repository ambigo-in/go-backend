package handlers

import (
	"encoding/json"
	"net/http"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/auth"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type ProfileHandler struct {
	AuthStore *auth.Store
}

func NewProfileHandler(authStore *auth.Store) *ProfileHandler {
	return &ProfileHandler{
		AuthStore: authStore,
	}
}

// -----------------------------------------------------
// USER PROFILE ENDPOINTS
// -----------------------------------------------------

// HandleGetUserProfile returns the authenticated user's details
func (h *ProfileHandler) HandleGetUserProfile(w http.ResponseWriter, r *http.Request) {
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	objID, err := primitive.ObjectIDFromHex(uidStr)
	if err != nil {
		response.Error(w, "Invalid User ID format", http.StatusBadRequest)
		return
	}

	user, err := h.AuthStore.FindUserByID(r.Context(), objID)
	if err != nil {
		response.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	if user == nil {
		response.Error(w, "User not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// HandleUpdateUserFCM updates the user's FCM token
func (h *ProfileHandler) HandleUpdateUserFCM(w http.ResponseWriter, r *http.Request) {
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

	var payload struct {
		FCMToken string `json:"fcm_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	err = h.AuthStore.UpdateUserFCM(r.Context(), objID, payload.FCMToken)
	if err != nil {
		response.Error(w, "Failed to update FCM token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "FCM Token updated successfully"})
}

// -----------------------------------------------------
// DRIVER PROFILE ENDPOINTS
// -----------------------------------------------------

// HandleGetDriverProfile returns the verified driver's details
func (h *ProfileHandler) HandleGetDriverProfile(w http.ResponseWriter, r *http.Request) {
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	role, ok := r.Context().Value(middleware.UserRoleKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	objID, err := primitive.ObjectIDFromHex(uidStr)
	if err != nil {
		response.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	if role == "unvrf_driver" {
		driver, err := h.AuthStore.FindUnverifiedDriverByID(r.Context(), objID)
		if err != nil {
			response.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
		if driver == nil {
			response.Error(w, "User not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(driver)
		return
	}

	driver, err := h.AuthStore.FindDriverByID(r.Context(), objID)
	if err != nil {
		response.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	if driver == nil {
		response.Error(w, "Driver not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(driver)
}

// HandleUpdateDriverFCM updates the driver's FCM token
func (h *ProfileHandler) HandleUpdateDriverFCM(w http.ResponseWriter, r *http.Request) {
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	role, ok := r.Context().Value(middleware.UserRoleKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	objID, err := primitive.ObjectIDFromHex(uidStr)
	if err != nil {
		response.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	var payload struct {
		FCMToken string `json:"fcm_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	if role == "unvrf_driver" {
		err = h.AuthStore.UpdateUnverifiedDriverFCM(r.Context(), objID, payload.FCMToken)
	} else {
		err = h.AuthStore.UpdateDriverFCM(r.Context(), objID, payload.FCMToken)
	}
	if err != nil {
		response.Error(w, "Failed to update FCM token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "FCM Token updated successfully"})
}
