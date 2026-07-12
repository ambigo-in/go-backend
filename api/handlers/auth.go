package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/requestid"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

var mobileRegex = regexp.MustCompile(`^[6-9]\d{9}$`)

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

type otpPayload struct {
	Mobile       string `json:"mobile"`
	AppSignature string `json:"app_signature,omitempty"`
	DeviceID     string `json:"device_id,omitempty"`
	DeviceName   string `json:"device_name,omitempty"`
}

type verifyPayload struct {
	Name         string `json:"name,omitempty"`
	Mobile       string `json:"mobile"`
	OTP          string `json:"otp"`
	ReferralCode string `json:"referral_code,omitempty"`
	DeviceID     string `json:"device_id,omitempty"`
	DeviceName   string `json:"device_name,omitempty"`
}

func (h *AuthHandler) HandleUserRequestOTP(w http.ResponseWriter, r *http.Request) {
	var payload otpPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	// V11: Validate mobile number
	if !mobileRegex.MatchString(payload.Mobile) {
		response.Error(w, "Invalid mobile number", http.StatusBadRequest)
		return
	}

	// V13: Check account lockout
	locked, err := h.AuthStore.IsOTPLocked(r.Context(), payload.Mobile)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if locked {
		response.Error(w, "Too many attempts. Try again later.", http.StatusTooManyRequests)
		return
	}

	otp, err := h.AuthStore.GenerateAndStoreOTP(r.Context(), payload.Mobile)
	if err != nil {
		logger.Log.Error().Err(err).Str("mobile", payload.Mobile).Msg("Failed to generate OTP")
		response.Error(w, "Failed to send OTP", http.StatusInternalServerError)
		return
	}

	reqID := requestid.FromContext(r.Context())
	h.EventBus.PublishEvent(eventbus.ChannelAuthOTPRequested, eventbus.AuthOTPRequestedPayload{
		Mobile: payload.Mobile, Role: "user", RequestID: reqID,
	})

	// V12: Don't leak SMS error details
	if err := auth.SendSMS(h.SMSCfg, payload.Mobile, otp, payload.AppSignature); err != nil {
		logger.Log.Error().Err(err).Str("mobile", payload.Mobile).Msg("SMS send failed")
		response.Error(w, "Failed to send OTP", http.StatusInternalServerError)
		return
	}

	// Look up existing user to show name on OTP screen
	var name string
	existingUser, _ := h.AuthStore.FindUserByMobile(r.Context(), payload.Mobile)
	if existingUser != nil {
		name = existingUser.Name
	}
	response.Success(w, http.StatusOK, map[string]string{"detail": "OTP sent", "name": name})
}

func (h *AuthHandler) HandleUserVerifyOTP(w http.ResponseWriter, r *http.Request) {
	var payload verifyPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	if !mobileRegex.MatchString(payload.Mobile) {
		response.Error(w, "Invalid mobile number", http.StatusBadRequest)
		return
	}

	// V13: Check lockout before OTP verify
	locked, err := h.AuthStore.IsOTPLocked(r.Context(), payload.Mobile)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if locked {
		response.Error(w, "Too many attempts. Try again later.", http.StatusTooManyRequests)
		return
	}

	// V2: VerifyOTP now checks expiry in application code
	valid, err := h.AuthStore.VerifyOTP(r.Context(), payload.Mobile, payload.OTP)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if !valid {
		// V13: Track failed attempt
		_ = h.AuthStore.IncrementFailedOTP(r.Context(), payload.Mobile)
		response.Error(w, "Invalid OTP", http.StatusUnauthorized)
		return
	}

	// V13: Reset failed attempts on success
	_ = h.AuthStore.ResetFailedOTP(r.Context(), payload.Mobile)

	user, err := h.AuthStore.FindUserByMobile(r.Context(), payload.Mobile)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	reqID := requestid.FromContext(r.Context())

	if user == nil {
		if payload.Name == "" {
			response.Error(w, "Name is required for new users", http.StatusBadRequest)
			return
		}
		// V17: Validate referral code if provided
		if payload.ReferralCode != "" {
			validRef, err := h.AuthStore.ValidateReferralCode(r.Context(), payload.ReferralCode)
			if err != nil {
				response.Error(w, "Internal error", http.StatusInternalServerError)
				return
			}
			if !validRef {
				response.Error(w, "Invalid referral code", http.StatusBadRequest)
				return
			}
		}
		user, err = h.AuthStore.CreateUser(r.Context(), payload.Name, payload.Mobile, payload.ReferralCode)
		if err != nil {
			logger.Log.Error().Err(err).Str("mobile", payload.Mobile).Msg("Failed to create user")
			response.Error(w, "Registration failed", http.StatusInternalServerError)
			return
		}
		h.EventBus.PublishEvent(eventbus.ChannelAuthUserRegistered, eventbus.AuthUserRegisteredPayload{
			UserID: user.ID.Hex(), Mobile: payload.Mobile, Name: payload.Name, RequestID: reqID,
		})
	}

	// V8: Generate access token + refresh token
	accessToken, err := auth.GenerateAccessToken(user.ID.Hex(), "user", h.JWTSecret)
	if err != nil {
		response.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	refreshToken, _, err := h.AuthStore.CreateRefreshToken(r.Context(), user.ID.Hex(), "user", payload.DeviceID, payload.DeviceName)
	if err != nil {
		response.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	h.AuthStore.UpdateUserJWT(r.Context(), user.ID, accessToken)

	h.EventBus.PublishEvent(eventbus.ChannelAuthUserLoggedIn, eventbus.AuthUserLoggedInPayload{
		UserID: user.ID.Hex(), Mobile: payload.Mobile, RequestID: reqID,
	})

	response.Success(w, http.StatusOK, map[string]string{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
	})
}

func (h *AuthHandler) HandleDriverRequestOTP(w http.ResponseWriter, r *http.Request) {
	var payload otpPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	if !mobileRegex.MatchString(payload.Mobile) {
		response.Error(w, "Invalid mobile number", http.StatusBadRequest)
		return
	}

	locked, err := h.AuthStore.IsOTPLocked(r.Context(), payload.Mobile)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if locked {
		response.Error(w, "Too many attempts. Try again later.", http.StatusTooManyRequests)
		return
	}

	otp, err := h.AuthStore.GenerateAndStoreOTP(r.Context(), payload.Mobile)
	if err != nil {
		logger.Log.Error().Err(err).Str("mobile", payload.Mobile).Msg("Failed to generate OTP")
		response.Error(w, "Failed to send OTP", http.StatusInternalServerError)
		return
	}

	reqID := requestid.FromContext(r.Context())
	h.EventBus.PublishEvent(eventbus.ChannelAuthOTPRequested, eventbus.AuthOTPRequestedPayload{
		Mobile: payload.Mobile, Role: "driver", RequestID: reqID,
	})

	if err := auth.SendSMS(h.SMSCfg, payload.Mobile, otp, payload.AppSignature); err != nil {
		logger.Log.Error().Err(err).Str("mobile", payload.Mobile).Msg("SMS send failed")
		response.Error(w, "Failed to send OTP", http.StatusInternalServerError)
		return
	}

	// Look up existing driver to show name on OTP screen
	var name string
	existingDriver, _ := h.AuthStore.FindDriverByMobile(r.Context(), payload.Mobile)
	if existingDriver != nil {
		name = existingDriver.Name
	}
	response.Success(w, http.StatusOK, map[string]string{"detail": "OTP sent", "name": name})
}

func (h *AuthHandler) HandleDriverVerifyOTP(w http.ResponseWriter, r *http.Request) {
	var payload verifyPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	if !mobileRegex.MatchString(payload.Mobile) {
		response.Error(w, "Invalid mobile number", http.StatusBadRequest)
		return
	}

	locked, err := h.AuthStore.IsOTPLocked(r.Context(), payload.Mobile)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if locked {
		response.Error(w, "Too many attempts. Try again later.", http.StatusTooManyRequests)
		return
	}

	valid, err := h.AuthStore.VerifyOTP(r.Context(), payload.Mobile, payload.OTP)
	if err != nil || !valid {
		_ = h.AuthStore.IncrementFailedOTP(r.Context(), payload.Mobile)
		response.Error(w, "Invalid OTP", http.StatusUnauthorized)
		return
	}
	_ = h.AuthStore.ResetFailedOTP(r.Context(), payload.Mobile)

	driver, err := h.AuthStore.FindDriverByMobile(r.Context(), payload.Mobile)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	role := "driver"
	driverID := primitive.NilObjectID
	reqID := requestid.FromContext(r.Context())

	if driver != nil {
		driverID = driver.ID
	} else {
		role = "unvrf_driver"
		unverifiedDriver, err := h.AuthStore.FindUnverifiedDriverByMobile(r.Context(), payload.Mobile)
		if err != nil {
			response.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		if unverifiedDriver == nil {
			if payload.Name == "" {
				response.Error(w, "Name is required to register", http.StatusBadRequest)
				return
			}
			unverifiedDriver, err = h.AuthStore.CreateUnverifiedDriver(r.Context(), payload.Name, payload.Mobile)
			if err != nil {
				logger.Log.Error().Err(err).Str("mobile", payload.Mobile).Msg("Failed to create driver")
				response.Error(w, "Registration failed", http.StatusInternalServerError)
				return
			}
			h.EventBus.PublishEvent(eventbus.ChannelAuthDriverCreated, eventbus.AuthDriverCreatedPayload{
				DriverID: unverifiedDriver.ID.Hex(), Mobile: payload.Mobile, Name: payload.Name, RequestID: reqID,
			})
		}
		driverID = unverifiedDriver.ID
	}

	accessToken, err := auth.GenerateAccessToken(driverID.Hex(), role, h.JWTSecret)
	if err != nil {
		response.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	refreshToken, _, err := h.AuthStore.CreateRefreshToken(r.Context(), driverID.Hex(), role, payload.DeviceID, payload.DeviceName)
	if err != nil {
		response.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	if role == "driver" {
		h.AuthStore.UpdateDriverJWT(r.Context(), driverID, accessToken)
	} else {
		h.AuthStore.UpdateUnverifiedDriverJWT(r.Context(), driverID, accessToken)
	}

	h.EventBus.PublishEvent(eventbus.ChannelAuthDriverLoggedIn, eventbus.AuthDriverLoggedInPayload{
		DriverID: driverID.Hex(), Mobile: payload.Mobile, Role: role, RequestID: reqID,
	})

	response.Success(w, http.StatusOK, map[string]string{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
	})
}

// V8: Token refresh endpoint
func (h *AuthHandler) HandleRefreshToken(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		RefreshToken string `json:"refresh_token"`
		DeviceID     string `json:"device_id,omitempty"`
		DeviceName   string `json:"device_name,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.RefreshToken == "" {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	// V5: Validate refresh token (checks revoked + expiry)
	oldRT, err := h.AuthStore.ValidateRefreshToken(r.Context(), payload.RefreshToken)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if oldRT == nil {
		response.Error(w, "Invalid or expired refresh token", http.StatusUnauthorized)
		return
	}

	// Rotate: revoke old, create new
	_ = h.AuthStore.RevokeRefreshToken(r.Context(), payload.RefreshToken)

	newAccessToken, err := auth.GenerateAccessToken(oldRT.UserID, oldRT.Role, h.JWTSecret)
	if err != nil {
		response.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	newRefreshToken, _, err := h.AuthStore.CreateRefreshToken(r.Context(), oldRT.UserID, oldRT.Role, payload.DeviceID, payload.DeviceName)
	if err != nil {
		response.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	response.Success(w, http.StatusOK, map[string]string{
		"access_token":  newAccessToken,
		"refresh_token": newRefreshToken,
	})
}

// V4: Logout endpoint
func (h *AuthHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	role, _ := r.Context().Value(middleware.UserRoleKey).(string)

	// Revoke all refresh tokens for this user
	_ = h.AuthStore.RevokeAllUserRefreshTokens(r.Context(), userID)

	// Clear stored JWT
	objID, err := primitive.ObjectIDFromHex(userID)
	if err == nil {
		switch role {
		case "user":
			_ = h.AuthStore.ClearUserJWT(r.Context(), objID)
		case "driver":
			_ = h.AuthStore.ClearDriverJWT(r.Context(), objID)
		case "unvrf_driver":
			_ = h.AuthStore.ClearUnverifiedDriverJWT(r.Context(), objID)
		}
	}

	response.Success(w, http.StatusOK, map[string]string{"detail": "Logged out"})
}

// V18: List active sessions
func (h *AuthHandler) HandleListSessions(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	sessions, err := h.AuthStore.ListUserSessions(r.Context(), userID)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	response.Success(w, http.StatusOK, map[string]interface{}{"sessions": sessions})
}

// V18: Revoke a specific session
func (h *AuthHandler) HandleRevokeSession(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	var payload struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.DeviceID == "" {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	_ = h.AuthStore.RevokeSessionByDeviceID(r.Context(), userID, payload.DeviceID)
	response.Success(w, http.StatusOK, map[string]string{"detail": "Session revoked"})
}
