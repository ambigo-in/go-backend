package handlers

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/ids"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/mailer"
	"golang.org/x/crypto/bcrypt"
)

type HospitalReceptionistHandler struct {
	AuthStore *auth.Store
	JWTSecret string
	Mailer    *mailer.ResendMailer
}

func NewHospitalReceptionistHandler(authStore *auth.Store, jwtSecret string, mailer *mailer.ResendMailer) *HospitalReceptionistHandler {
	return &HospitalReceptionistHandler{AuthStore: authStore, JWTSecret: jwtSecret, Mailer: mailer}
}

var receptionistUsernameRegex = regexp.MustCompile(`^[a-zA-Z0-9_]{4,20}$`)
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

func generateTempPassword() string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"
	const length = 12
	b := make([]byte, length)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[n.Int64()]
	}
	return string(b)
}

func generateUsernameFromEmail(email string) string {
	prefix := strings.Split(email, "@")[0]
	// sanitize to alnum underscore
	sanitized := ""
	for _, ch := range prefix {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') {
			sanitized += string(ch)
		} else {
			sanitized += "_"
		}
	}
	sanitized = strings.Trim(sanitized, "_")
	if len(sanitized) < 4 {
		sanitized = sanitized + "user"
	}
	if len(sanitized) > 12 {
		sanitized = sanitized[:12]
	}
	// add 4-digit suffix
	suffix := ""
	for i := 0; i < 4; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(10))
		suffix += fmt.Sprintf("%d", n.Int64())
	}
	username := fmt.Sprintf("%s%s", sanitized, suffix)
	// ensure still matches regex and <=20
	if len(username) > 20 {
		username = username[:20]
	}
	return username
}

func (h *HospitalReceptionistHandler) HandleCreateReceptionist(w http.ResponseWriter, r *http.Request) {
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
	mdID := mdIDStr
	md, err := h.AuthStore.FindHospitalMDByID(r.Context(), mdID)
	if err != nil || md == nil || md.HospitalID == nil {
		response.Error(w, "MD not linked to hospital", http.StatusBadRequest)
		return
	}
	var req struct {
		Name   string `json:"name"`
		Email  string `json:"email"`
		Mobile string `json:"mobile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Name == "" {
		response.Error(w, "Name required", http.StatusBadRequest)
		return
	}
	if !emailRegex.MatchString(req.Email) {
		response.Error(w, "Valid email required", http.StatusBadRequest)
		return
	}
	// email uniqueness
	if existing, _ := h.AuthStore.FindHospitalReceptionistByEmail(r.Context(), req.Email); existing != nil {
		response.Error(w, "Email already used", http.StatusBadRequest)
		return
	}
	// generate unique username
	username := ""
	for i := 0; i < 5; i++ {
		candidate := generateUsernameFromEmail(req.Email)
		if existing, _ := h.AuthStore.FindHospitalReceptionistByUsername(r.Context(), candidate); existing == nil {
			username = candidate
			break
		}
	}
	if username == "" {
		response.Error(w, "Failed to generate username", http.StatusInternalServerError)
		return
	}
	tempPass := generateTempPassword()
	hash, err := bcrypt.GenerateFromPassword([]byte(tempPass), bcrypt.DefaultCost)
	if err != nil {
		response.Error(w, "Failed to hash password", http.StatusInternalServerError)
		return
	}
	hashStr := string(hash)
	var mobilePtr *string
	if strings.TrimSpace(req.Mobile) != "" {
		m := strings.TrimSpace(req.Mobile)
		mobilePtr = &m
	}
	emailLower := strings.ToLower(req.Email)
	recept := &auth.HospitalReceptionist{
		HospitalID:         *md.HospitalID,
		CreatedByMDID:      md.ID,
		Name:               req.Name,
		Username:           username,
		Email:              &emailLower,
		PasswordHash:       hashStr,
		Mobile:             mobilePtr,
		Active:             true,
		Status:             "invited",
		MustChangePassword: true,
	}
	if err := h.AuthStore.CreateHospitalReceptionist(r.Context(), recept); err != nil {
		response.Error(w, "Failed to create receptionist", http.StatusInternalServerError)
		return
	}
	// send invite email (test mode will redirect to delivered@resend.dev)
	hospitalName := "Ambigo Hospital"
	if h.Mailer != nil {
		if err := h.Mailer.SendReceptionistInvite(emailLower, username, tempPass, hospitalName); err != nil {
			logger.Log.Error().Err(err).Str("email", emailLower).Msg("Failed to send receptionist invite email")
			// not failing creation, allow resend later
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Receptionist created, invite sent", "id": recept.ID, "username": username, "email": emailLower})
}

func (h *HospitalReceptionistHandler) HandleListReceptionists(w http.ResponseWriter, r *http.Request) {
	mdIDStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	mdID := mdIDStr
	md, err := h.AuthStore.FindHospitalMDByID(r.Context(), mdID)
	if err != nil || md == nil || md.HospitalID == nil {
		response.Error(w, "MD not linked to hospital", http.StatusBadRequest)
		return
	}
	list, err := h.AuthStore.ListReceptionistsByHospital(r.Context(), *md.HospitalID)
	if err != nil {
		response.Error(w, "Failed to fetch", http.StatusInternalServerError)
		return
	}
	// Return minimal view for MD: email, username, status, delete. But return full struct filtered.
	type view struct {
		ID                 string  `json:"_id"`
		Email              *string `json:"email,omitempty"`
		Username           string  `json:"username"`
		Name               string  `json:"name"`
		Status             string  `json:"status"`
		MustChangePassword bool    `json:"must_change_password"`
		Active             bool    `json:"active"`
	}
	out := make([]view, 0, len(list))
	for _, rec := range list {
		out = append(out, view{
			ID:                 rec.ID,
			Email:              rec.Email,
			Username:           rec.Username,
			Name:               rec.Name,
			Status:             rec.Status,
			MustChangePassword: rec.MustChangePassword,
			Active:             rec.Active,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (h *HospitalReceptionistHandler) HandleDeleteReceptionist(w http.ResponseWriter, r *http.Request) {
	mdIDStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	mdID := mdIDStr
	md, err := h.AuthStore.FindHospitalMDByID(r.Context(), mdID)
	if err != nil || md == nil || md.HospitalID == nil {
		response.Error(w, "MD not linked to hospital", http.StatusBadRequest)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		response.Error(w, "ID required", http.StatusBadRequest)
		return
	}
	if !ids.IsValid(req.ID) {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	recept, err := h.AuthStore.FindHospitalReceptionistByID(r.Context(), req.ID)
	if err != nil || recept == nil {
		response.Error(w, "Receptionist not found", http.StatusNotFound)
		return
	}
	if recept.HospitalID != *md.HospitalID {
		response.Error(w, "Forbidden: different hospital", http.StatusForbidden)
		return
	}
	if err := h.AuthStore.DeleteHospitalReceptionist(r.Context(), req.ID); err != nil {
		response.Error(w, "Delete failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Receptionist deleted"})
}

func (h *HospitalReceptionistHandler) HandleResendInvite(w http.ResponseWriter, r *http.Request) {
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
	md, err := h.AuthStore.FindHospitalMDByID(r.Context(), mdIDStr)
	if err != nil || md == nil || md.HospitalID == nil {
		response.Error(w, "MD not linked to hospital", http.StatusBadRequest)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !ids.IsValid(req.ID) {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	recept, err := h.AuthStore.FindHospitalReceptionistByID(r.Context(), req.ID)
	if err != nil || recept == nil {
		response.Error(w, "Receptionist not found", http.StatusNotFound)
		return
	}
	if recept.HospitalID != *md.HospitalID {
		response.Error(w, "Forbidden: different hospital", http.StatusForbidden)
		return
	}
	if recept.Email == nil || *recept.Email == "" {
		response.Error(w, "Receptionist has no email", http.StatusBadRequest)
		return
	}
	tempPass := generateTempPassword()
	hash, err := bcrypt.GenerateFromPassword([]byte(tempPass), bcrypt.DefaultCost)
	if err != nil {
		response.Error(w, "Failed to hash", http.StatusInternalServerError)
		return
	}
	if err := h.AuthStore.SetReceptionistTempPassword(r.Context(), recept.ID, string(hash)); err != nil {
		response.Error(w, "Failed to update", http.StatusInternalServerError)
		return
	}
	if h.Mailer != nil {
		hospitalName := "Ambigo Hospital"
		if err := h.Mailer.SendReceptionistInvite(*recept.Email, recept.Username, tempPass, hospitalName); err != nil {
			logger.Log.Error().Err(err).Msg("Resend invite failed")
			response.Error(w, "Failed to send email", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Invite resent"})
}

func (h *HospitalReceptionistHandler) HandleChangePassword(w http.ResponseWriter, r *http.Request) {
	uid, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	role, _ := r.Context().Value(middleware.UserRoleKey).(string)
	if role != "hospital_receptionist" {
		response.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if len(req.NewPassword) < 8 {
		response.Error(w, "New password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	recept, err := h.AuthStore.FindHospitalReceptionistByID(r.Context(), uid)
	if err != nil || recept == nil {
		response.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(recept.PasswordHash), []byte(req.OldPassword)) != nil {
		response.Error(w, "Old password incorrect", http.StatusUnauthorized)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		response.Error(w, "Failed to hash", http.StatusInternalServerError)
		return
	}
	if err := h.AuthStore.UpdateReceptionistPassword(r.Context(), recept.ID, string(hash)); err != nil {
		response.Error(w, "Failed to update", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Password changed"})
}

func (h *HospitalReceptionistHandler) HandleReceptionistLogin(w http.ResponseWriter, r *http.Request) {
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
	recept, err := h.AuthStore.FindHospitalReceptionistByUsername(r.Context(), req.Username)
	if err != nil || recept == nil {
		response.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}
	if !recept.Active || recept.Status == "disabled" {
		response.Error(w, "Account disabled", http.StatusForbidden)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(recept.PasswordHash), []byte(req.Password)) != nil {
		response.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}
	accessToken, err := auth.GenerateHospitalJWT(recept.ID, "hospital_receptionist", recept.HospitalID, h.JWTSecret)
	if err != nil {
		response.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}
	sessionID := auth.NewSessionID()
	refreshStr, _, err := h.AuthStore.CreateRefreshToken(r.Context(), recept.ID, "hospital_receptionist", sessionID, req.DeviceID, req.DeviceName)
	if err != nil {
		response.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}
	_ = h.AuthStore.UpdateHospitalReceptionistJWT(r.Context(), recept.ID, accessToken)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"access_token":         accessToken,
		"refresh_token":        refreshStr,
		"session_id":           sessionID,
		"role":                 "hospital_receptionist",
		"hospital_id":          recept.HospitalID,
		"must_change_password": recept.MustChangePassword,
		"status":               recept.Status,
	})
}

func (h *HospitalReceptionistHandler) HandleReceptionistMe(w http.ResponseWriter, r *http.Request) {
	uid, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	role, _ := r.Context().Value(middleware.UserRoleKey).(string)
	if role != "hospital_receptionist" && role != "hospital_md" {
		response.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	// Try receptionist first
	if role == "hospital_receptionist" {
		oid := uid
		recept, err := h.AuthStore.FindHospitalReceptionistByID(r.Context(), oid)
		if err == nil && recept != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":                   recept.ID,
				"username":             recept.Username,
				"email":                recept.Email,
				"name":                 recept.Name,
				"hospital_id":          recept.HospitalID,
				"role":                 "hospital_receptionist",
				"status":               recept.Status,
				"must_change_password": recept.MustChangePassword,
			})
			return
		}
	}
	// Fallback: hospital_md also can call me
	oid := uid
	md, err := h.AuthStore.FindHospitalMDByID(r.Context(), oid)
	if err == nil && md != nil {
		var hid interface{}
		if md.HospitalID != nil {
			hid = *md.HospitalID
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":          md.ID,
			"username":    md.Username,
			"name":        md.Name,
			"hospital_id": hid,
			"role":        "hospital_md",
		})
		return
	}
	response.Error(w, "Not found", http.StatusNotFound)
}
