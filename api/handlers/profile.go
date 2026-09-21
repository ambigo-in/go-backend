package handlers

import (
	"encoding/json"
	"net/http"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/ids"
)

type ProfileHandler struct {
	AuthStore  *auth.Store
	AdminStore *admin.Store
}

func NewProfileHandler(authStore *auth.Store) *ProfileHandler {
	return &ProfileHandler{
		AuthStore: authStore,
	}
}

// SetAdminStore wires ambulance-type lookup for amb_type_name enrichment.
func (h *ProfileHandler) SetAdminStore(s *admin.Store) {
	h.AdminStore = s
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

	if !ids.IsValid(uidStr) {
		response.Error(w, "Invalid User ID format", http.StatusBadRequest)
		return
	}

	user, err := h.AuthStore.FindUserByID(r.Context(), uidStr)
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
	if !ids.IsValid(uidStr) {
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

	err := h.AuthStore.UpdateUserFCM(r.Context(), uidStr, payload.FCMToken)
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

	if !ids.IsValid(uidStr) {
		response.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	if role == "unvrf_driver" {
		driver, err := h.AuthStore.FindUnverifiedDriverByID(r.Context(), uidStr)
		if err != nil {
			response.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
		if driver != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(driver)
			return
		}

		// Driver was approved but token not refreshed — check verified drivers
		verifiedDriver, err := h.AuthStore.FindDriverByID(r.Context(), uidStr)
		if err != nil {
			response.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
		if verifiedDriver != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(enrichDriver(verifiedDriver, h, r))
			return
		}

		response.Error(w, "User not found", http.StatusNotFound)
		return
	}

	driver, err := h.AuthStore.FindDriverByID(r.Context(), uidStr)
	if err != nil {
		response.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	if driver == nil {
		response.Error(w, "Driver not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(enrichDriver(driver, h, r))
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

	if !ids.IsValid(uidStr) {
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
		if err := h.AuthStore.UpdateUnverifiedDriverFCM(r.Context(), uidStr, payload.FCMToken); err != nil {
			response.Error(w, "Failed to update FCM token", http.StatusInternalServerError)
			return
		}
	} else {
		if err := h.AuthStore.UpdateDriverFCM(r.Context(), uidStr, payload.FCMToken); err != nil {
			response.Error(w, "Failed to update FCM token", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "FCM Token updated successfully"})
}

// enrichDriver returns the driver plus amb_type_name resolved from
// vehicle_type (ambulance-type ID). Old apps ignore the extra key.
func enrichDriver(driver *auth.Driver, h *ProfileHandler, r *http.Request) map[string]interface{} {
	out := map[string]interface{}{
		"_id":                  driver.ID,
		"name":                 driver.Name,
		"mobile":               driver.Mobile,
		"photo":                driver.Photo,
		"vehicle_type":         driver.VehicleType,
		"vehicle_registration": driver.VehicleReg,
		"wallet_details":       driver.WalletDetails,
		"wallet_balance":       driver.WalletBalance,
		"wallet_verified":      driver.WalletVerified,
		"referral_code":        driver.ReferralCode,
	}
	if driver.MyReferralCode != "" {
		out["my_referral_code"] = driver.MyReferralCode
	}
	if driver.WalletVerifiedAt != nil {
		out["wallet_verified_at"] = driver.WalletVerifiedAt
	}
	if driver.Location != nil {
		out["location"] = driver.Location
	}
	if driver.Details != nil {
		out["details"] = driver.Details
	}
	if driver.LastLocationUpdate != nil {
		out["last_location_update"] = driver.LastLocationUpdate
	}
	if driver.MDAmbulanceID != nil {
		out["md_ambulance_id"] = driver.MDAmbulanceID
	} else if h != nil && h.AuthStore != nil {
		// MDAmbulanceID is resolved live (not stored on the scanned struct):
		// fetch it so hospital-linked drivers see their effective ambulance.
		if mdID, merr := h.AuthStore.GetDriverMDAmbulanceID(r.Context(), driver.ID); merr == nil && mdID != nil {
			out["md_ambulance_id"] = mdID
		}
	}
	if h != nil && h.AdminStore != nil && driver.VehicleType != "" {
		if amb, err := h.AdminStore.GetAmbulanceTypeByID(r.Context(), driver.VehicleType); err == nil && amb != nil {
			out["amb_type_name"] = amb.Name
		}
	}
	return out
}
