package handlers

import (
	"context"
	"encoding/json"
	"errors"
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
	"ambigo-backend/internal/ids"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/payment"
	"ambigo-backend/internal/pricing"
	"ambigo-backend/internal/referral"
	"ambigo-backend/internal/requestid"
	"ambigo-backend/internal/ride"

	"github.com/jackc/pgx/v5"
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
	if !ids.IsValid(uidStr) {
		return role
	}
	found, err := h.AuthStore.FindDriverByID(context.Background(), uidStr)
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
			totalAmount = payment.RoundRupees(totalAmount)

			// Calculate Driver Share (DriverShare is a percentage of BaseFare)
			driverBaseFare := ambType.BaseFare * ambType.DriverShare / 100.0
			dBase := h.PricingEngine.CalculateBaseAndDistanceFare(distanceKm, driverBaseFare, pricingTiers)
			dEmergency := h.PricingEngine.CalculateEmergencySurcharge(dBase, newRide.EmergencyPriority > 0)
			dNight := h.PricingEngine.CalculateNightSurcharge(dBase, time.Now())
			driverShareTotal := dBase + dEmergency + dNight
			driverShareTotal = payment.RoundRupees(driverShareTotal)

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
		"ride_id": newRide.ID,
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
		RideID: rideID, UserID: rideData.UserID, Status: string(ride.StatusArrived), RequestID: reqID,
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
		RideID: rideID, UserID: rideData.UserID, Status: string(ride.StatusInProgress), RequestID: reqID,
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
			finalAmount = payment.RoundRupees(finalAmount)
		}
	}

	if finalAmount <= 0 {
		logger.Log.Error().Str("ride_id", rideID).Str("request_id", reqID).Msg("Ride completed with non-positive fare and no fallback available")
		response.Error(w, "Cannot complete ride: fare could not be determined", http.StatusUnprocessableEntity)
		return
	}

	// Apply referral credit discount. The claim is atomic (single-statement
	// DELETE...RETURNING): concurrent completions cannot spend the same
	// credit twice. Claimed value is restored on every failure below, so the
	// user never loses credit for a ride that did not complete.
	referralDiscount := 0.0
	if h.ReferralService != nil {
		discount, err := h.ReferralService.ConsumeUserReferralCredit(r.Context(), rideData.UserID)
		if err == nil {
			referralDiscount = discount
		} else {
			logger.Log.Error().Err(err).Str("user_id", rideData.UserID).Str("request_id", reqID).Msg("Failed to consume referral credit")
		}
	}
	restoreDiscount := func() {
		if h.ReferralService != nil && referralDiscount > 0 {
			h.ReferralService.RestoreUserReferralCredit(r.Context(), rideData.UserID, referralDiscount)
		}
	}

	userAmount := finalAmount - referralDiscount
	if userAmount < 0 {
		userAmount = 0
	}

	paymentDesc := fmt.Sprintf("Charges for ride to %s", req.DropAddress)
	driverShare := 0.0
	if rideData.Fare != nil {
		driverShare = rideData.Fare.DriverShare
	}
	pmt := &payment.Payment{
		UserID:         rideData.UserID,
		PartnerID:      driverID,
		RideID:         rideID,
		Description:    paymentDesc,
		OriginalAmount: finalAmount,
		ChargedAmount:  userAmount,
		DriverShare:    driverShare,
		PaymentMode:    payment.PaymentMode(req.PaymentMode),
		CreatedAt:      time.Now(),
	}

	// Idempotency: a retried complete (client timeout after commit) must not
	// mint a second bill. If a bill already exists, the discount (if any) was
	// consumed for nothing — restore it before answering the conflict.
	if existing, err := h.PaymentStore.FindPaymentByRideID(r.Context(), rideID); err != nil {
		logger.Log.Error().Err(err).Str("ride_id", rideID).Str("request_id", reqID).Msg("Duplicate-payment check failed")
		restoreDiscount()
		response.Error(w, "Failed to complete ride: "+err.Error(), http.StatusInternalServerError)
		return
	} else if existing != nil {
		restoreDiscount()
		response.Error(w, "Ride already has a payment", http.StatusConflict)
		return
	}

	// Validate payment mode early: anything else hits the DB CHECK as a 500.
	if req.PaymentMode != "cash" && req.PaymentMode != "online" {
		restoreDiscount()
		response.Error(w, "Invalid payment mode (must be cash or online)", http.StatusBadRequest)
		return
	}

	if req.PaymentMode == "online" {
		if userAmount <= 0 {
			// Fully discounted: nothing to collect. Settle immediately as
			// paid so the bill never sticks unpaid; the platform absorbs
			// the driver share (same rule as referral discounts).
			pmt.Paid = true
			now := time.Now()
			pmt.PaidAt = &now
		} else {
			orderID, err := h.RazorpayService.CreateOrder(userAmount, rideID)
			if err != nil {
				logger.Log.Error().Err(err).Str("ride_id", rideID).Str("request_id", reqID).Msg("Razorpay order creation failed; ride not completed")
				restoreDiscount()
				response.Error(w, "Failed to create online payment order, please retry", http.StatusBadGateway)
				return
			}
			pmt.RazorpayOrderID = &orderID
		}
	} else {
		pmt.Paid = true
		now := time.Now()
		pmt.PaidAt = &now
	}

	// Prepare fare update payload if referral discount applied
	var fareToUpdate *ride.Fare
	if referralDiscount > 0 && rideData.Fare != nil {
		rideData.Fare.ReferralDiscount = referralDiscount
		rideData.Fare.Total = userAmount
		fareToUpdate = rideData.Fare
	}

	// Atomic Saga: ride status + fare + payment + wallet commission in single Tx
	// Uses payment.WithTx with rideStore.WithTx / paymentStore.WithTx / walletStore.WithTx per 03 §6 and SYSTEM_DESIGN_AUDIT §4.2
	// Pool is shared (single Postgres DB), so any store's pool is valid; use PaymentStore.Pool()
	if err := payment.WithTx(r.Context(), h.Dispatcher.RideStore.Pool(), func(tx pgx.Tx) error {
		rTx := h.Dispatcher.RideStore.WithTx(tx)
		sTx := h.PaymentStore.WithTx(tx)
		wTx := h.WalletStore.WithTx(tx)
		if err := rTx.UpdateRideStatus(r.Context(), rideID, ride.StatusInProgress, ride.StatusCompleted); err != nil {
			return err
		}
		if fareToUpdate != nil {
			if err := rTx.UpdateRideFare(r.Context(), rideID, fareToUpdate); err != nil {
				return err
			}
		}
		if err := sTx.CreatePayment(r.Context(), pmt); err != nil {
			return err
		}
		if req.PaymentMode != "online" {
			commission := pmt.OriginalAmount - pmt.DriverShare
			if commission > 0 {
				if !ids.IsValid(driverID) {
					return errors.New("invalid driver for commission debit")
				}
				var exists bool
				if qerr := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM drivers WHERE id=$1)`, driverID).Scan(&exists); qerr != nil {
					return qerr
				}
				if !exists {
					return errors.New("driver not found for commission debit")
				}
				if err := wTx.UpdateWalletBalance(r.Context(), driverID, -commission); err != nil {
					return err
				}
				bal, berr := wTx.GetBalance(r.Context(), driverID)
				if berr != nil {
					return berr
				}
				if err := wTx.InsertLedgerEntry(r.Context(), &payment.WalletTransaction{
					DriverID:    driverID,
					Amount:      commission,
					Direction:   "debit",
					TxnType:     "commission_debit",
					ReferenceID: pmt.ID,
					Status:      "success",
					BalanceAfter: &bal,
				}); err != nil {
					return err
				}
			}
		} else if pmt.Paid && pmt.DriverShare > 0 {
			// Fully-discounted online ride settled above: credit the driver
			// share now (platform absorbs), with a ledger row.
			if !ids.IsValid(driverID) {
				return errors.New("invalid driver for zero-amount credit")
			}
			if err := wTx.UpdateWalletBalance(r.Context(), driverID, pmt.DriverShare); err != nil {
				return err
			}
			bal, berr := wTx.GetBalance(r.Context(), driverID)
			if berr != nil {
				return berr
			}
			if err := wTx.InsertLedgerEntry(r.Context(), &payment.WalletTransaction{
				DriverID:    driverID,
				Amount:      pmt.DriverShare,
				Direction:   "credit",
				TxnType:     "ride_credit",
				ReferenceID: pmt.ID,
				Status:      "success",
				BalanceAfter: &bal,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		logger.Log.Error().Err(err).Str("ride_id", rideID).Str("request_id", reqID).Msg("Failed to complete ride transaction")
		restoreDiscount()
		response.Error(w, "Failed to complete ride: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelRideCompleted, eventbus.RideCompletedPayload{
		RideID:      rideID,
		DriverID:    driverID,
		UserID:      rideData.UserID,
		PaymentMode: req.PaymentMode,
		FinalAmount: userAmount,
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
		"payment_id":        pmt.ID,
		"razorpay_order_id": pmt.RazorpayOrderID,
		"take_cash":         req.PaymentMode != "online",
		"amount":            userAmount,
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

	if !ids.IsValid(*rideData.DriverID) {
		response.Error(w, "Invalid driver ID", http.StatusBadRequest)
		return
	}

	driver, err := h.AuthStore.FindDriverByID(r.Context(), *rideData.DriverID)
	if err != nil || driver == nil {
		response.Error(w, "Driver not found", http.StatusNotFound)
		return
	}

	// Resolve vehicle_type ObjectId to the actual type name
	if driver.VehicleType != "" {
		if ambType, err := h.AdminStore.GetAmbulanceTypeByID(r.Context(), driver.VehicleType); err == nil && ambType != nil {
			driver.VehicleType = ambType.Name
		}
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

	if !ids.IsValid(rideData.UserID) {
		response.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	user, err := h.AuthStore.FindUserByID(r.Context(), rideData.UserID)
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
		total := payment.RoundRupees(base+emergency+night)

		driverBaseFare := ambType.BaseFare * ambType.DriverShare / 100.0
		dBase := h.PricingEngine.CalculateBaseAndDistanceFare(req.DistanceKm, driverBaseFare, pricingTiers)
		dEmergency := h.PricingEngine.CalculateEmergencySurcharge(dBase, req.IsSOS)
		dNight := h.PricingEngine.CalculateNightSurcharge(dBase, time.Now())
		driverShare := payment.RoundRupees(dBase+dEmergency+dNight)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"estimates": []map[string]interface{}{
				{
					"amb_type_id":  ambType.ID,
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
		total := payment.RoundRupees(base+emergency+night)

		driverBaseFare := ambType.BaseFare * ambType.DriverShare / 100.0
		dBase := h.PricingEngine.CalculateBaseAndDistanceFare(req.DistanceKm, driverBaseFare, pricingTiers)
		dEmergency := h.PricingEngine.CalculateEmergencySurcharge(dBase, req.IsSOS)
		dNight := h.PricingEngine.CalculateNightSurcharge(dBase, time.Now())
		driverShare := payment.RoundRupees(dBase+dEmergency+dNight)

		estimates = append(estimates, estimate{
			AmbTypeID:   ambType.ID,
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
