package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/requestid"
	"ambigo-backend/internal/ride"
	"ambigo-backend/internal/translation"
	"golang.org/x/crypto/bcrypt"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type AdminHandler struct {
	Store         *admin.Store
	AuthStore     *auth.Store
	EventBus      *eventbus.InMemoryBus
	HospitalStore *admin.HospitalStore
	CounterStore  *admin.CounterStore
	RideStore     *ride.Store
	JWTSecret     string
	SMSCfg        auth.SMSCountryConfig
}

func NewAdminHandler(store *admin.Store, authStore *auth.Store, eventBus *eventbus.InMemoryBus, hStore *admin.HospitalStore, cStore *admin.CounterStore, rStore *ride.Store, jwtSecret string, smsCfg auth.SMSCountryConfig) *AdminHandler {
	return &AdminHandler{
		Store:         store,
		AuthStore:     authStore,
		EventBus:      eventBus,
		HospitalStore: hStore,
		CounterStore:  cStore,
		RideStore:     rStore,
		JWTSecret:     jwtSecret,
		SMSCfg:        smsCfg,
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

	token, err := auth.GenerateJWT(adminUser.ID.Hex(), "admin", adminUser.Role, h.JWTSecret)
	if err != nil {
		response.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"detail": "Admin Login Successful",
		"token":  token,
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
		response.Error(w, "Failed to generate OTP", http.StatusInternalServerError)
		return
	}

	if err := auth.SendSMS(h.SMSCfg, req.Mobile, otp, ""); err != nil {
		logger.Log.Error().Err(err).Str("mobile", req.Mobile).Msg("Admin OTP SMS send failed")
		response.Error(w, "Failed to send SMS", http.StatusInternalServerError)
		return
	}

	logger.Log.Info().Str("mobile", req.Mobile).Msg("Admin OTP sent via SMS")
	json.NewEncoder(w).Encode(map[string]string{"detail": "OTP sent", "name": adminUser.Name})
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
	if !adminUser.Active {
		response.Error(w, "Account deactivated", http.StatusForbidden)
		return
	}

	token, err := auth.GenerateJWT(adminUser.ID.Hex(), "admin", adminUser.Role, h.JWTSecret)
	if err != nil {
		response.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	refreshToken, _, err := h.AuthStore.CreateRefreshToken(r.Context(), adminUser.ID.Hex(), "admin", "", "")
	if err != nil {
		logger.Log.Error().Err(err).Msg("Admin refresh token creation failed")
	}

	response.Success(w, http.StatusOK, map[string]string{
		"access_token":  token,
		"refresh_token": refreshToken,
	})
}

// V19: Admin password change
func (h *AdminHandler) HandleAdminPasswordChange(w http.ResponseWriter, r *http.Request) {
	adminIDStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		CurrentPassword string `json:"current_password" validate:"required"`
		NewPassword     string `json:"new_password" validate:"required,min=8"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	objID, err := primitive.ObjectIDFromHex(adminIDStr)
	if err != nil {
		response.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	adminUser, err := h.Store.FindAdminByID(r.Context(), objID)
	if err != nil || adminUser == nil {
		response.Error(w, "Admin not found", http.StatusNotFound)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(adminUser.HashedPassword), []byte(req.CurrentPassword)); err != nil {
		response.Error(w, "Current password is incorrect", http.StatusUnauthorized)
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	if err := h.Store.UpdateAdminPassword(r.Context(), objID, string(hashed)); err != nil {
		response.Error(w, "Failed to update password", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"detail": "Password updated successfully"})
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

// ---------------------------------------------------------------
// VERIFIED DRIVERS
// ---------------------------------------------------------------

// HandleListDrivers returns a paginated list of verified drivers
func (h *AdminHandler) HandleListDrivers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Skip int64 `json:"skip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	drivers, total, err := h.AuthStore.ListDrivers(r.Context(), req.Skip)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"total": total,
		"data":  drivers,
	})
}

// HandleGetDriverDetails returns a single verified driver with full details
func (h *AdminHandler) HandleGetDriverDetails(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	objID, err := primitive.ObjectIDFromHex(req.ID)
	if err != nil {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	driver, err := h.AuthStore.FindDriverByID(r.Context(), objID)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if driver == nil {
		response.Error(w, "Driver not found", http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(driver)
}

// HandleAddDriver creates a new verified driver
func (h *AdminHandler) HandleAddDriver(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	var driver auth.Driver
	if err := json.NewDecoder(r.Body).Decode(&driver); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &driver) {
		return
	}

	if err := h.AuthStore.InsertDriver(r.Context(), &driver); err != nil {
		response.Error(w, "Failed to add driver", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAuthDriverCreated, eventbus.AuthDriverCreatedPayload{
		DriverID: driver.ID.Hex(), Name: driver.Name, Mobile: driver.Mobile, RequestID: reqID,
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Driver added successfully"})
}

// HandleUpdateDriver updates an existing verified driver
func (h *AdminHandler) HandleUpdateDriver(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	var driver auth.Driver
	if err := json.NewDecoder(r.Body).Decode(&driver); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if driver.ID.IsZero() {
		response.Error(w, "ID is required", http.StatusBadRequest)
		return
	}

	if err := h.AuthStore.UpdateDriver(r.Context(), &driver); err != nil {
		response.Error(w, "Failed to update driver", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAuthDriverLoggedIn, eventbus.AuthDriverLoggedInPayload{
		DriverID: driver.ID.Hex(), Mobile: driver.Mobile, RequestID: reqID,
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Driver updated successfully"})
}

// HandleDeleteDriver removes a verified driver
func (h *AdminHandler) HandleDeleteDriver(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	objID, err := primitive.ObjectIDFromHex(req.ID)
	if err != nil {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if err := h.AuthStore.DeleteDriver(r.Context(), objID); err != nil {
		response.Error(w, "Failed to delete driver", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"detail": "Driver deleted successfully"})
}

// ---------------------------------------------------------------
// UNVERIFIED DRIVERS
// ---------------------------------------------------------------

// HandleListUnverifiedDrivers returns drivers pending approval
func (h *AdminHandler) HandleListUnverifiedDrivers(w http.ResponseWriter, r *http.Request) {
	drivers, err := h.AuthStore.ListUnverifiedDrivers(r.Context())
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(drivers)
}

// HandleListAllUnverifiedDrivers returns all unverified drivers (including rejected/in-progress)
func (h *AdminHandler) HandleListAllUnverifiedDrivers(w http.ResponseWriter, r *http.Request) {
	drivers, err := h.AuthStore.ListAllUnverifiedDrivers(r.Context())
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(drivers)
}

// HandleFetchUnverifiedDriver returns a single unverified driver
func (h *AdminHandler) HandleFetchUnverifiedDriver(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	objID, err := primitive.ObjectIDFromHex(req.ID)
	if err != nil {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
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

	json.NewEncoder(w).Encode(driver)
}

// HandleAcceptDriver approves an unverified driver, moving them to verified drivers
func (h *AdminHandler) HandleAcceptDriver(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	var driver auth.Driver
	if err := json.NewDecoder(r.Body).Decode(&driver); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if driver.ID.IsZero() {
		response.Error(w, "Driver ID is required", http.StatusBadRequest)
		return
	}

	if err := h.AuthStore.ApproveDriver(r.Context(), &driver); err != nil {
		response.Error(w, "Failed to approve driver: "+err.Error(), http.StatusBadRequest)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAuthDriverApproved, eventbus.AuthDriverApprovedPayload{
		DriverID: driver.ID.Hex(), Name: driver.Name, Mobile: driver.Mobile, RequestID: reqID,
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Driver Approved"})
}

// HandleRejectDriver sets the error message on an unverified driver
func (h *AdminHandler) HandleRejectDriver(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	var req struct {
		DriverID     string `json:"driver_id" validate:"required"`
		ErrorMessage string `json:"error_message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	objID, err := primitive.ObjectIDFromHex(req.DriverID)
	if err != nil {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if err := h.AuthStore.RejectUnverifiedDriver(r.Context(), objID, req.ErrorMessage); err != nil {
		response.Error(w, "Failed to reject driver", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminDriverRejected, eventbus.AdminDriverRejectedPayload{
		DriverID: req.DriverID, RequestID: reqID,
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Driver Rejected"})
}

// HandleUnverifiedDriverCounter returns the current count of unverified drivers
func (h *AdminHandler) HandleUnverifiedDriverCounter(w http.ResponseWriter, r *http.Request) {
	count, err := h.CounterStore.GetCounter(r.Context(), "unverified_drivers")
	if err != nil {
		response.Error(w, "Error fetching counter", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(strconv.Itoa(count))
}

// ---------------------------------------------------------------
// DRIVER RIDE HISTORY
// ---------------------------------------------------------------

// HandleDriverRideList returns the ride history for a specific driver
func (h *AdminHandler) HandleDriverRideList(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	rides, err := h.RideStore.GetRideHistory(r.Context(), req.ID, "driver", 100, 0)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(rides)
}

// ---------------------------------------------------------------
// ADMIN PROFILE
// ---------------------------------------------------------------

// HandleAdminFCMUpdate updates the admin's FCM token
func (h *AdminHandler) HandleAdminFCMUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FCMToken string `json:"fcm_token" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	adminIDStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	objID, err := primitive.ObjectIDFromHex(adminIDStr)
	if err != nil {
		response.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	if err := h.Store.UpdateAdminFCM(r.Context(), objID, req.FCMToken); err != nil {
		response.Error(w, "Failed to update FCM token", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"detail": "FCM token updated"})
}

// HandleAdminLocationUpdate updates the admin's location
func (h *AdminHandler) HandleAdminLocationUpdate(w http.ResponseWriter, r *http.Request) {
	var loc admin.GeoJSON
	if err := json.NewDecoder(r.Body).Decode(&loc); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	adminIDStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	objID, err := primitive.ObjectIDFromHex(adminIDStr)
	if err != nil {
		response.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	if err := h.Store.UpdateAdminLocation(r.Context(), objID, &loc); err != nil {
		response.Error(w, "Failed to update location", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"detail": "Location updated"})
}

// ---------------------------------------------------------------
// USERS
// ---------------------------------------------------------------

// HandleListUsers returns all registered users
func (h *AdminHandler) HandleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.AuthStore.ListUsers(r.Context())
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(users)
}

// ---------------------------------------------------------------
// USER RIDE HISTORY
// ---------------------------------------------------------------

// HandleUserRideList returns the ride history for a specific user
func (h *AdminHandler) HandleUserRideList(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	rides, err := h.RideStore.GetRideHistory(r.Context(), req.ID, "user", 100, 0)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(rides)
}

// ---------------------------------------------------------------
// RIDE LISTING (completed / ongoing)
// ---------------------------------------------------------------

type rideStatusFilter struct {
	Status ride.RideStatus `json:"status"`
}

// HandleListCompletedRides returns all completed rides
func (h *AdminHandler) HandleListCompletedRides(w http.ResponseWriter, r *http.Request) {
	rides, err := h.RideStore.ListRidesByStatus(r.Context(), []ride.RideStatus{ride.StatusCompleted}, 100, 0)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(rides)
}

// HandleListOngoingRides returns all currently active rides (ASSIGNED, ARRIVED, IN_PROGRESS)
func (h *AdminHandler) HandleListOngoingRides(w http.ResponseWriter, r *http.Request) {
	rides, err := h.RideStore.ListRidesByStatus(r.Context(), []ride.RideStatus{ride.StatusAssigned, ride.StatusArrived, ride.StatusInProgress}, 100, 0)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(rides)
}

// ---------------------------------------------------------------
// AMBULANCE TYPE UPDATE
// ---------------------------------------------------------------

// HandleUpdateAmbulanceType updates an existing ambulance type
func (h *AdminHandler) HandleUpdateAmbulanceType(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	var amb admin.AmbulanceType
	if err := json.NewDecoder(r.Body).Decode(&amb); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if amb.ID.IsZero() {
		response.Error(w, "ID is required", http.StatusBadRequest)
		return
	}

	if err := h.Store.UpdateAmbulanceType(r.Context(), &amb); err != nil {
		response.Error(w, "Failed to update ambulance type", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminAmbTypeCreated, eventbus.AdminAmbTypePayload{
		AmbTypeID: amb.ID.Hex(), Name: amb.Name, RequestID: reqID,
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Ambulance type updated"})
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
