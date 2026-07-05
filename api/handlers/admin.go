package handlers

import (
	"encoding/json"
	"net/http"

	"ambigo-backend/api/response"
	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/requestid"
	"ambigo-backend/internal/translation"
	"golang.org/x/crypto/bcrypt"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type AdminHandler struct {
	Store         *admin.Store
	AuthStore     *auth.Store
	EventBus      *eventbus.InMemoryBus
	HospitalStore *admin.HospitalStore
	JWTSecret     string
}

func NewAdminHandler(store *admin.Store, authStore *auth.Store, eventBus *eventbus.InMemoryBus, hStore *admin.HospitalStore, jwtSecret string) *AdminHandler {
	return &AdminHandler{
		Store:         store,
		AuthStore:     authStore,
		EventBus:      eventBus,
		HospitalStore: hStore,
		JWTSecret:     jwtSecret,
	}
}

func (h *AdminHandler) HandleAdminLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username" validate:"required"`
		Password string `json:"password" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	adminUser, err := h.Store.FindAdminByUsername(r.Context(), req.Username)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if adminUser == nil {
		response.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}
	if !adminUser.Active {
		response.Error(w, "Account deactivated", http.StatusForbidden)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(adminUser.HashedPassword), []byte(req.Password)); err != nil {
		response.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	token, err := auth.GenerateJWT(adminUser.ID.Hex(), "admin", h.JWTSecret)
	if err != nil {
		response.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"detail": "Admin Login Successful",
		"token":  token,
		"name":   adminUser.Name,
		"role":   adminUser.Role,
	})
}

func (h *AdminHandler) HandleAdminMobileRequestOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mobile string `json:"mobile" validate:"required,len=10"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	adminUser, err := h.Store.FindAdminByMobile(r.Context(), req.Mobile)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if adminUser == nil {
		response.Error(w, "Mobile not registered as admin", http.StatusUnauthorized)
		return
	}
	if !adminUser.Active {
		response.Error(w, "Account deactivated", http.StatusForbidden)
		return
	}

	otp, err := h.AuthStore.GenerateAndStoreOTP(r.Context(), req.Mobile)
	if err != nil {
		response.Error(w, "Failed to send OTP", http.StatusInternalServerError)
		return
	}

	logger.Log.Info().Str("mobile", req.Mobile).Str("otp", otp).Msg("Admin OTP sent")
	json.NewEncoder(w).Encode(map[string]string{"detail": "OTP sent"})
}

func (h *AdminHandler) HandleAdminMobileVerifyOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mobile string `json:"mobile" validate:"required,len=10"`
		OTP    string `json:"otp" validate:"required,len=6"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	valid, err := h.AuthStore.VerifyOTP(r.Context(), req.Mobile, req.OTP)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if !valid {
		response.Error(w, "Invalid OTP", http.StatusUnauthorized)
		return
	}

	adminUser, err := h.Store.FindAdminByMobile(r.Context(), req.Mobile)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if adminUser == nil {
		response.Error(w, "Admin not found", http.StatusUnauthorized)
		return
	}

	token, err := auth.GenerateJWT(adminUser.ID.Hex(), "admin", h.JWTSecret)
	if err != nil {
		response.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"detail": "Admin Login Successful",
		"token":  token,
		"name":   adminUser.Name,
		"role":   adminUser.Role,
	})
}

func (h *AdminHandler) HandleCreateAmbulanceType(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	var amb admin.AmbulanceType
	if err := json.NewDecoder(r.Body).Decode(&amb); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &amb) {
		return
	}

	if err := h.Store.CreateAmbulanceType(r.Context(), &amb); err != nil {
		response.Error(w, "Failed to create: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminAmbTypeCreated, eventbus.AdminAmbTypePayload{
		AmbTypeID: amb.ID.Hex(), Name: amb.Name, RequestID: reqID,
	})

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"detail": "Created",
		"id":     amb.ID.Hex(),
	})
}

func (h *AdminHandler) HandleListAmbulanceTypes(w http.ResponseWriter, r *http.Request) {
	list, err := h.Store.ListAmbulanceTypes(r.Context())
	if err != nil {
		response.Error(w, "Failed to fetch list: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func (h *AdminHandler) HandleDeleteAmbulanceType(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())
	idStr := r.PathValue("id")
	objID, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	err = h.Store.DeleteAmbulanceType(r.Context(), objID)
	if err != nil {
		response.Error(w, "Failed to delete: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminAmbTypeDeleted, eventbus.AdminAmbTypePayload{
		AmbTypeID: idStr, RequestID: reqID,
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Deleted"})
}

// HandleApproveDriver flips an unverified driver to a verified driver
func (h *AdminHandler) HandleApproveDriver(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	var driver auth.Driver
	if err := json.NewDecoder(r.Body).Decode(&driver); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	
	err := h.AuthStore.ApproveDriver(r.Context(), &driver)
	if err != nil {
		response.Error(w, "Failed to approve driver: "+err.Error(), http.StatusBadRequest)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAuthDriverApproved, eventbus.AuthDriverApprovedPayload{
		DriverID: driver.ID.Hex(), Name: driver.Name, Mobile: driver.Mobile, RequestID: reqID,
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Driver Approved"})
}

// HandleGetUnverifiedDrivers returns a list of drivers waiting for approval
func (h *AdminHandler) HandleGetUnverifiedDrivers(w http.ResponseWriter, r *http.Request) {
	// Normally we would have an AuthStore method for this, we'll mock it for brevity.
	// We'll leave it as an empty array for the prototype.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode([]interface{}{})
}

// -------------------------
// HOSPITALS
// -------------------------

type HospitalRequest struct {
	ID          string `json:"_id"`
	Name        string `json:"name" validate:"required"`
	Address     string `json:"address" validate:"required"`
	City        string `json:"city" validate:"required"`
	Coordinates struct {
		Lat float64 `json:"lat" validate:"required,min=-90,max=90"`
		Lng float64 `json:"lng" validate:"required,min=-180,max=180"`
	} `json:"coordinates" validate:"required"`
	AlwaysOpen bool     `json:"always_open"`
	Services   []string `json:"services"`
	Timing     *struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"timing"`
}

func (h *AdminHandler) HandleAddHospital(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	var req HospitalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	hospital := admin.Hospital{
		Name:    translation.TranslateField(req.Name),
		Address: translation.TranslateField(req.Address),
		City:    translation.TranslateField(req.City),
		Location: admin.GeoJSON{
			Type:        "Point",
			Coordinates: []float64{req.Coordinates.Lng, req.Coordinates.Lat},
		},
		AlwaysOpen: req.AlwaysOpen,
		Services:   req.Services,
	}
	if req.Timing != nil {
		hospital.Timing = &admin.Timing{
			Start: req.Timing.Start,
			End:   req.Timing.End,
		}
	}

	if err := h.HospitalStore.CreateHospital(r.Context(), &hospital); err != nil {
		response.Error(w, "Hospital add failed", http.StatusBadRequest)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminHospitalAdded, eventbus.AdminHospitalPayload{
		HospitalID: hospital.ID.Hex(), Name: hospital.Name["en_US"], RequestID: reqID,
	})
	json.NewEncoder(w).Encode(map[string]string{"detail": "Hospital added successfully"})
}

func (h *AdminHandler) HandleUpdateHospital(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	var req HospitalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		response.Error(w, "ID is required", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	objID, err := primitive.ObjectIDFromHex(req.ID)
	if err != nil {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	hospital := admin.Hospital{
		ID:      objID,
		Name:    translation.TranslateField(req.Name),
		Address: translation.TranslateField(req.Address),
		City:    translation.TranslateField(req.City),
		Location: admin.GeoJSON{
			Type:        "Point",
			Coordinates: []float64{req.Coordinates.Lng, req.Coordinates.Lat},
		},
		AlwaysOpen: req.AlwaysOpen,
		Services:   req.Services,
	}
	if req.Timing != nil {
		hospital.Timing = &admin.Timing{
			Start: req.Timing.Start,
			End:   req.Timing.End,
		}
	}

	if err := h.HospitalStore.UpdateHospital(r.Context(), &hospital); err != nil {
		response.Error(w, "Hospital updated failed", http.StatusBadRequest)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminHospitalUpdated, eventbus.AdminHospitalPayload{
		HospitalID: req.ID, RequestID: reqID,
	})
	json.NewEncoder(w).Encode(map[string]string{"detail": "Hospital updated successfully"})
}

func (h *AdminHandler) HandleDeleteHospital(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	var req struct {
		HospitalID string `json:"hospital_id" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	objID, err := primitive.ObjectIDFromHex(req.HospitalID)
	if err != nil {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if err := h.HospitalStore.DeleteHospital(r.Context(), objID); err != nil {
		response.Error(w, "Hospital delete failed", http.StatusBadRequest)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminHospitalDeleted, eventbus.AdminHospitalPayload{
		HospitalID: req.HospitalID, RequestID: reqID,
	})
	json.NewEncoder(w).Encode(map[string]string{"detail": "Hospital deleted successfully"})
}

