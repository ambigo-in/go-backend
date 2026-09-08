package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/auth"

	"ambigo-backend/internal/ids"
	"golang.org/x/crypto/bcrypt"
)

type HospitalAuthHandler struct {
	AuthStore    *auth.Store
	PendingStore *admin.PendingHospitalStore
	HospitalStore *admin.HospitalStore
	JWTSecret    string
	SMSCfg       auth.SMSCountryConfig
}

func NewHospitalAuthHandler(authStore *auth.Store, pendingStore *admin.PendingHospitalStore, hospitalStore *admin.HospitalStore, jwtSecret string, smsCfg auth.SMSCountryConfig) *HospitalAuthHandler {
	return &HospitalAuthHandler{
		AuthStore:    authStore,
		PendingStore: pendingStore,
		HospitalStore: hospitalStore,
		JWTSecret:    jwtSecret,
		SMSCfg:       smsCfg,
	}
}

var hospitalMDSignupMobileRegex = regexp.MustCompile(`^[6-9]\d{9}$`)
var hospitalUsernameRegex = regexp.MustCompile(`^[a-zA-Z0-9_]{4,20}$`)
var hospitalEmailRegex = regexp.MustCompile(`^[\w\-\.]+@([\w-]+\.)+[\w-]{2,4}$`)

// HandleHospitalMDRequestOTP godoc
func (h *HospitalAuthHandler) HandleHospitalMDRequestOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mobile       string `json:"mobile"`
		AppSignature string `json:"app_signature"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !hospitalMDSignupMobileRegex.MatchString(req.Mobile) {
		response.Error(w, "Invalid mobile number", http.StatusBadRequest)
		return
	}
	locked, err := h.AuthStore.IsOTPLocked(r.Context(), req.Mobile)
	if err == nil && locked {
		response.Error(w, "Too many attempts. Try again later.", http.StatusTooManyRequests)
		return
	}
	otp, err := h.AuthStore.GenerateAndStoreOTP(r.Context(), req.Mobile)
	if err != nil {
		response.Error(w, "Failed to generate OTP", http.StatusInternalServerError)
		return
	}
	auth.SendSMSAsync(h.SMSCfg, req.Mobile, otp, req.AppSignature)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "OTP sent"})
}

// HandleHospitalMDLoginRequestOTP checks admin verification before sending OTP for login
func (h *HospitalAuthHandler) HandleHospitalMDLoginRequestOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mobile       string `json:"mobile"`
		AppSignature string `json:"app_signature"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !hospitalMDSignupMobileRegex.MatchString(req.Mobile) {
		response.Error(w, "Invalid mobile number", http.StatusBadRequest)
		return
	}
	locked, err := h.AuthStore.IsOTPLocked(r.Context(), req.Mobile)
	if err == nil && locked {
		response.Error(w, "Too many attempts. Try again later.", http.StatusTooManyRequests)
		return
	}
	md, err := h.AuthStore.FindHospitalMDByMobile(r.Context(), req.Mobile)
	if err != nil {
		response.Error(w, "Failed to verify account", http.StatusInternalServerError)
		return
	}
	if md == nil {
		response.Error(w, "MD not found. Please signup first.", http.StatusNotFound)
		return
	}
	if md.Status != "active" {
		if md.Status == "pending" {
			response.Error(w, "Account pending admin approval", http.StatusForbidden)
			return
		}
		if md.Status == "rejected" {
			response.Error(w, "Account has been rejected", http.StatusForbidden)
			return
		}
		response.Error(w, "Account not active", http.StatusForbidden)
		return
	}
	otp, err := h.AuthStore.GenerateAndStoreOTP(r.Context(), req.Mobile)
	if err != nil {
		response.Error(w, "Failed to generate OTP", http.StatusInternalServerError)
		return
	}
	auth.SendSMSAsync(h.SMSCfg, req.Mobile, otp, req.AppSignature)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "OTP sent"})
}

// HandleHospitalMDSignup creates a pending hospital + pending MD after OTP verification
func (h *HospitalAuthHandler) HandleHospitalMDSignup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name           string  `json:"name" validate:"required"`
		Address        string  `json:"address" validate:"required"`
		Email          string  `json:"email" validate:"required"`
		MDNumber       string  `json:"md_number" validate:"required"`
		OfficialNumber string  `json:"official_number" validate:"required"`
		City           string  `json:"city" validate:"required"`
		Lat            float64 `json:"lat" validate:"required"`
		Lng            float64 `json:"lng" validate:"required"`
		OTP            string  `json:"otp" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !hospitalMDSignupMobileRegex.MatchString(req.MDNumber) || !hospitalMDSignupMobileRegex.MatchString(req.OfficialNumber) {
		response.Error(w, "Invalid mobile number", http.StatusBadRequest)
		return
	}
	if !hospitalEmailRegex.MatchString(req.Email) {
		response.Error(w, "Invalid email", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Address == "" || req.City == "" {
		response.Error(w, "Name, address and city are required", http.StatusBadRequest)
		return
	}
	// Verify OTP on MD number
	ok, err := h.AuthStore.VerifyOTP(r.Context(), req.MDNumber, req.OTP)
	if err != nil || !ok {
		if err == nil {
			_ = h.AuthStore.IncrementFailedOTP(r.Context(), req.MDNumber)
		}
		response.Error(w, "Invalid or expired OTP", http.StatusUnauthorized)
		return
	}
	_ = h.AuthStore.ResetFailedOTP(r.Context(), req.MDNumber)

	// Check existing pending MD with same mobile
	existingMD, _ := h.AuthStore.FindHospitalMDByMobile(r.Context(), req.MDNumber)
	if existingMD != nil && existingMD.Status == "pending" {
		response.Error(w, "Signup already pending approval", http.StatusBadRequest)
		return
	}
	if existingMD != nil && existingMD.Status == "active" {
		response.Error(w, "MD already registered", http.StatusBadRequest)
		return
	}

	// Create pending MD
	md := &auth.HospitalMD{
		Name:           req.Name,
		Email:          req.Email,
		Mobile:         req.MDNumber,
		OfficialNumber: req.OfficialNumber,
		Status:         "pending",
	}
	if err := h.AuthStore.CreateHospitalMD(r.Context(), md); err != nil {
		response.Error(w, "Failed to create signup", http.StatusInternalServerError)
		return
	}

	// Create pending hospital
	pending := &admin.PendingHospital{
		Name:           req.Name,
		Address:        req.Address,
		Email:          req.Email,
		MDNumber:       req.MDNumber,
		OfficialNumber: req.OfficialNumber,
		City:           req.City,
		Location:       &admin.GeoJSON{Type: "Point", Coordinates: []float64{req.Lng, req.Lat}},
		Status:         "pending",
		MDID:           md.ID,
	}
	if err := h.PendingStore.Create(r.Context(), pending); err != nil {
		response.Error(w, "Failed to create pending hospital", http.StatusInternalServerError)
		return
	}
	// Link MD -> pending hospital
	md.HospitalPendingID = &pending.ID
	_ = h.AuthStore.UpdateHospitalMD(r.Context(), md)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Signup submitted for verification", "pending_id": pending.ID})
}

// HandleHospitalMDVerifyOTP is mobile OTP login for MD (after approval, before password setup)
func (h *HospitalAuthHandler) HandleHospitalMDVerifyOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mobile     string `json:"mobile"`
		OTP        string `json:"otp"`
		DeviceID   string `json:"device_id"`
		DeviceName string `json:"device_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !hospitalMDSignupMobileRegex.MatchString(req.Mobile) || req.OTP == "" {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	locked, _ := h.AuthStore.IsOTPLocked(r.Context(), req.Mobile)
	if locked {
		response.Error(w, "Too many attempts. Try again later.", http.StatusTooManyRequests)
		return
	}
	ok, _ := h.AuthStore.VerifyOTP(r.Context(), req.Mobile, req.OTP)
	if !ok {
		_ = h.AuthStore.IncrementFailedOTP(r.Context(), req.Mobile)
		response.Error(w, "Invalid or expired OTP", http.StatusUnauthorized)
		return
	}
	_ = h.AuthStore.ResetFailedOTP(r.Context(), req.Mobile)

	md, err := h.AuthStore.FindHospitalMDByMobile(r.Context(), req.Mobile)
	if err != nil || md == nil {
		response.Error(w, "MD not found. Please signup first.", http.StatusNotFound)
		return
	}
	if md.Status != "active" {
		if md.Status == "pending" {
			response.Error(w, "Account pending admin approval", http.StatusForbidden)
			return
		}
		if md.Status == "rejected" {
			response.Error(w, "Account has been rejected", http.StatusForbidden)
			return
		}
		if md.Status == "banned" {
			response.Error(w, "Account has been banned", http.StatusForbidden)
			return
		}
		response.Error(w, "Account not active", http.StatusForbidden)
		return
	}

	// Check if password not yet setup
	needsSetup := md.PasswordHash == nil || *md.PasswordHash == ""
	var accessToken string
	if md.HospitalID != nil {
		accessToken, err = auth.GenerateHospitalAccessToken(md.ID, "hospital_md", *md.HospitalID, h.JWTSecret)
	} else {
		accessToken, err = auth.GenerateAccessToken(md.ID, "hospital_md", h.JWTSecret)
	}
	if err != nil {
		response.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}
	sessionID := auth.NewSessionID()
	refreshStr, _, err := h.AuthStore.CreateRefreshToken(r.Context(), md.ID, "hospital_md", sessionID, req.DeviceID, req.DeviceName)
	if err != nil {
		response.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}
	_ = h.AuthStore.UpdateHospitalMDJWT(r.Context(), md.ID, accessToken)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"access_token":         accessToken,
		"refresh_token":        refreshStr,
		"session_id":           sessionID,
		"needsPasswordSetup":   needsSetup,
	})
}

// HandleHospitalMDSetupPassword creates username+password after first OTP login
func (h *HospitalAuthHandler) HandleHospitalMDSetupPassword(w http.ResponseWriter, r *http.Request) {
	mdIDStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	role, _ := r.Context().Value(middleware.UserRoleKey).(string)
	if role != "hospital_md" {
		response.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !hospitalUsernameRegex.MatchString(req.Username) {
		response.Error(w, "Username must be 4-20 alphanumeric/underscore", http.StatusBadRequest)
		return
	}
	if len(req.Password) < 8 {
		response.Error(w, "Password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	// Check username unique
	existing, _ := h.AuthStore.FindHospitalMDByUsername(r.Context(), req.Username)
	if existing != nil {
		response.Error(w, "Username already taken", http.StatusBadRequest)
		return
	}
	if !ids.IsValid(mdIDStr) {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	mdID := mdIDStr
	md, err := h.AuthStore.FindHospitalMDByID(r.Context(), mdID)
	if err != nil || md == nil {
		response.Error(w, "MD not found", http.StatusNotFound)
		return
	}
	if md.PasswordHash != nil && *md.PasswordHash != "" {
		response.Error(w, "Password already set", http.StatusBadRequest)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		response.Error(w, "Failed to hash password", http.StatusInternalServerError)
		return
	}
	hashStr := string(hash)
	md.Username = &req.Username
	md.PasswordHash = &hashStr
	if err := h.AuthStore.UpdateHospitalMD(r.Context(), md); err != nil {
		response.Error(w, "Failed to save password", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Password setup successful"})
}

// HandleHospitalMDLoginPassword is username+password login for MD (after setup)
func (h *HospitalAuthHandler) HandleHospitalMDLoginPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		DeviceID   string `json:"device_id"`
		DeviceName string `json:"device_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	md, err := h.AuthStore.FindHospitalMDByUsername(r.Context(), req.Username)
	if err != nil || md == nil {
		response.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}
	if md.Status != "active" {
		if md.Status == "pending" {
			response.Error(w, "Account pending admin approval", http.StatusForbidden)
			return
		}
		if md.Status == "rejected" {
			response.Error(w, "Account has been rejected", http.StatusForbidden)
			return
		}
		if md.Status == "banned" {
			response.Error(w, "Account has been banned", http.StatusForbidden)
			return
		}
		response.Error(w, "Account not active", http.StatusForbidden)
		return
	}
	if md.PasswordHash == nil || bcrypt.CompareHashAndPassword([]byte(*md.PasswordHash), []byte(req.Password)) != nil {
		response.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}
	var accessToken string
	if md.HospitalID != nil {
		accessToken, err = auth.GenerateHospitalJWT(md.ID, "hospital_md", *md.HospitalID, h.JWTSecret)
	} else {
		accessToken, err = auth.GenerateJWT(md.ID, "hospital_md", "", h.JWTSecret)
	}
	if err != nil {
		response.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}
	sessionID := auth.NewSessionID()
	refreshStr, _, err := h.AuthStore.CreateRefreshToken(r.Context(), md.ID, "hospital_md", sessionID, req.DeviceID, req.DeviceName)
	if err != nil {
		response.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}
	_ = h.AuthStore.UpdateHospitalMDJWT(r.Context(), md.ID, accessToken)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"access_token":  accessToken,
		"refresh_token": refreshStr,
		"session_id":    sessionID,
		"role":          "hospital_md",
	})
}

// HandleHospitalMDStatus returns MD signup status by mobile (public, for pending stepper)
func (h *HospitalAuthHandler) HandleHospitalMDStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mobile string `json:"mobile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !hospitalMDSignupMobileRegex.MatchString(req.Mobile) {
		response.Error(w, "Invalid mobile number", http.StatusBadRequest)
		return
	}
	md, err := h.AuthStore.FindHospitalMDByMobile(r.Context(), req.Mobile)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if md == nil {
		response.Error(w, "MD not found", http.StatusNotFound)
		return
	}
	// Also fetch pending for rejection reason if any
	var pendingStatus *string
	var rejectionReason *string
	if md.HospitalPendingID != nil {
		if pending, _ := h.PendingStore.FindByID(r.Context(), *md.HospitalPendingID); pending != nil {
			pendingStatus = &pending.Status
			if pending.RejectionReason != nil {
				rejectionReason = pending.RejectionReason
			}
		}
	}
	// Fallback: check pending by md_number if no pending_id link
	if pendingStatus == nil {
		if pending, _ := h.PendingStore.FindByMDNumber(r.Context(), req.Mobile); pending != nil {
			pendingStatus = &pending.Status
			rejectionReason = pending.RejectionReason
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":           md.Status,
		"pending_status":   pendingStatus,
		"rejection_reason": rejectionReason,
		"hospital_id":      md.HospitalID,
		"name":             md.Name,
		"mobile":           md.Mobile,
	})
}

// HandleHospitalMDMe returns current MD profile
func (h *HospitalAuthHandler) HandleHospitalMDMe(w http.ResponseWriter, r *http.Request) {
	mdIDStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !ids.IsValid(mdIDStr) {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	mdID := mdIDStr
	md, err := h.AuthStore.FindHospitalMDByID(r.Context(), mdID)
	if err != nil || md == nil {
		response.Error(w, "MD not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":           md.ID,
		"name":         md.Name,
		"email":        md.Email,
		"mobile":       md.Mobile,
		"username":     md.Username,
		"hospital_id":  md.HospitalID,
		"status":       md.Status,
	})
}
