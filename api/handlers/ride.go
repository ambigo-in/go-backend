package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/dispatch"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/payment"
	"ambigo-backend/internal/referral"
	"ambigo-backend/internal/requestid"
	"ambigo-backend/internal/pricing"
	"ambigo-backend/internal/ride"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type RideHandler struct {
	Dispatcher      *dispatch.Dispatcher
	EventBus        *eventbus.InMemoryBus
	PaymentStore    *payment.Store
	RazorpayService *payment.RazorpayService
	AuthStore       *auth.Store
	AdminStore      *admin.Store
	RouteClient     *dispatch.RouteClient
	PricingEngine   *pricing.Engine
	WalletStore     *payment.WalletStore
	ReferralService *referral.Service
}

func NewRideHandler(dispatcher *dispatch.Dispatcher, eventBus *eventbus.InMemoryBus, paymentStore *payment.Store, rzp *payment.RazorpayService, authStore *auth.Store, adminStore *admin.Store, routeClient *dispatch.RouteClient, walletStore *payment.WalletStore, referralService *referral.Service) *RideHandler {
	return &RideHandler{
		Dispatcher:      dispatcher,
		EventBus:        eventBus,
		PaymentStore:    paymentStore,
		RazorpayService: rzp,
		AuthStore:       authStore,
		AdminStore:      adminStore,
		RouteClient:     routeClient,
		PricingEngine:   pricing.NewEngine(),
		WalletStore:     walletStore,
		ReferralService: referralService,
	}
}

// upgradeUnvrfDriverRole checks if an unverified driver has been promoted to verified.
// If so, returns "driver" so they can query rides without re-logging in.
func (h *RideHandler) upgradeUnvrfDriverRole(uidStr, role string) string {
	if role != "unvrf_driver" {
		return role
	}
	objID, err := primitive.ObjectIDFromHex(uidStr)
	if err != nil {
		return role
	}
	found, err := h.AuthStore.FindDriverByID(context.Background(), objID)
	if err == nil && found != nil {
		return "driver"
	}
	return role
}

func (h *RideHandler) HandleRequestRide(w http.ResponseWriter, r *http.Request) {
	log := logger.Ctx(r.Context())

	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		PickupLat     float64 `json:"pickup_lat" validate:"required,min=-90,max=90"`
		PickupLng     float64 `json:"pickup_lng" validate:"required,min=-180,max=180"`
		DropoffLat    float64 `json:"dropoff_lat" validate:"required,min=-90,max=90"`
		DropoffLng    float64 `json:"dropoff_lng" validate:"required,min=-180,max=180"`
		AmbTypeID     string  `json:"amb_type_id"`
		HospitalID    string  `json:"hospital_id"`
		PickupAddress string  `json:"pickup_address" validate:"required"`
		DropAddress   string  `json:"drop_address" validate:"required"`
		PaymentMode   string  `json:"payment_mode" validate:"omitempty,oneof=cash online"`
		IsSOS         bool    `json:"is_sos"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	// Book Any: resolve to a type with nearby available drivers
	if req.AmbTypeID == "" {
		candidates, err := h.Dispatcher.Matcher.FindBestDrivers(r.Context(), req.PickupLat, req.PickupLng, 5, "")
		if err != nil || len(candidates) == 0 {
			availableTypes := h.Dispatcher.Matcher.FindAvailableOtherTypes(req.PickupLat, req.PickupLng, "")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":          http.StatusText(http.StatusNotFound),
				"detail":         "No ambulances available nearby",
				"code":           http.StatusNotFound,
				"available_types": availableTypes,
			})
			return
		}
		for _, candidate := range candidates {
			vType, err := h.Dispatcher.Matcher.LocStore.GetDriverVehicleType(candidate.DriverID)
			if err != nil || vType == "" {
				continue
			}
			if name, ok := h.Dispatcher.Matcher.AmbTypeNames[vType]; ok && (name == "Auto Riksha" || name == "Car Cab") {
				continue
			}
			req.AmbTypeID = vType
			break
		}
		if req.AmbTypeID == "" {
			availableTypes := h.Dispatcher.Matcher.FindAvailableOtherTypes(req.PickupLat, req.PickupLng, "")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":          http.StatusText(http.StatusNotFound),
				"detail":         "No eligible ambulances available nearby",
				"code":           http.StatusNotFound,
				"available_types": availableTypes,
			})
			return
		}
	}

	// Generate a 4-digit OTP for starting the ride
	otp := fmt.Sprintf("%04d", rand.Intn(10000))

	paymentMode := req.PaymentMode
	if paymentMode == "" {
		paymentMode = "cash"
	}
	priority := 0
	if req.IsSOS {
		priority = 10
	}

	newRide := &ride.Ride{
		UserID:            uidStr,
		AmbTypeID:         optionalString(req.AmbTypeID),
		HospitalID:        optionalString(req.HospitalID),
		StartOTP:          otp,
		PickupAddress:     req.PickupAddress,
		DropAddress:       req.DropAddress,
		EmergencyPriority: priority,
		PaymentMode:       paymentMode,
		Pickup: ride.GeoJSONPoint{
			Type:        "Point",
			Coordinates: []float64{req.PickupLng, req.PickupLat},
		},
		Drop: ride.GeoJSONPoint{
			Type:        "Point",
			Coordinates: []float64{req.DropoffLng, req.DropoffLat},
		},
	}

	// Compute distance server-side using Google Routes API
	route, err := h.RouteClient.CalculateETA(r.Context(), req.PickupLat, req.PickupLng, req.DropoffLat, req.DropoffLng)
	if err != nil {
		log.Error().Err(err).Msg("Failed to compute route")
	} else if route != nil {
		newRide.Route = route
	}

	// Calculate estimated fare upfront and lock it in
	if newRide.AmbTypeID == nil || *newRide.AmbTypeID == "" {
		log.Warn().Msg("Fare skipped: AmbTypeID is nil or empty")
	} else {
		ambType, err := h.AdminStore.GetAmbulanceTypeByID(r.Context(), *newRide.AmbTypeID)
		if err != nil {
			log.Error().Err(err).Msg("Fare skipped: GetAmbulanceTypeByID error")
		} else if ambType == nil {
			log.Warn().Str("amb_type_id", *newRide.AmbTypeID).Msg("Fare skipped: ambType not found")
		} else {
			distanceKm := 0.0
			if newRide.Route != nil {
				distanceKm = newRide.Route.DistanceKm
			}

			log.Debug().Float64("base_fare", ambType.BaseFare).Float64("driver_share", ambType.DriverShare).Int("tiers", len(ambType.PricingTier)).Float64("distance", distanceKm).Msg("Fare input")

			pricingTiers := make([]pricing.PricingTier, len(ambType.PricingTier))
			for i, t := range ambType.PricingTier {
				pricingTiers[i] = pricing.PricingTier{
					ThresholdDistance: t.ThresholdDistance,
					CostPerKm:         t.CostPerKm,
				}
			}

			// Calculate Total Fare
			base := h.PricingEngine.CalculateBaseAndDistanceFare(distanceKm, ambType.BaseFare, pricingTiers)
			emergency := h.PricingEngine.CalculateEmergencySurcharge(base, newRide.EmergencyPriority > 0)
			night := h.PricingEngine.CalculateNightSurcharge(base, time.Now())
			totalAmount := base + emergency + night
			totalAmount = float64(int(totalAmount*100)) / 100

			// Calculate Driver Share (DriverShare is a percentage of BaseFare)
			driverBaseFare := ambType.BaseFare * ambType.DriverShare / 100.0
			dBase := h.PricingEngine.CalculateBaseAndDistanceFare(distanceKm, driverBaseFare, pricingTiers)
			dEmergency := h.PricingEngine.CalculateEmergencySurcharge(dBase, newRide.EmergencyPriority > 0)
			dNight := h.PricingEngine.CalculateNightSurcharge(dBase, time.Now())
			driverShareTotal := dBase + dEmergency + dNight
			driverShareTotal = float64(int(driverShareTotal*100)) / 100

			log.Debug().Float64("total", totalAmount).Float64("driver_share", driverShareTotal).Msg("Fare computed")

			newRide.Fare = &ride.Fare{
				BaseFare:           ambType.BaseFare,
				DistanceFare:       base - ambType.BaseFare,
				EmergencySurcharge: emergency,
				NightSurcharge:     night,
				Total:              totalAmount,
				DriverShare:        driverShareTotal,
				Currency:           "INR",
			}
		}
	}

	// Reject if user already has an active ride
	existing, _ := h.Dispatcher.RideStore.GetCurrentRide(r.Context(), uidStr, "user")
	if existing != nil {
		response.Error(w, "You already have an active ride", http.StatusConflict)
		return
	}

	// This triggers the database creation and starts the matching loop
	if err := h.Dispatcher.RequestRide(r.Context(), newRide); err != nil {
		response.Error(w, "Failed to request ride: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Ride requested successfully",
		"ride_id": newRide.ID.Hex(),
		"otp":     otp, // Returning OTP so the user's app can display it
	})
}

func (h *RideHandler) HandleDriverAccept(w http.ResponseWriter, r *http.Request) {
	driverID, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	rideID := r.PathValue("id")

	// Send to dispatcher
	err := h.Dispatcher.HandleDriverAccept(r.Context(), rideID, driverID)
	if err != nil {
		response.Error(w, "Failed to accept ride: "+err.Error(), http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Ride accepted successfully",
	})
}

// ----------------------------------------------------------------------------
// PHASE 6: FULL RIDE LIFECYCLE ENDPOINTS
// ----------------------------------------------------------------------------

func (h *RideHandler) HandleArrive(w http.ResponseWriter, r *http.Request) {
	rideID := r.PathValue("id")
	callerID, _ := r.Context().Value(middleware.UserIDKey).(string)
	reqID := requestid.FromContext(r.Context())

	rideData, err := h.Dispatcher.RideStore.GetRideByID(r.Context(), rideID)
	if err != nil || rideData == nil {
		response.Error(w, "Ride not found", http.StatusNotFound)
		return
	}

	// A3: Only the assigned driver can advance the ride
	if rideData.DriverID == nil || callerID != *rideData.DriverID {
		response.Error(w, "Forbidden: you are not the assigned driver for this ride", http.StatusForbidden)
		return
	}

	if rideData.Status == ride.StatusArrived || rideData.Status == ride.StatusInProgress || rideData.Status == ride.StatusCompleted {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"detail": "Already arrived"})
		return
	}

	err = h.Dispatcher.RideStore.UpdateRideStatus(r.Context(), rideID, ride.StatusAssigned, ride.StatusArrived)
	if err != nil {
		response.Error(w, "Failed to arrive at pickup: "+err.Error(), http.StatusBadRequest)
		return
	}
	h.EventBus.PublishEvent(eventbus.ChannelRideArrived, eventbus.RideStatusChangedPayload{
		RideID: rideID, Status: string(ride.StatusArrived), RequestID: reqID,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Driver Arrived"})
}

func (h *RideHandler) HandleStart(w http.ResponseWriter, r *http.Request) {
	rideID := r.PathValue("id")
	callerID, _ := r.Context().Value(middleware.UserIDKey).(string)
	reqID := requestid.FromContext(r.Context())

	var req struct {
		OTP     string `json:"otp"`
		UserOTP string `json:"user_otp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	rideData, err := h.Dispatcher.RideStore.GetRideByID(r.Context(), rideID)
	if err != nil || rideData == nil {
		response.Error(w, "Ride not found", http.StatusNotFound)
		return
	}

	// A3/A8: Only the assigned driver can start the ride (even when OTP is disabled)
	if rideData.DriverID == nil || callerID != *rideData.DriverID {
		response.Error(w, "Forbidden: you are not the assigned driver for this ride", http.StatusForbidden)
		return
	}

	// Idempotent: already started
	if rideData.Status == ride.StatusInProgress {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"detail": "Ride already started"})
		return
	}
	if rideData.Status == ride.StatusCompleted || rideData.Status == ride.StatusCancelled {
		response.Error(w, "Ride is already completed or cancelled", http.StatusBadRequest)
		return
	}

	// Verify OTP if ambulance type requires it
	otpRequired := true
	if rideData.AmbTypeID != nil {
		ambType, err := h.AdminStore.GetAmbulanceTypeByID(r.Context(), *rideData.AmbTypeID)
		if err == nil && ambType != nil {
			otpRequired = ambType.OTPRequired
		}
	}
	if otpRequired {
		otp := req.OTP
		if otp == "" {
			otp = req.UserOTP
		}
		if otp == "" || rideData.StartOTP != otp {
			response.Error(w, "Invalid OTP", http.StatusBadRequest)
			return
		}
	}

	if rideData.Status == ride.StatusAssigned {
		_ = h.Dispatcher.RideStore.UpdateRideStatus(r.Context(), rideID, ride.StatusAssigned, ride.StatusArrived)
		rideData.Status = ride.StatusArrived
	}
	err = h.Dispatcher.RideStore.UpdateRideStatus(r.Context(), rideID, rideData.Status, ride.StatusInProgress)
	if err != nil {
		response.Error(w, "Failed to start ride: "+err.Error(), http.StatusBadRequest)
		return
	}
	h.EventBus.PublishEvent(eventbus.ChannelRideStarted, eventbus.RideStatusChangedPayload{
		RideID: rideID, Status: string(ride.StatusInProgress), RequestID: reqID,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Ride Started"})
}

func (h *RideHandler) HandleComplete(w http.ResponseWriter, r *http.Request) {
	rideID := r.PathValue("id")
	driverID, _ := r.Context().Value(middleware.UserIDKey).(string)
	reqID := requestid.FromContext(r.Context())

	var req struct {
		DropAddress string `json:"drop_address"`
		PaymentMode string `json:"payment_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if req.PaymentMode == "" {
		req.PaymentMode = "cash"
	}

	rideData, err := h.Dispatcher.RideStore.GetRideByID(r.Context(), rideID)
	if err != nil || rideData == nil {
		response.Error(w, "Ride not found", http.StatusNotFound)
		return
	}

	// A3: Only the assigned driver can complete the ride
	if rideData.DriverID == nil || driverID != *rideData.DriverID {
		response.Error(w, "Forbidden: you are not the assigned driver for this ride", http.StatusForbidden)
		return
	}

	err = h.Dispatcher.RideStore.UpdateRideStatus(r.Context(), rideID, ride.StatusInProgress, ride.StatusCompleted)
	if err != nil {
		response.Error(w, "Failed to complete ride: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Use the pre-calculated fare from when the ride was requested
	finalAmount := 0.0
	if rideData.Fare != nil && rideData.Fare.Total > 0 {
		finalAmount = rideData.Fare.Total
	} else if rideData.AmbTypeID != nil && *rideData.AmbTypeID != "" {
		// Fallback in case Fare was somehow not computed (e.g. old rides)
		ambType, err := h.AdminStore.GetAmbulanceTypeByID(r.Context(), *rideData.AmbTypeID)
		if err == nil && ambType != nil {
			distanceKm := 0.0
			if rideData.Route != nil {
				distanceKm = rideData.Route.DistanceKm
			}

			pricingTiers := make([]pricing.PricingTier, len(ambType.PricingTier))
			for i, t := range ambType.PricingTier {
				pricingTiers[i] = pricing.PricingTier{
					ThresholdDistance: t.ThresholdDistance,
					CostPerKm:         t.CostPerKm,
				}
			}

			base := h.PricingEngine.CalculateBaseAndDistanceFare(distanceKm, ambType.BaseFare, pricingTiers)
			emergency := h.PricingEngine.CalculateEmergencySurcharge(base, rideData.EmergencyPriority > 0)
			night := h.PricingEngine.CalculateNightSurcharge(base, time.Now())
			finalAmount = base + emergency + night
			finalAmount = float64(int(finalAmount*100)) / 100
		}
	}

	if finalAmount <= 0 {
		finalAmount = 50.0
	}

	paymentDesc := fmt.Sprintf("Charges for ride to %s", req.DropAddress)
	pmt := &payment.Payment{
		UserID:         rideData.UserID,
		PartnerID:      driverID,
		RideID:         rideID,
		Description:    paymentDesc,
		OriginalAmount: finalAmount,
		ChargedAmount:  finalAmount,
		PaymentMode:    payment.PaymentMode(req.PaymentMode),
		CreatedAt:      time.Now(),
	}

	if req.PaymentMode == "online" {
		orderID, err := h.RazorpayService.CreateOrder(finalAmount, rideID)
		if err == nil {
			pmt.RazorpayOrderID = &orderID
		}
	} else {
		pmt.Paid = true
		now := time.Now()
		pmt.PaidAt = &now
	}

	// Settle driver wallet
	driverObjID, _ := primitive.ObjectIDFromHex(driverID)
	if rideData.Fare != nil && driverObjID.Hex() != "" {
		if req.PaymentMode == "online" {
			h.WalletStore.UpdateWalletBalance(r.Context(), driverObjID, rideData.Fare.DriverShare)
		} else {
			commission := rideData.Fare.Total - rideData.Fare.DriverShare
			if commission > 0 {
				h.WalletStore.UpdateWalletBalance(r.Context(), driverObjID, -commission)
			}
		}
	}

	h.PaymentStore.CreatePayment(r.Context(), pmt)

	h.EventBus.PublishEvent(eventbus.ChannelRideCompleted, eventbus.RideCompletedPayload{
		RideID:      rideID,
		DriverID:    driverID,
		UserID:      rideData.UserID,
		PaymentMode: req.PaymentMode,
		FinalAmount: finalAmount,
		DriverShare: func() float64 {
			if rideData.Fare != nil {
				return rideData.Fare.DriverShare
			}
			return 0
		}(),
		DropAddress: req.DropAddress,
		RequestID:   reqID,
	})

	// V20: Process referral ride completion
	if h.ReferralService != nil {
		go h.ReferralService.ProcessRideCompletion(context.Background(), rideData.UserID, driverID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"detail":            "Ride Completed",
		"payment_id":        pmt.ID.Hex(),
		"razorpay_order_id": pmt.RazorpayOrderID,
		"take_cash":         req.PaymentMode != "online",
		"amount":            finalAmount,
	})
}

func (h *RideHandler) HandleCancel(w http.ResponseWriter, r *http.Request) {
	rideID := r.PathValue("id")
	callerID, _ := r.Context().Value(middleware.UserIDKey).(string)
	callerRole, _ := r.Context().Value(middleware.UserRoleKey).(string)
	reqID := requestid.FromContext(r.Context())

	rideData, err := h.Dispatcher.RideStore.GetRideByID(r.Context(), rideID)
	if err != nil || rideData == nil {
		response.Error(w, "Ride not found", http.StatusNotFound)
		return
	}

	// Verify ownership: only the ride's user or assigned driver can cancel
	if callerID != rideData.UserID && (rideData.DriverID == nil || callerID != *rideData.DriverID) {
		response.Error(w, "Forbidden: you do not own this ride", http.StatusForbidden)
		return
	}

	if rideData.Status == ride.StatusCancelled || rideData.Status == ride.StatusCompleted {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"detail": "Ride already cancelled"})
		return
	}

	cancelReason := ""
	switch {
	case callerRole == "driver":
		cancelReason = "driver_cancelled"
	case rideData.DriverID == nil || *rideData.DriverID == "":
		cancelReason = "user_cancelled_before_assignment"
	default:
		cancelReason = "user_cancelled"
	}
	err = h.Dispatcher.RideStore.CancelRide(r.Context(), rideID, rideData.Status, cancelReason)
	if err != nil {
		response.Error(w, "Failed to cancel ride: "+err.Error(), http.StatusBadRequest)
		return
	}
	h.EventBus.PublishEvent(eventbus.ChannelRideCancelled, eventbus.RideCancelledPayload{
		RideID:    rideID,
		Reason:    cancelReason,
		UserID:    rideData.UserID,
		DriverID: func() string {
			if rideData.DriverID != nil {
				return *rideData.DriverID
			}
			return ""
		}(),
		RequestID: reqID,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Ride Cancelled"})
}

func (h *RideHandler) HandleGetHistory(w http.ResponseWriter, r *http.Request) {
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
	role = h.upgradeUnvrfDriverRole(uidStr, role)

	limit, _ := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 64)
	if limit <= 0 {
		limit = 10
	}
	skip, _ := strconv.ParseInt(r.URL.Query().Get("skip"), 10, 64)

	rides, err := h.Dispatcher.RideStore.GetRideHistory(r.Context(), uidStr, role, limit, skip)
	if err != nil {
		response.Error(w, "Failed to fetch history: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rides)
}

func (h *RideHandler) HandleGetCurrentRide(w http.ResponseWriter, r *http.Request) {
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
	role = h.upgradeUnvrfDriverRole(uidStr, role)

	var req struct {
		RideID string `json:"ride_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	var rideData *ride.Ride
	var err error

	if req.RideID != "" {
		rideData, err = h.Dispatcher.RideStore.GetRideByID(r.Context(), req.RideID)
	} else {
		rideData, err = h.Dispatcher.RideStore.GetCurrentRide(r.Context(), uidStr, role)
	}

	if err != nil {
		response.Error(w, "Failed to fetch ride: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if rideData == nil {
		w.Write([]byte(`{"found": false}`))
		return
	}

	// Flutter expects legacy status strings (searching_rides, accepted_rides, ongoing_rides)
	statusStr := string(rideData.Status)
	if rideData.Status == ride.StatusSearching {
		statusStr = "searching_rides"
	} else if rideData.Status == ride.StatusAssigned || rideData.Status == ride.StatusArrived {
		statusStr = "accepted_rides"
	} else if rideData.Status == ride.StatusInProgress {
		statusStr = "ongoing_rides"
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"found":  true,
		"status": statusStr,
		"data":   rideData,
	})
}

// HandleGetDriverDetails is used by the User App to get the driver's info
func (h *RideHandler) HandleGetDriverDetails(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RideID string `json:"ride_id" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	callerID, _ := r.Context().Value(middleware.UserIDKey).(string)
	callerRole, _ := r.Context().Value(middleware.UserRoleKey).(string)

	rideData, err := h.Dispatcher.RideStore.GetRideByID(r.Context(), req.RideID)
	if err != nil || rideData == nil {
		response.Error(w, "Ride not found", http.StatusNotFound)
		return
	}

	// A2: Only the ride's user, driver, or admin can view driver PII
	if callerRole != "admin" && callerID != rideData.UserID && (rideData.DriverID == nil || callerID != *rideData.DriverID) {
		response.Error(w, "Forbidden: you do not own this ride", http.StatusForbidden)
		return
	}

	if rideData.DriverID == nil {
		response.Error(w, "No driver assigned yet", http.StatusNotFound)
		return
	}

	objID, err := primitive.ObjectIDFromHex(*rideData.DriverID)
	if err != nil {
		response.Error(w, "Invalid driver ID", http.StatusBadRequest)
		return
	}

	driver, err := h.AuthStore.FindDriverByID(r.Context(), objID)
	if err != nil || driver == nil {
		response.Error(w, "Driver not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(driver)
}

// HandleGetUserDetails is used by the Driver App to get the user's info
func (h *RideHandler) HandleGetUserDetails(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RideID string `json:"ride_id" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	callerID, _ := r.Context().Value(middleware.UserIDKey).(string)
	callerRole, _ := r.Context().Value(middleware.UserRoleKey).(string)

	rideData, err := h.Dispatcher.RideStore.GetRideByID(r.Context(), req.RideID)
	if err != nil || rideData == nil {
		response.Error(w, "Ride not found", http.StatusNotFound)
		return
	}

	// A2: Only the ride's user, driver, or admin can view user PII
	if callerRole != "admin" && callerID != rideData.UserID && (rideData.DriverID == nil || callerID != *rideData.DriverID) {
		response.Error(w, "Forbidden: you do not own this ride", http.StatusForbidden)
		return
	}

	objID, err := primitive.ObjectIDFromHex(rideData.UserID)
	if err != nil {
		response.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	user, err := h.AuthStore.FindUserByID(r.Context(), objID)
	if err != nil || user == nil {
		response.Error(w, "User not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

func (h *RideHandler) HandleRoutePreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OriginLat  float64 `json:"origin_lat" validate:"required,min=-90,max=90"`
		OriginLng  float64 `json:"origin_lng" validate:"required,min=-180,max=180"`
		DestLat    float64 `json:"dest_lat" validate:"required,min=-90,max=90"`
		DestLng    float64 `json:"dest_lng" validate:"required,min=-180,max=180"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	route, err := h.RouteClient.CalculateETA(r.Context(), req.OriginLat, req.OriginLng, req.DestLat, req.DestLng)
	if err != nil {
		response.Error(w, "Failed to compute route: "+err.Error(), http.StatusInternalServerError)
		return
	}

	coords := ride.DecodePolyline(route.Polyline)
	if len(coords) == 0 {
		coords = [][2]float64{{req.OriginLat, req.OriginLng}, {req.DestLat, req.DestLng}}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"distance_km":      route.DistanceKm,
		"duration_seconds": route.DurationSeconds,
		"polyline_coords":  coords,
	})
}

func (h *RideHandler) HandleFareEstimate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DistanceKm float64 `json:"distance_km" validate:"required,gt=0"`
		AmbTypeID  string  `json:"amb_type_id"`
		IsSOS      bool    `json:"is_sos"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	// If amb_type_id is provided, estimate for that type only
	if req.AmbTypeID != "" {
		ambType, err := h.AdminStore.GetAmbulanceTypeByID(r.Context(), req.AmbTypeID)
		if err != nil || ambType == nil {
			response.Error(w, "Ambulance type not found", http.StatusNotFound)
			return
		}

		pricingTiers := make([]pricing.PricingTier, len(ambType.PricingTier))
		for i, t := range ambType.PricingTier {
			pricingTiers[i] = pricing.PricingTier{
				ThresholdDistance: t.ThresholdDistance,
				CostPerKm:         t.CostPerKm,
			}
		}

		base := h.PricingEngine.CalculateBaseAndDistanceFare(req.DistanceKm, ambType.BaseFare, pricingTiers)
		emergency := h.PricingEngine.CalculateEmergencySurcharge(base, req.IsSOS)
		night := h.PricingEngine.CalculateNightSurcharge(base, time.Now())
		total := float64(int((base+emergency+night)*100)) / 100

		driverBaseFare := ambType.BaseFare * ambType.DriverShare / 100.0
		dBase := h.PricingEngine.CalculateBaseAndDistanceFare(req.DistanceKm, driverBaseFare, pricingTiers)
		dEmergency := h.PricingEngine.CalculateEmergencySurcharge(dBase, req.IsSOS)
		dNight := h.PricingEngine.CalculateNightSurcharge(dBase, time.Now())
		driverShare := float64(int((dBase+dEmergency+dNight)*100)) / 100

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"estimates": []map[string]interface{}{
				{
					"amb_type_id":  ambType.ID.Hex(),
					"name":         ambType.Name,
					"base_fare":    ambType.BaseFare,
					"total":        total,
					"driver_share": driverShare,
				},
			},
		})
		return
	}

	// No amb_type_id — estimate for all types
	allTypes, err := h.AdminStore.ListAmbulanceTypes(r.Context())
	if err != nil {
		response.Error(w, "Failed to load ambulance types", http.StatusInternalServerError)
		return
	}

	type estimate struct {
		AmbTypeID   string  `json:"amb_type_id"`
		Name        string  `json:"name"`
		BaseFare    float64 `json:"base_fare"`
		Total       float64 `json:"total"`
		DriverShare float64 `json:"driver_share"`
	}
	estimates := make([]estimate, 0, len(allTypes))
	for _, ambType := range allTypes {
		pricingTiers := make([]pricing.PricingTier, len(ambType.PricingTier))
		for i, t := range ambType.PricingTier {
			pricingTiers[i] = pricing.PricingTier{
				ThresholdDistance: t.ThresholdDistance,
				CostPerKm:         t.CostPerKm,
			}
		}

		base := h.PricingEngine.CalculateBaseAndDistanceFare(req.DistanceKm, ambType.BaseFare, pricingTiers)
		emergency := h.PricingEngine.CalculateEmergencySurcharge(base, req.IsSOS)
		night := h.PricingEngine.CalculateNightSurcharge(base, time.Now())
		total := float64(int((base+emergency+night)*100)) / 100

		driverBaseFare := ambType.BaseFare * ambType.DriverShare / 100.0
		dBase := h.PricingEngine.CalculateBaseAndDistanceFare(req.DistanceKm, driverBaseFare, pricingTiers)
		dEmergency := h.PricingEngine.CalculateEmergencySurcharge(dBase, req.IsSOS)
		dNight := h.PricingEngine.CalculateNightSurcharge(dBase, time.Now())
		driverShare := float64(int((dBase+dEmergency+dNight)*100)) / 100

		estimates = append(estimates, estimate{
			AmbTypeID:   ambType.ID.Hex(),
			Name:        ambType.Name,
			BaseFare:    ambType.BaseFare,
			Total:       total,
			DriverShare: driverShare,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"estimates": estimates,
	})
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
