package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/ids"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/requestid"

	"golang.org/x/crypto/bcrypt"
)

// HandleListStaff returns all admin team members.
func (h *AdminHandler) HandleListStaff(w http.ResponseWriter, r *http.Request) {
	staff, err := h.Store.ListStaff(r.Context())
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to list staff")
		response.Error(w, "Failed to list staff", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(staff)
}

// HandleCreateStaff creates a new staff/co-admin account.
func (h *AdminHandler) HandleCreateStaff(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())
	callerID, _ := r.Context().Value(middleware.UserIDKey).(string)
	callerRole, _ := r.Context().Value(middleware.AdminRoleKey).(string)

	var req struct {
		Username string `json:"username" validate:"required"`
		Name     string `json:"name" validate:"required"`
		Role     string `json:"role"`
		Mobile   string `json:"mobile"`
		Password string `json:"password" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	req.Name = strings.TrimSpace(req.Name)
	req.Mobile = strings.TrimSpace(req.Mobile)
	req.Role = strings.TrimSpace(req.Role)

	if len(req.Username) < 3 {
		response.Error(w, "Username must be at least 3 characters", http.StatusBadRequest)
		return
	}
	if len(req.Password) < 6 {
		response.Error(w, "Password must be at least 6 characters", http.StatusBadRequest)
		return
	}

	if req.Role == "" {
		req.Role = "admin"
	}
	if req.Role != "admin" && req.Role != "super_admin" {
		response.Error(w, "Role must be 'admin' or 'super_admin'", http.StatusBadRequest)
		return
	}

	// Only super_admin (or legacy root admin with empty role) can create another super_admin
	if req.Role == "super_admin" && callerRole != "super_admin" && callerRole != "" {
		response.Error(w, "Forbidden: only super_admin can create another super_admin", http.StatusForbidden)
		return
	}

	exists, err := h.Store.CheckAdminUsernameExists(r.Context(), req.Username, "")
	if err != nil {
		logger.Log.Error().Err(err).Str("username", req.Username).Msg("Username check failed")
		response.Error(w, "Internal error checking username", http.StatusInternalServerError)
		return
	}
	if exists {
		response.Error(w, "Staff member with this username already exists", http.StatusConflict)
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Password hash failed")
		response.Error(w, "Failed to hash password", http.StatusInternalServerError)
		return
	}

	staff, err := h.Store.CreateStaff(r.Context(), ids.New(), req.Username, string(hashed), req.Name, req.Role, req.Mobile)
	if err != nil {
		logger.Log.Error().Err(err).Str("username", req.Username).Msg("Failed to create staff")
		response.Error(w, "Failed to create staff member", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminStaffCreated, eventbus.AdminStaffPayload{
		AdminID:   staff.ID,
		Username:  staff.Username,
		Name:      staff.Name,
		Role:      staff.Role,
		Action:    "created",
		ActorID:   callerID,
		RequestID: reqID,
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(staff)
}

// HandleUpdateStaff updates an existing staff member's details or role.
func (h *AdminHandler) HandleUpdateStaff(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())
	callerRole, _ := r.Context().Value(middleware.AdminRoleKey).(string)
	callerID, _ := r.Context().Value(middleware.UserIDKey).(string)

	var req struct {
		ID       string `json:"id" validate:"required"`
		Name     string `json:"name"`
		Role     string `json:"role"`
		Mobile   string `json:"mobile"`
		Password string `json:"password,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	if !ids.IsValid(req.ID) {
		response.Error(w, "Invalid staff ID", http.StatusBadRequest)
		return
	}

	existing, err := h.Store.FindAdminByID(r.Context(), req.ID)
	if err != nil || existing == nil {
		response.Error(w, "Staff member not found", http.StatusNotFound)
		return
	}

	// Only super_admin (or empty root role) can edit a super_admin
	if existing.Role == "super_admin" && callerRole != "super_admin" && callerRole != "" {
		response.Error(w, "Forbidden: only super_admin can modify a super_admin", http.StatusForbidden)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = existing.Name
	}
	req.Mobile = strings.TrimSpace(req.Mobile)

	req.Role = strings.TrimSpace(req.Role)
	if req.Role == "" {
		req.Role = existing.Role
	}
	if req.Role != "admin" && req.Role != "super_admin" {
		response.Error(w, "Role must be 'admin' or 'super_admin'", http.StatusBadRequest)
		return
	}

	// If promoting to super_admin:
	if req.Role == "super_admin" && existing.Role != "super_admin" && callerRole != "super_admin" && callerRole != "" {
		response.Error(w, "Forbidden: only super_admin can promote to super_admin", http.StatusForbidden)
		return
	}

	// If demoting from super_admin:
	if existing.Role == "super_admin" && req.Role != "super_admin" {
		count, err := h.Store.CountActiveSuperAdmins(r.Context())
		if err != nil || count <= 1 {
			response.Error(w, "Cannot demote the only active super_admin", http.StatusBadRequest)
			return
		}
	}

	// Optional password reset
	if req.Password != "" {
		if len(req.Password) < 6 {
			response.Error(w, "Password must be at least 6 characters", http.StatusBadRequest)
			return
		}
		hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			response.Error(w, "Failed to hash password", http.StatusInternalServerError)
			return
		}
		if err := h.Store.UpdateAdminPassword(r.Context(), req.ID, string(hashed)); err != nil {
			response.Error(w, "Failed to update password", http.StatusInternalServerError)
			return
		}
	}

	updated, err := h.Store.UpdateStaff(r.Context(), req.ID, req.Name, req.Mobile, req.Role)
	if err != nil {
		logger.Log.Error().Err(err).Str("id", req.ID).Msg("Failed to update staff")
		response.Error(w, "Failed to update staff member", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminStaffUpdated, eventbus.AdminStaffPayload{
		AdminID:   updated.ID,
		Username:  updated.Username,
		Name:      updated.Name,
		Role:      updated.Role,
		Action:    "updated",
		ActorID:   callerID,
		RequestID: reqID,
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(updated)
}

// HandleToggleStaff activates or suspends a staff member.
func (h *AdminHandler) HandleToggleStaff(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())
	callerID, _ := r.Context().Value(middleware.UserIDKey).(string)
	callerRole, _ := r.Context().Value(middleware.AdminRoleKey).(string)

	var req struct {
		ID     string `json:"id" validate:"required"`
		Active *bool  `json:"active,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	if !ids.IsValid(req.ID) {
		response.Error(w, "Invalid staff ID", http.StatusBadRequest)
		return
	}

	if callerID == req.ID {
		response.Error(w, "Cannot change the active status of your own account", http.StatusBadRequest)
		return
	}

	existing, err := h.Store.FindAdminByID(r.Context(), req.ID)
	if err != nil || existing == nil {
		response.Error(w, "Staff member not found", http.StatusNotFound)
		return
	}

	if existing.Role == "super_admin" && callerRole != "super_admin" && callerRole != "" {
		response.Error(w, "Forbidden: only super_admin can suspend a super_admin", http.StatusForbidden)
		return
	}

	var nextActive bool
	if req.Active != nil {
		nextActive = *req.Active
	} else {
		nextActive = !existing.Active
	}

	if !nextActive && existing.Role == "super_admin" {
		count, err := h.Store.CountActiveSuperAdmins(r.Context())
		if err != nil || count <= 1 {
			response.Error(w, "Cannot deactivate the only active super_admin", http.StatusBadRequest)
			return
		}
	}

	if !nextActive {
		_, _ = h.AuthStore.RevokeAllUserRefreshTokens(r.Context(), req.ID, "staff_suspended")
	}

	updated, err := h.Store.SetStaffActive(r.Context(), req.ID, nextActive)
	if err != nil {
		logger.Log.Error().Err(err).Str("id", req.ID).Msg("Failed to toggle staff")
		response.Error(w, "Failed to toggle staff status", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminStaffToggled, eventbus.AdminStaffPayload{
		AdminID:   updated.ID,
		Username:  updated.Username,
		Name:      updated.Name,
		Role:      updated.Role,
		Action:    "toggled",
		ActorID:   callerID,
		RequestID: reqID,
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(updated)
}

// HandleDeleteStaff removes a staff member permanently.
func (h *AdminHandler) HandleDeleteStaff(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())
	callerID, _ := r.Context().Value(middleware.UserIDKey).(string)
	callerRole, _ := r.Context().Value(middleware.AdminRoleKey).(string)

	var req struct {
		ID string `json:"id" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	if !ids.IsValid(req.ID) {
		response.Error(w, "Invalid staff ID", http.StatusBadRequest)
		return
	}

	if callerID == req.ID {
		response.Error(w, "Cannot delete your own account", http.StatusBadRequest)
		return
	}

	existing, err := h.Store.FindAdminByID(r.Context(), req.ID)
	if err != nil || existing == nil {
		response.Error(w, "Staff member not found", http.StatusNotFound)
		return
	}

	if existing.Role == "super_admin" {
		if callerRole != "super_admin" && callerRole != "" {
			response.Error(w, "Forbidden: only super_admin can delete a super_admin", http.StatusForbidden)
			return
		}
		count, err := h.Store.CountActiveSuperAdmins(r.Context())
		if err != nil || count <= 1 {
			response.Error(w, "Cannot delete the only active super_admin", http.StatusBadRequest)
			return
		}
	}

	_, _ = h.AuthStore.RevokeAllUserRefreshTokens(r.Context(), req.ID, "staff_deleted")

	if err := h.Store.DeleteStaff(r.Context(), req.ID); err != nil {
		logger.Log.Error().Err(err).Str("id", req.ID).Msg("Failed to delete staff")
		response.Error(w, "Failed to delete staff member", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminStaffDeleted, eventbus.AdminStaffPayload{
		AdminID:   req.ID,
		Username:  existing.Username,
		Name:      existing.Name,
		Role:      existing.Role,
		Action:    "deleted",
		ActorID:   callerID,
		RequestID: reqID,
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"detail": "Staff member deleted successfully",
		"id":     req.ID,
	})
}
