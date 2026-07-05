package handlers

import (
	"encoding/json"
	"net/http"

	"ambigo-backend/api/response"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/requestid"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type AuthHandler struct {
	AuthStore *auth.Store
	EventBus  *eventbus.InMemoryBus
	JWTSecret string
	SMSCfg    auth.SMSCountryConfig
}

func NewAuthHandler(authStore *auth.Store, eventBus *eventbus.InMemoryBus, jwtSecret string, smsCfg auth.SMSCountryConfig) *AuthHandler {
	return &AuthHandler{
		AuthStore: authStore,
		EventBus:  eventBus,
		JWTSecret: jwtSecret,
		SMSCfg:    smsCfg,
	}
}

// -----------------------------------------------------
// USER ENDPOINTS
// -----------------------------------------------------

type UserRequestOTPPayload struct {
	Mobile       string `json:"mobile" validate:"required"`
	AppSignature string `json:"app_signature,omitempty"`
}

// HandleUserRequestOTP sends a 6-digit OTP to the user
func (h *AuthHandler) HandleUserRequestOTP(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	var payload UserRequestOTPPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &payload) {
		return
	}

	otp, err := h.AuthStore.GenerateAndStoreOTP(r.Context(), payload.Mobile)
	if err != nil {
		response.Error(w, "Failed to generate OTP", http.StatusInternalServerError)
		return
	}
	h.EventBus.PublishEvent(eventbus.ChannelAuthOTPRequested, eventbus.AuthOTPRequestedPayload{
		Mobile: payload.Mobile, Role: "user", RequestID: reqID,
	})

	// Send the SMS
	err = auth.SendSMS(h.SMSCfg, payload.Mobile, otp, payload.AppSignature)
	if err != nil {
		response.Error(w, "Failed to send SMS: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Check if user exists
	user, err := h.AuthStore.FindUserByMobile(r.Context(), payload.Mobile)
	if err != nil {
		response.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	found := user != nil
	name := ""
	if found {
		name = user.Name
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"found": found,
		"name":  name,
	})
}

type UserVerifyOTPPayload struct {
	Name         string `json:"name,omitempty"`
	Mobile       string `json:"mobile" validate:"required"`
	OTP          string `json:"otp" validate:"required"`
	ReferralCode string `json:"referral_code,omitempty"`
}

// HandleUserVerifyOTP checks the OTP and issues a JWT token
func (h *AuthHandler) HandleUserVerifyOTP(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	var payload UserVerifyOTPPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &payload) {
		return
	}

	// 1. Verify OTP
	valid, err := h.AuthStore.VerifyOTP(r.Context(), payload.Mobile, payload.OTP)
	if err != nil {
		response.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	if !valid {
		response.Error(w, "Invalid OTP", http.StatusUnauthorized)
		return
	}

	// 2. Find or Create User
	user, err := h.AuthStore.FindUserByMobile(r.Context(), payload.Mobile)
	if err != nil {
		response.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	if user == nil {
		// Create new user
		if payload.Name == "" {
			response.Error(w, "Name is required for new users", http.StatusBadRequest)
			return
		}
		user, err = h.AuthStore.CreateUser(r.Context(), payload.Name, payload.Mobile, payload.ReferralCode)
		if err != nil {
			response.Error(w, "Failed to create user", http.StatusInternalServerError)
			return
		}
		h.EventBus.PublishEvent(eventbus.ChannelAuthUserRegistered, eventbus.AuthUserRegisteredPayload{
			UserID: user.ID.Hex(), Mobile: payload.Mobile, Name: payload.Name, RequestID: reqID,
		})
	}

	// 3. Generate JWT Token
	token, err := auth.GenerateJWT(user.ID.Hex(), "user", h.JWTSecret)
	if err != nil {
		response.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	// 4. Update Token in DB
	h.AuthStore.UpdateUserJWT(r.Context(), user.ID, token)

	h.EventBus.PublishEvent(eventbus.ChannelAuthUserLoggedIn, eventbus.AuthUserLoggedInPayload{
		UserID: user.ID.Hex(), Mobile: payload.Mobile, RequestID: reqID,
	})

	// 5. Return Token
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"token": token,
	})
}

// -----------------------------------------------------
// DRIVER ENDPOINTS
// -----------------------------------------------------

// HandleDriverRequestOTP sends a 6-digit OTP to the driver
func (h *AuthHandler) HandleDriverRequestOTP(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	var payload UserRequestOTPPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &payload) {
		return
	}

	otp, err := h.AuthStore.GenerateAndStoreOTP(r.Context(), payload.Mobile)
	if err != nil {
		response.Error(w, "Failed to generate OTP", http.StatusInternalServerError)
		return
	}
	h.EventBus.PublishEvent(eventbus.ChannelAuthOTPRequested, eventbus.AuthOTPRequestedPayload{
		Mobile: payload.Mobile, Role: "driver", RequestID: reqID,
	})

	err = auth.SendSMS(h.SMSCfg, payload.Mobile, otp, payload.AppSignature)
	if err != nil {
		response.Error(w, "Failed to send SMS: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Check if driver exists
	driver, err := h.AuthStore.FindDriverByMobile(r.Context(), payload.Mobile)
	if err != nil {
		response.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	unverifiedDriver, err := h.AuthStore.FindUnverifiedDriverByMobile(r.Context(), payload.Mobile)
	if err != nil {
		response.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	found := driver != nil || unverifiedDriver != nil
	name := ""
	if driver != nil {
		name = driver.Name
	} else if unverifiedDriver != nil {
		name = unverifiedDriver.Name
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"found": found,
		"name":  name,
	})
}

// HandleDriverVerifyOTP checks the OTP and issues a JWT token for the driver
func (h *AuthHandler) HandleDriverVerifyOTP(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	var payload UserVerifyOTPPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &payload) {
		return
	}

	valid, err := h.AuthStore.VerifyOTP(r.Context(), payload.Mobile, payload.OTP)
	if err != nil || !valid {
		response.Error(w, "Invalid OTP", http.StatusUnauthorized)
		return
	}

	driver, err := h.AuthStore.FindDriverByMobile(r.Context(), payload.Mobile)
	if err != nil {
		response.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	role := "driver"
	driverID := primitive.NilObjectID
	if driver != nil {
		driverID = driver.ID
	} else {
		role = "unvrf_driver"
		unverifiedDriver, err := h.AuthStore.FindUnverifiedDriverByMobile(r.Context(), payload.Mobile)
		if err != nil {
			response.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
		if unverifiedDriver == nil {
			if payload.Name == "" {
				response.Error(w, "Driver not found. Please sign up first.", http.StatusNotFound)
				return
			}
			unverifiedDriver, err = h.AuthStore.CreateUnverifiedDriver(r.Context(), payload.Name, payload.Mobile)
			if err != nil {
				response.Error(w, "Failed to create driver", http.StatusInternalServerError)
				return
			}
			h.EventBus.PublishEvent(eventbus.ChannelAuthDriverCreated, eventbus.AuthDriverCreatedPayload{
				DriverID: unverifiedDriver.ID.Hex(), Mobile: payload.Mobile, Name: payload.Name, RequestID: reqID,
			})
		}
		driverID = unverifiedDriver.ID
	}

	token, err := auth.GenerateJWT(driverID.Hex(), role, h.JWTSecret)
	if err != nil {
		response.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}
	if role == "driver" {
		h.AuthStore.UpdateDriverJWT(r.Context(), driverID, token)
	} else {
		h.AuthStore.UpdateUnverifiedDriverJWT(r.Context(), driverID, token)
	}

	h.EventBus.PublishEvent(eventbus.ChannelAuthDriverLoggedIn, eventbus.AuthDriverLoggedInPayload{
		DriverID: driverID.Hex(), Mobile: payload.Mobile, Role: role, RequestID: reqID,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"token": token,
	})
}
