package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/ids"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/mailer"
	"ambigo-backend/internal/requestid"
	"ambigo-backend/internal/ride"
	"ambigo-backend/internal/translation"
	"golang.org/x/crypto/bcrypt"
)

type AdminHandler struct {
	Store                *admin.Store
	AuthStore            *auth.Store
	EventBus             *eventbus.InMemoryBus
	HospitalStore        *admin.HospitalStore
	HospitalCityStore    *admin.HospitalCityStore
	PendingHospitalStore *admin.PendingHospitalStore
	CounterStore         *admin.CounterStore
	RideStore            *ride.Store
	JWTSecret            string
	SMSCfg               auth.SMSCountryConfig
	Mailer               *mailer.ResendMailer
}

func NewAdminHandler(store *admin.Store, authStore *auth.Store, eventBus *eventbus.InMemoryBus, hStore *admin.HospitalStore, hcStore *admin.HospitalCityStore, pStore *admin.PendingHospitalStore, cStore *admin.CounterStore, rStore *ride.Store, jwtSecret string, smsCfg auth.SMSCountryConfig, mailer *mailer.ResendMailer) *AdminHandler {
	return &AdminHandler{
		Store:                store,
		AuthStore:            authStore,
		EventBus:             eventBus,
		HospitalStore:        hStore,
		HospitalCityStore:    hcStore,
		PendingHospitalStore: pStore,
		CounterStore:         cStore,
		RideStore:            rStore,
		JWTSecret:            jwtSecret,
		SMSCfg:               smsCfg,
		Mailer:               mailer,
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

	token, err := auth.GenerateJWT(adminUser.ID, "admin", adminUser.Role, h.JWTSecret)
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

	auth.SendSMSAsync(h.SMSCfg, req.Mobile, otp, "")
	logger.Log.Info().Str("mobile", req.Mobile).Msg("Admin OTP enqueued via async SMS worker")
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

	token, err := auth.GenerateJWT(adminUser.ID, "admin", adminUser.Role, h.JWTSecret)
	if err != nil {
		response.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	refreshToken, _, err := h.AuthStore.CreateRefreshToken(r.Context(), adminUser.ID, "admin", auth.NewSessionID(), "", "")
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

	if !ids.IsValid(adminIDStr) {
		response.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	adminUser, err := h.Store.FindAdminByID(r.Context(), adminIDStr)
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

	if err := h.Store.UpdateAdminPassword(r.Context(), adminIDStr, string(hashed)); err != nil {
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
		AmbTypeID: amb.ID, Name: amb.Name, RequestID: reqID,
	})

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"detail": "Created",
		"id":     amb.ID,
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
	if !ids.IsValid(idStr) {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	err := h.Store.DeleteAmbulanceType(r.Context(), idStr)
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

	if !ids.IsValid(req.ID) {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	driver, err := h.AuthStore.FindDriverByID(r.Context(), req.ID)
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
		DriverID: driver.ID, Name: driver.Name, Mobile: driver.Mobile, RequestID: reqID,
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Driver added successfully"})
}

// HandleUpdateDriver updates an existing verified driver
func (h *AdminHandler) HandleUpdateDriver(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	var req auth.Driver
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if ids.IsZero(req.ID) {
		response.Error(w, "ID is required", http.StatusBadRequest)
		return
	}
	if !ids.IsValid(req.ID) {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	existing, err := h.AuthStore.FindDriverByID(r.Context(), req.ID)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if existing == nil {
		response.Error(w, "Driver not found", http.StatusNotFound)
		return
	}

	// Merge: only overwrite fields that are non-empty / non-nil in the incoming payload
	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.Mobile != "" {
		existing.Mobile = req.Mobile
	}
	if req.Photo != "" {
		existing.Photo = req.Photo
	}
	if req.VehicleType != "" {
		existing.VehicleType = req.VehicleType
	}
	if req.VehicleReg != "" {
		existing.VehicleReg = req.VehicleReg
	}
	if req.ReferralCode != "" {
		existing.ReferralCode = req.ReferralCode
	}
	if req.MyReferralCode != "" {
		existing.MyReferralCode = req.MyReferralCode
	}
	if req.WalletBalance != 0 {
		existing.WalletBalance = req.WalletBalance
	}
	if req.Location != nil {
		existing.Location = req.Location
	}
	if req.FCMToken != nil {
		existing.FCMToken = req.FCMToken
	}
	if req.JWTToken != nil {
		existing.JWTToken = req.JWTToken
	}
	if req.LastLocationUpdate != nil {
		existing.LastLocationUpdate = req.LastLocationUpdate
	}
	if req.WalletDetails != nil {
		if existing.WalletDetails == nil {
			existing.WalletDetails = &auth.WalletDetails{}
		}
		if req.WalletDetails.AccountNo != "" {
			existing.WalletDetails.AccountNo = req.WalletDetails.AccountNo
		}
		if req.WalletDetails.BenfName != "" {
			existing.WalletDetails.BenfName = req.WalletDetails.BenfName
		}
		if req.WalletDetails.IFSCCode != "" {
			existing.WalletDetails.IFSCCode = req.WalletDetails.IFSCCode
		}
		if req.WalletDetails.BenfID != "" {
			existing.WalletDetails.BenfID = req.WalletDetails.BenfID
		}
	}
	if req.Details != nil {
		if existing.Details == nil {
			existing.Details = &auth.DriverDetails{}
		}
		if req.Details.POIImage != "" {
			existing.Details.POIImage = req.Details.POIImage
		}
		if req.Details.RCNumber != "" {
			existing.Details.RCNumber = req.Details.RCNumber
		}
		if req.Details.RCImage != "" {
			existing.Details.RCImage = req.Details.RCImage
		}
		if req.Details.DLNumber != "" {
			existing.Details.DLNumber = req.Details.DLNumber
		}
		if req.Details.DLImage != "" {
			existing.Details.DLImage = req.Details.DLImage
		}
		if req.Details.AmbFront != "" {
			existing.Details.AmbFront = req.Details.AmbFront
		}
		if req.Details.AmbInside != "" {
			existing.Details.AmbInside = req.Details.AmbInside
		}
	}

	if err := h.AuthStore.UpdateDriver(r.Context(), existing); err != nil {
		response.Error(w, "Failed to update driver", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAuthDriverLoggedIn, eventbus.AuthDriverLoggedInPayload{
		DriverID: existing.ID, Mobile: existing.Mobile, RequestID: reqID,
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

	if !ids.IsValid(req.ID) {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if err := h.AuthStore.DeleteDriver(r.Context(), req.ID); err != nil {
		response.Error(w, "Failed to delete driver", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"detail": "Driver deleted successfully"})
}

// ---------------------------------------------------------------
// UNVERIFIED DRIVERS
// ---------------------------------------------------------------

// HandleListUnverifiedDrivers returns drivers pending approval (capped at 50, keyset paginated).
func (h *AdminHandler) HandleListUnverifiedDrivers(w http.ResponseWriter, r *http.Request) {
	limit, cursor, offset := parsePagination(r)
	var drivers []auth.UnverifiedDriver
	var err error
	if offset > 0 && cursor == "" {
		drivers, err = h.AuthStore.ListUnverifiedDriversWithOffset(r.Context(), limit, offset)
	} else {
		drivers, err = h.AuthStore.ListUnverifiedDriversPaginated(r.Context(), limit, cursor)
	}
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(drivers)
}

// HandleListAllUnverifiedDrivers returns all unverified drivers (including rejected/in-progress) capped at 50.
func (h *AdminHandler) HandleListAllUnverifiedDrivers(w http.ResponseWriter, r *http.Request) {
	limit, cursor, offset := parsePagination(r)
	var drivers []auth.UnverifiedDriver
	var err error
	if offset > 0 && cursor == "" {
		drivers, err = h.AuthStore.ListAllUnverifiedDriversWithOffset(r.Context(), limit, offset)
	} else {
		drivers, err = h.AuthStore.ListAllUnverifiedDriversPaginated(r.Context(), limit, cursor)
	}
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

	if !ids.IsValid(req.ID) {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	driver, err := h.AuthStore.FindUnverifiedDriverByID(r.Context(), req.ID)
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
	if ids.IsZero(driver.ID) {
		response.Error(w, "Driver ID is required", http.StatusBadRequest)
		return
	}

	if err := h.AuthStore.ApproveDriver(r.Context(), &driver); err != nil {
		response.Error(w, "Failed to approve driver: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Revoke old refresh tokens so the driver must re-login with role "driver"
	if _, revokeErr := h.AuthStore.RevokeAllUserRefreshTokens(r.Context(), driver.ID, "driver_approved"); revokeErr != nil {
		logger.Log.Error().Err(revokeErr).Str("driver_id", driver.ID).Msg("Failed to revoke driver refresh tokens after approval")
	}

	h.EventBus.PublishEvent(eventbus.ChannelAuthDriverApproved, eventbus.AuthDriverApprovedPayload{
		DriverID: driver.ID, Name: driver.Name, Mobile: driver.Mobile, RequestID: reqID,
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

	if !ids.IsValid(req.DriverID) {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if err := h.AuthStore.RejectUnverifiedDriver(r.Context(), req.DriverID, req.ErrorMessage); err != nil {
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
	if !ids.IsValid(adminIDStr) {
		response.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	if err := h.Store.UpdateAdminFCM(r.Context(), adminIDStr, req.FCMToken); err != nil {
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
	if !ids.IsValid(adminIDStr) {
		response.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	if err := h.Store.UpdateAdminLocation(r.Context(), adminIDStr, &loc); err != nil {
		response.Error(w, "Failed to update location", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"detail": "Location updated"})
}

// ---------------------------------------------------------------
// USERS
// ---------------------------------------------------------------

// HandleListUsers returns registered users capped at 50, keyset paginated with limit/skip or cursor.
func (h *AdminHandler) HandleListUsers(w http.ResponseWriter, r *http.Request) {
	limit, cursor, offset := parsePagination(r)
	var users []auth.User
	var err error
	if offset > 0 && cursor == "" {
		users, err = h.AuthStore.ListUsersWithOffset(r.Context(), limit, offset)
	} else {
		users, err = h.AuthStore.ListUsersPaginated(r.Context(), limit, cursor)
	}
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(users)
}

// parsePagination extracts limit (default 50, capped 50), cursor and offset from query params or JSON body.
func parsePagination(r *http.Request) (int, string, int) {
	limit := 50
	cursor := r.URL.Query().Get("cursor")
	if cursor == "" {
		cursor = r.URL.Query().Get("after_id")
	}
	offset := 0
	if v := r.URL.Query().Get("skip"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	} else if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 50 {
				n = 50
			}
			limit = n
		}
	}
	// Also try JSON body: {"limit":50,"cursor":"...","after_id":"...","skip":0}
	if r.Body != nil && r.ContentLength != 0 {
		var body struct {
			Limit   *int   `json:"limit"`
			Cursor  string `json:"cursor"`
			AfterID string `json:"after_id"`
			Skip    *int   `json:"skip"`
			Offset  *int   `json:"offset"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			if body.Limit != nil && *body.Limit > 0 {
				n := *body.Limit
				if n > 50 {
					n = 50
				}
				limit = n
			}
			if body.Cursor != "" {
				cursor = body.Cursor
			} else if body.AfterID != "" {
				cursor = body.AfterID
			}
			if body.Skip != nil && *body.Skip >= 0 {
				offset = *body.Skip
			} else if body.Offset != nil && *body.Offset >= 0 {
				offset = *body.Offset
			}
		}
	}
	return limit, cursor, offset
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

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	var req admin.AmbulanceType
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if ids.IsZero(req.ID) {
		response.Error(w, "ID is required", http.StatusBadRequest)
		return
	}

	existing, err := h.Store.GetAmbulanceTypeByID(r.Context(), req.ID)
	if err != nil {
		response.Error(w, "Failed to fetch ambulance type: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if existing == nil {
		response.Error(w, "Ambulance type not found", http.StatusNotFound)
		return
	}

	// Merge: only overwrite non-zero / provided fields to support partial updates
	merged := *existing
	if req.Name != "" {
		merged.Name = req.Name
	}
	if req.Photo != "" {
		merged.Photo = req.Photo
	}
	if req.ListingThreshold != 0 {
		merged.ListingThreshold = req.ListingThreshold
	}
	if req.BaseFare != 0 {
		merged.BaseFare = req.BaseFare
	}
	if req.DriverShare != 0 {
		merged.DriverShare = req.DriverShare
	}
	if len(req.PricingTier) > 0 {
		merged.PricingTier = req.PricingTier
	}
	// Bool fields: detect presence via raw JSON to allow explicit false
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &raw); err == nil {
		if _, ok := raw["helper_included"]; ok {
			merged.HelperIncluded = req.HelperIncluded
		}
		if _, ok := raw["otp_required"]; ok {
			merged.OTPRequired = req.OTPRequired
		}
	} else {
		// fallback: overwrite if incoming differs from existing (allows toggling)
		if req.HelperIncluded != existing.HelperIncluded {
			merged.HelperIncluded = req.HelperIncluded
		}
		if req.OTPRequired != existing.OTPRequired {
			merged.OTPRequired = req.OTPRequired
		}
	}

	if !response.Validate(w, &merged) {
		return
	}

	if err := h.Store.UpdateAmbulanceType(r.Context(), &merged); err != nil {
		response.Error(w, "Failed to update ambulance type", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminAmbTypeCreated, eventbus.AdminAmbTypePayload{
		AmbTypeID: merged.ID, Name: merged.Name, RequestID: reqID,
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Ambulance type updated"})
}

// -------------------------
// HOSPITALS
// -------------------------

type HospitalRequest struct {
	ID           string `json:"_id"`
	Name         string `json:"name" validate:"required"`
	Address      string `json:"address" validate:"required"`
	City         string `json:"city" validate:"required"`
	HospitalType string `json:"hospital_type"`
	Coordinates  struct {
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

	hType := req.HospitalType
	if hType == "" {
		hType = admin.ClassifyHospitalType(req.Name, nil)
	}

	hospital := admin.Hospital{
		Name:    translation.Map{"en_US": req.Name},
		Address: translation.Map{"en_US": req.Address},
		City:    translation.Map{"en_US": req.City},
		Location: admin.GeoJSON{
			Type:        "Point",
			Coordinates: []float64{req.Coordinates.Lng, req.Coordinates.Lat},
		},
		AlwaysOpen:   req.AlwaysOpen,
		Services:     req.Services,
		Source:       admin.HospitalSourceAdmin,
		H3Cells:      admin.BuildH3Cells(req.Coordinates.Lng, req.Coordinates.Lat),
		HospitalType: hType,
		Category:     admin.HospitalCategoryFromType(hType),
		TypeLocked:   true,
	}
	if req.Services == nil {
		hospital.Services = []string{}
	}
	if req.Timing != nil {
		hospital.Timing = &admin.Timing{
			Start: req.Timing.Start,
			End:   req.Timing.End,
		}
	} else {
		hospital.Timing = &admin.Timing{Start: "12:00 AM", End: "11:59 PM"}
	}

	if err := h.HospitalStore.CreateHospital(r.Context(), &hospital); err != nil {
		response.Error(w, "Hospital add failed", http.StatusBadRequest)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminHospitalAdded, eventbus.AdminHospitalPayload{
		HospitalID: hospital.ID, Name: hospital.Name["en_US"], RequestID: reqID,
	})
	json.NewEncoder(w).Encode(map[string]string{"detail": "Hospital added successfully"})
}

func (h *AdminHandler) HandleUpdateHospital(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	body, err := io.ReadAll(r.Body)
	if err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	var req HospitalRequest
	if err := json.Unmarshal(body, &req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		response.Error(w, "ID is required", http.StatusBadRequest)
		return
	}
	if !ids.IsValid(req.ID) {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	// Conditional validation: only validate coordinates if they were actually sent
	if _, ok := raw["coordinates"]; ok {
		if req.Coordinates.Lat < -90 || req.Coordinates.Lat > 90 {
			response.Error(w, "Coordinates: lat must be -90..90", http.StatusBadRequest)
			return
		}
		if req.Coordinates.Lng < -180 || req.Coordinates.Lng > 180 {
			response.Error(w, "Coordinates: lng must be -180..180", http.StatusBadRequest)
			return
		}
	}

	existing, err := h.HospitalStore.FindByID(r.Context(), req.ID)
	if err != nil {
		response.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if existing == nil {
		response.Error(w, "Hospital not found", http.StatusNotFound)
		return
	}

	// Extended fields for full Hospital struct merge (covers PlaceID, Source, H3Cells, etc.)
	var ext struct {
		PlaceID      *string   `json:"place_id"`
		Source       *string   `json:"source"`
		H3Cells      *[]string `json:"h3_cells"`
		HospitalType *string   `json:"hospital_type"`
		Category     *string   `json:"category"`
		GoogleTypes  *[]string `json:"google_types"`
		TypeLocked   *bool     `json:"type_locked"`
	}
	_ = json.Unmarshal(body, &ext)

	// Also decode as Hospital to handle translation.Map and other types directly
	var incoming admin.Hospital
	_ = json.Unmarshal(body, &incoming)

	// Merge: only overwrite existing if incoming is non-zero / present
	if req.Name != "" {
		if existing.Name == nil {
			existing.Name = make(translation.Map)
		}
		existing.Name["en_US"] = req.Name
	} else if len(incoming.Name) > 0 {
		existing.Name = incoming.Name
	}
	if req.Address != "" {
		if existing.Address == nil {
			existing.Address = make(translation.Map)
		}
		existing.Address["en_US"] = req.Address
	} else if len(incoming.Address) > 0 {
		existing.Address = incoming.Address
	}
	if req.City != "" {
		if existing.City == nil {
			existing.City = make(translation.Map)
		}
		existing.City["en_US"] = req.City
	} else if len(incoming.City) > 0 {
		existing.City = incoming.City
	}
	if req.Coordinates.Lat != 0 || req.Coordinates.Lng != 0 {
		existing.Location = admin.GeoJSON{
			Type:        "Point",
			Coordinates: []float64{req.Coordinates.Lng, req.Coordinates.Lat},
		}
		existing.H3Cells = admin.BuildH3Cells(req.Coordinates.Lng, req.Coordinates.Lat)
	} else if len(incoming.Location.Coordinates) > 0 {
		existing.Location = incoming.Location
		if len(incoming.H3Cells) > 0 {
			existing.H3Cells = incoming.H3Cells
		} else {
			existing.H3Cells = admin.BuildH3Cells(incoming.Location.Coordinates[0], incoming.Location.Coordinates[1])
		}
	}
	if req.Timing != nil {
		existing.Timing = &admin.Timing{
			Start: req.Timing.Start,
			End:   req.Timing.End,
		}
	} else if incoming.Timing != nil {
		existing.Timing = incoming.Timing
	}
	if _, ok := raw["always_open"]; ok {
		existing.AlwaysOpen = req.AlwaysOpen
	} else if _, ok := raw["AlwaysOpen"]; ok {
		existing.AlwaysOpen = incoming.AlwaysOpen
	}
	if req.Services != nil {
		existing.Services = req.Services
	} else if len(incoming.Services) > 0 {
		existing.Services = incoming.Services
	} else if _, ok := raw["services"]; ok && incoming.Services != nil {
		// explicitly sent empty array -> clear
		existing.Services = incoming.Services
	}
	if req.HospitalType != "" {
		existing.HospitalType = req.HospitalType
		existing.Category = admin.HospitalCategoryFromType(req.HospitalType)
		existing.TypeLocked = true
	} else if ext.HospitalType != nil && *ext.HospitalType != "" {
		existing.HospitalType = *ext.HospitalType
		existing.Category = admin.HospitalCategoryFromType(*ext.HospitalType)
		existing.TypeLocked = true
	} else if incoming.HospitalType != "" {
		existing.HospitalType = incoming.HospitalType
		if incoming.Category != "" {
			existing.Category = incoming.Category
		} else {
			existing.Category = admin.HospitalCategoryFromType(incoming.HospitalType)
		}
		if incoming.TypeLocked {
			existing.TypeLocked = true
		}
	}
	if ext.PlaceID != nil && *ext.PlaceID != "" {
		existing.PlaceID = *ext.PlaceID
	} else if incoming.PlaceID != "" {
		existing.PlaceID = incoming.PlaceID
	}
	if ext.Source != nil && *ext.Source != "" {
		existing.Source = *ext.Source
	} else if incoming.Source != "" {
		existing.Source = incoming.Source
	}
	// H3Cells from ext only if coordinates not already handled
	if !(req.Coordinates.Lat != 0 || req.Coordinates.Lng != 0) && len(incoming.Location.Coordinates) == 0 {
		if ext.H3Cells != nil && len(*ext.H3Cells) > 0 {
			existing.H3Cells = *ext.H3Cells
		} else if len(incoming.H3Cells) > 0 {
			existing.H3Cells = incoming.H3Cells
		}
	}
	if ext.Category != nil && *ext.Category != "" {
		existing.Category = *ext.Category
	} else if incoming.Category != "" {
		existing.Category = incoming.Category
	}
	if ext.GoogleTypes != nil {
		if len(*ext.GoogleTypes) > 0 {
			existing.GoogleTypes = *ext.GoogleTypes
		} else if _, ok := raw["google_types"]; ok {
			existing.GoogleTypes = *ext.GoogleTypes
		}
	} else if len(incoming.GoogleTypes) > 0 {
		existing.GoogleTypes = incoming.GoogleTypes
	} else if _, ok := raw["google_types"]; ok && incoming.GoogleTypes != nil {
		existing.GoogleTypes = incoming.GoogleTypes
	}
	if ext.TypeLocked != nil {
		existing.TypeLocked = *ext.TypeLocked
	}

	if err := h.HospitalStore.UpdateHospital(r.Context(), existing); err != nil {
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

	if !ids.IsValid(req.HospitalID) {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if err := h.HospitalStore.DeleteHospital(r.Context(), req.HospitalID); err != nil {
		response.Error(w, "Hospital delete failed", http.StatusBadRequest)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminHospitalDeleted, eventbus.AdminHospitalPayload{
		HospitalID: req.HospitalID, RequestID: reqID,
	})
	json.NewEncoder(w).Encode(map[string]string{"detail": "Hospital deleted successfully"})
}

// -------------------------
// HOSPITAL SERVICE AREAS (cities)
// -------------------------

type HospitalCityRequest struct {
	ID              string   `json:"_id"`
	Name            string   `json:"name" validate:"required"`
	Lat             float64  `json:"lat" validate:"required,min=-90,max=90"`
	Lng             float64  `json:"lng" validate:"required,min=-180,max=180"`
	RadiusKM        float64  `json:"radius_km" validate:"required,min=1,max=60"`
	MaxPerCategory  *int     `json:"max_per_category" validate:"omitempty,min=5,max=60"`
	Enabled         bool     `json:"enabled"`
}

func (h *AdminHandler) HandleListHospitalCities(w http.ResponseWriter, r *http.Request) {
	list, err := h.HospitalCityStore.ListAll(r.Context())
	if err != nil {
		response.Error(w, "Failed to fetch list", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func (h *AdminHandler) HandleAddHospitalCity(w http.ResponseWriter, r *http.Request) {
	var req HospitalCityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	maxPerCategory := 40
	if req.MaxPerCategory != nil {
		maxPerCategory = *req.MaxPerCategory
	}
	if maxPerCategory < 5 {
		maxPerCategory = 5
	}
	if maxPerCategory > 60 {
		maxPerCategory = 60
	}
	city := &admin.HospitalCity{
		Name:           req.Name,
		Lat:            req.Lat,
		Lng:            req.Lng,
		RadiusM:        int64(req.RadiusKM * 1000),
		MaxPerCategory: maxPerCategory,
		Enabled:        req.Enabled,
	}
	if err := h.HospitalCityStore.Create(r.Context(), city); err != nil {
		response.Error(w, "City add failed", http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"detail": "Service area added successfully", "_id": city.ID})
}

func (h *AdminHandler) HandleUpdateHospitalCity(w http.ResponseWriter, r *http.Request) {
	var req HospitalCityRequest
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

	if !ids.IsValid(req.ID) {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	maxPerCategoryUpd := 40
	if req.MaxPerCategory != nil {
		maxPerCategoryUpd = *req.MaxPerCategory
	}
	if maxPerCategoryUpd < 5 {
		maxPerCategoryUpd = 5
	}
	if maxPerCategoryUpd > 60 {
		maxPerCategoryUpd = 60
	}
	city := &admin.HospitalCity{
		ID:             req.ID,
		Name:           req.Name,
		Lat:            req.Lat,
		Lng:            req.Lng,
		RadiusM:        int64(req.RadiusKM * 1000),
		MaxPerCategory: maxPerCategoryUpd,
		Enabled:        req.Enabled,
	}
	if err := h.HospitalCityStore.Update(r.Context(), city); err != nil {
		response.Error(w, "City update failed", http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"detail": "Service area updated successfully"})
}

func (h *AdminHandler) HandleDeleteHospitalCity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"_id" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	if !ids.IsValid(req.ID) {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if err := h.HospitalCityStore.Delete(r.Context(), req.ID); err != nil {
		response.Error(w, "City delete failed", http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"detail": "Service area deleted successfully"})
}

// -------------------------
// PENDING HOSPITALS (MD signup queue)
// -------------------------

func (h *AdminHandler) HandleListPendingHospitals(w http.ResponseWriter, r *http.Request) {
	list, err := h.PendingHospitalStore.ListPending(r.Context())
	if err != nil {
		response.Error(w, "Failed to fetch pending list", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func (h *AdminHandler) HandleFetchPendingHospital(w http.ResponseWriter, r *http.Request) {
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
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	p, err := h.PendingHospitalStore.FindByID(r.Context(), req.ID)
	if err != nil || p == nil {
		response.Error(w, "Pending hospital not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

func (h *AdminHandler) HandleApprovePendingHospital(w http.ResponseWriter, r *http.Request) {
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
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	pending, err := h.PendingHospitalStore.FindByID(r.Context(), req.ID)
	if err != nil || pending == nil {
		response.Error(w, "Pending hospital not found", http.StatusNotFound)
		return
	}
	if pending.Status != "pending" {
		response.Error(w, "Already processed", http.StatusBadRequest)
		return
	}
	// Create active hospital from pending details
	hType := admin.ClassifyHospitalType(pending.Name, nil)
	hospital := admin.Hospital{
		Name:         translation.Map{"en_US": pending.Name},
		Address:      translation.Map{"en_US": pending.Address},
		City:         translation.Map{"en_US": pending.City},
		Location:     admin.GeoJSON{Type: "Point", Coordinates: []float64{0, 0}},
		Timing:       &admin.Timing{Start: "12:00 AM", End: "11:59 PM"},
		AlwaysOpen:   true,
		Services:     []string{},
		Source:       admin.HospitalSourceAdmin,
		HospitalType: hType,
		Category:     admin.HospitalCategoryFromType(hType),
		TypeLocked:   true,
	}
	if pending.Location != nil && len(pending.Location.Coordinates) == 2 {
		hospital.Location = *pending.Location
		hospital.H3Cells = admin.BuildH3Cells(pending.Location.Coordinates[0], pending.Location.Coordinates[1])
	}
	if err := h.HospitalStore.CreateHospital(r.Context(), &hospital); err != nil {
		response.Error(w, "Failed to create hospital", http.StatusInternalServerError)
		return
	}
	_ = h.CounterStore.IncrementCounter(r.Context(), "hospitals")
	// Update pending status
	reviewerID, _ := r.Context().Value(middleware.UserIDKey).(string)
	_ = h.PendingHospitalStore.Approve(r.Context(), req.ID, reviewerID)

	// Link MD to hospital and activate
	if pending.MDID != "" {
		if ids.IsValid(pending.MDID) {
			md, _ := h.AuthStore.FindHospitalMDByID(r.Context(), pending.MDID)
			if md != nil {
				md.HospitalID = &hospital.ID
				md.Status = "active"
				_ = h.AuthStore.UpdateHospitalMD(r.Context(), md)
			}
		}
	}

	// Send approval email (no link, just where to login)
	if h.Mailer != nil && pending.Email != "" {
		_ = h.Mailer.SendHospitalApproveEmail(pending.Email, pending.Name, pending.City)
	} else {
		logger.Log.Warn().Str("pending_id", pending.ID).Msg("Approve email skipped: no email or mailer")
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Hospital approved", "hospital_id": hospital.ID})
}

func (h *AdminHandler) HandleRejectPendingHospital(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID           string `json:"id" validate:"required"`
		ErrorMessage string `json:"error_message" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}
	if !ids.IsValid(req.ID) {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	pending, err := h.PendingHospitalStore.FindByID(r.Context(), req.ID)
	if err != nil || pending == nil {
		response.Error(w, "Pending hospital not found", http.StatusNotFound)
		return
	}
	// Hybrid C: keep pending row as rejected for audit, delete MD to free mobile
	reviewerID, _ := r.Context().Value(middleware.UserIDKey).(string)
	if err := h.PendingHospitalStore.Reject(r.Context(), req.ID, reviewerID, req.ErrorMessage); err != nil {
		response.Error(w, "Reject failed", http.StatusInternalServerError)
		return
	}
	// Send rejection email (no link, just where to login and reason)
	if h.Mailer != nil && pending.Email != "" {
		_ = h.Mailer.SendHospitalRejectEmail(pending.Email, pending.Name, req.ErrorMessage)
	}
	if pending.MDID != "" && ids.IsValid(pending.MDID) {
		_ = h.AuthStore.DeleteHospitalMD(r.Context(), pending.MDID)
		_, _ = h.AuthStore.RevokeAllUserRefreshTokens(r.Context(), pending.MDID, "rejected")
		_ = h.AuthStore.ClearHospitalMDJWT(r.Context(), pending.MDID)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Hospital rejected"})
}

func (h *AdminHandler) HandlePendingHospitalCounter(w http.ResponseWriter, r *http.Request) {
	count, err := h.PendingHospitalStore.CountPending(r.Context())
	if err != nil {
		response.Error(w, "Failed to fetch counter", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int64{"count": count})
}

func (h *AdminHandler) HandleListHospitalMDs(w http.ResponseWriter, r *http.Request) {
	list, err := h.AuthStore.ListHospitalMDs(r.Context())
	if err != nil {
		response.Error(w, "Failed to fetch", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func (h *AdminHandler) HandleBanHospitalMD(w http.ResponseWriter, r *http.Request) {
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
	if err := h.AuthStore.BanHospitalMD(r.Context(), req.ID); err != nil {
		response.Error(w, "Ban failed", http.StatusInternalServerError)
		return
	}
	_, _ = h.AuthStore.RevokeAllUserRefreshTokens(r.Context(), req.ID, "banned")
	_ = h.AuthStore.ClearHospitalMDJWT(r.Context(), req.ID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "MD banned"})
}

func (h *AdminHandler) HandleUnbanHospitalMD(w http.ResponseWriter, r *http.Request) {
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
	if err := h.AuthStore.UnbanHospitalMD(r.Context(), req.ID); err != nil {
		response.Error(w, "Unban failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "MD unbanned"})
}

func (h *AdminHandler) HandleDeleteHospitalMD(w http.ResponseWriter, r *http.Request) {
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
	if err := h.AuthStore.DeleteHospitalMD(r.Context(), req.ID); err != nil {
		response.Error(w, "Delete failed", http.StatusInternalServerError)
		return
	}
	_, _ = h.AuthStore.RevokeAllUserRefreshTokens(r.Context(), req.ID, "deleted")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "MD login deleted, hospital retained"})
}
