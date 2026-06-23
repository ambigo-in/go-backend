package handlers

import (
	"ambigo-backend/api/middleware"
	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/dispatch"
	"ambigo-backend/internal/metrics"
	"ambigo-backend/internal/payment"
	"ambigo-backend/internal/pricing"
	"ambigo-backend/internal/ride"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type RideHandler struct {
	Dispatcher      *dispatch.Dispatcher
	PaymentStore    *payment.Store
	RazorpayService *payment.RazorpayService
	AuthStore       *auth.Store
	AdminStore      *admin.Store
	RouteClient     *dispatch.RouteClient
	PricingEngine   *pricing.Engine
	WalletStore     *payment.WalletStore
}

func NewRideHandler(dispatcher *dispatch.Dispatcher, paymentStore *payment.Store, rzp *payment.RazorpayService, authStore *auth.Store, adminStore *admin.Store, routeClient *dispatch.RouteClient, walletStore *payment.WalletStore) *RideHandler {
	return &RideHandler{
		Dispatcher:      dispatcher,
		PaymentStore:    paymentStore,
		RazorpayService: rzp,
		AuthStore:       authStore,
		AdminStore:      adminStore,
		RouteClient:     routeClient,
		PricingEngine:   pricing.NewEngine(),
		WalletStore:     walletStore,
	}
}

func (h *RideHandler) HandleRequestRide(w http.ResponseWriter, r *http.Request) {
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		PickupLat     float64 `json:"pickup_lat"`
		PickupLng     float64 `json:"pickup_lng"`
		DropoffLat    float64 `json:"dropoff_lat"`
		DropoffLng    float64 `json:"dropoff_lng"`
		AmbTypeID     string  `json:"amb_type_id"`
		HospitalID    string  `json:"hospital_id"`
		PickupAddress string  `json:"pickup_address"`
		DropAddress   string  `json:"drop_address"`
		PaymentMode   string  `json:"payment_mode"`
		IsSOS         bool    `json:"is_sos"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
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

	metrics.RideRequestsTotal.Inc()

	// Compute distance server-side using Google Routes API
	route, err := h.RouteClient.CalculateETA(r.Context(), req.PickupLat, req.PickupLng, req.DropoffLat, req.DropoffLng)
	if err != nil {
		log.Printf("[RideHandler] Failed to compute route: %v", err)
	} else if route != nil {
		newRide.Route = route
	}

	// Calculate estimated fare upfront and lock it in
	if newRide.AmbTypeID == nil || *newRide.AmbTypeID == "" {
		log.Printf("[RideHandler] Fare skipped: AmbTypeID is nil or empty")
	} else {
		ambType, err := h.AdminStore.GetAmbulanceTypeByID(r.Context(), *newRide.AmbTypeID)
		if err != nil {
			log.Printf("[RideHandler] Fare skipped: GetAmbulanceTypeByID error: %v", err)
		} else if ambType == nil {
			log.Printf("[RideHandler] Fare skipped: ambType not found for ID %s", *newRide.AmbTypeID)
		} else {
			distanceKm := 0.0
			if newRide.Route != nil {
				distanceKm = newRide.Route.DistanceKm
			}

			log.Printf("[RideHandler] Fare input: baseFare=%.2f, driverShare=%.2f, tiers=%d, distance=%.3f",
				ambType.BaseFare, ambType.DriverShare, len(ambType.PricingTier), distanceKm)

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

			// Calculate Driver Share
			dBase := h.PricingEngine.CalculateBaseAndDistanceFare(distanceKm, ambType.DriverShare, pricingTiers)
			dEmergency := h.PricingEngine.CalculateEmergencySurcharge(dBase, newRide.EmergencyPriority > 0)
			dNight := h.PricingEngine.CalculateNightSurcharge(dBase, time.Now())
			driverShareTotal := dBase + dEmergency + dNight
			driverShareTotal = float64(int(driverShareTotal*100)) / 100

			log.Printf("[RideHandler] Fare computed: total=%.2f, driverShare=%.2f", totalAmount, driverShareTotal)

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

	// This triggers the database creation and starts the matching loop
	if err := h.Dispatcher.RequestRide(newRide); err != nil {
		http.Error(w, "Failed to request ride: "+err.Error(), http.StatusInternalServerError)
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
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	rideID := r.PathValue("id")

	// Send to dispatcher
	err := h.Dispatcher.HandleDriverAccept(r.Context(), rideID, driverID)
	if err != nil {
		http.Error(w, "Failed to accept ride: "+err.Error(), http.StatusConflict)
		return
	}

	metrics.RidesAssignedTotal.Inc()

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

	rideData, err := h.Dispatcher.RideStore.GetRideByID(r.Context(), rideID)
	if err != nil || rideData == nil {
		http.Error(w, "Ride not found", http.StatusNotFound)
		return
	}

	if rideData.Status == ride.StatusArrived || rideData.Status == ride.StatusInProgress || rideData.Status == ride.StatusCompleted {
		json.NewEncoder(w).Encode(map[string]string{"detail": "Already arrived"})
		return
	}

	err = h.Dispatcher.RideStore.UpdateRideStatus(r.Context(), rideID, ride.StatusAssigned, ride.StatusArrived)
	if err != nil {
		http.Error(w, "Failed to arrive at pickup: "+err.Error(), http.StatusBadRequest)
		return
	}
	h.Dispatcher.WSManager.SendToRideWatchers(rideID, "RIDE_UPDATE", map[string]string{"ride_id": rideID, "status": string(ride.StatusArrived)})
	json.NewEncoder(w).Encode(map[string]string{"detail": "Driver Arrived"})
}

func (h *RideHandler) HandleStart(w http.ResponseWriter, r *http.Request) {
	rideID := r.PathValue("id")

	var req struct {
		OTP     string `json:"otp"`
		UserOTP string `json:"user_otp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	rideData, err := h.Dispatcher.RideStore.GetRideByID(r.Context(), rideID)
	if err != nil || rideData == nil {
		http.Error(w, "Ride not found", http.StatusNotFound)
		return
	}

	// Idempotent: already started
	if rideData.Status == ride.StatusInProgress {
		json.NewEncoder(w).Encode(map[string]string{"detail": "Ride already started"})
		return
	}
	if rideData.Status == ride.StatusCompleted || rideData.Status == ride.StatusCancelled {
		http.Error(w, "Ride is already completed or cancelled", http.StatusBadRequest)
		return
	}

	// Verify OTP
	otp := req.OTP
	if otp == "" {
		otp = req.UserOTP
	}
	if otp != "" && rideData.StartOTP != otp {
		http.Error(w, "Invalid OTP", http.StatusBadRequest)
		return
	}

	if rideData.Status == ride.StatusAssigned {
		_ = h.Dispatcher.RideStore.UpdateRideStatus(r.Context(), rideID, ride.StatusAssigned, ride.StatusArrived)
		rideData.Status = ride.StatusArrived
	}
	err = h.Dispatcher.RideStore.UpdateRideStatus(r.Context(), rideID, rideData.Status, ride.StatusInProgress)
	if err != nil {
		http.Error(w, "Failed to start ride: "+err.Error(), http.StatusBadRequest)
		return
	}
	h.Dispatcher.WSManager.SendToRideWatchers(rideID, "RIDE_UPDATE", map[string]string{"ride_id": rideID, "status": string(ride.StatusInProgress)})
	json.NewEncoder(w).Encode(map[string]string{"detail": "Ride Started"})
}

func (h *RideHandler) HandleComplete(w http.ResponseWriter, r *http.Request) {
	rideID := r.PathValue("id")
	driverID, _ := r.Context().Value(middleware.UserIDKey).(string)

	var req struct {
		DropAddress string `json:"drop_address"`
		PaymentMode string `json:"payment_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if req.PaymentMode == "" {
		req.PaymentMode = "cash"
	}

	rideData, err := h.Dispatcher.RideStore.GetRideByID(r.Context(), rideID)
	if err != nil || rideData == nil {
		http.Error(w, "Ride not found", http.StatusNotFound)
		return
	}

	err = h.Dispatcher.RideStore.UpdateRideStatus(r.Context(), rideID, ride.StatusInProgress, ride.StatusCompleted)
	if err != nil {
		http.Error(w, "Failed to complete ride: "+err.Error(), http.StatusBadRequest)
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
	metrics.RidesCompletedTotal.Inc()

	h.Dispatcher.WSManager.ClearActiveRide(driverID)
	h.Dispatcher.WSManager.SendToRideWatchers(rideID, "RIDE_UPDATE", map[string]string{"ride_id": rideID, "status": string(ride.StatusCompleted)})

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

	rideData, err := h.Dispatcher.RideStore.GetRideByID(r.Context(), rideID)
	if err != nil || rideData == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"detail": "Ride not found"})
		return
	}

	if rideData.Status == ride.StatusCancelled || rideData.Status == ride.StatusCompleted {
		json.NewEncoder(w).Encode(map[string]string{"detail": "Ride already cancelled"})
		return
	}

	err = h.Dispatcher.RideStore.UpdateRideStatus(r.Context(), rideID, rideData.Status, ride.StatusCancelled)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"detail": "Failed to cancel ride: " + err.Error()})
		return
	}
	if rideData.DriverID != nil {
		h.Dispatcher.WSManager.ClearActiveRide(*rideData.DriverID)
	}
	metrics.RidesCancelledTotal.Inc()
	h.Dispatcher.WSManager.SendToRideWatchers(rideID, "RIDE_UPDATE", map[string]string{"ride_id": rideID, "status": string(ride.StatusCancelled)})
	json.NewEncoder(w).Encode(map[string]string{"detail": "Ride Cancelled"})
}

func (h *RideHandler) HandleGetHistory(w http.ResponseWriter, r *http.Request) {
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	role, ok := r.Context().Value(middleware.UserRoleKey).(string)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	limit, _ := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 64)
	if limit <= 0 {
		limit = 10
	}
	skip, _ := strconv.ParseInt(r.URL.Query().Get("skip"), 10, 64)

	rides, err := h.Dispatcher.RideStore.GetRideHistory(r.Context(), uidStr, role, limit, skip)
	if err != nil {
		http.Error(w, "Failed to fetch history: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rides)
}

func (h *RideHandler) HandleGetCurrentRide(w http.ResponseWriter, r *http.Request) {
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	role, ok := r.Context().Value(middleware.UserRoleKey).(string)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

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
		http.Error(w, "Failed to fetch ride: "+err.Error(), http.StatusInternalServerError)
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
		RideID string `json:"ride_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	rideData, err := h.Dispatcher.RideStore.GetRideByID(r.Context(), req.RideID)
	if err != nil || rideData == nil {
		http.Error(w, "Ride not found", http.StatusNotFound)
		return
	}

	if rideData.DriverID == nil {
		http.Error(w, "No driver assigned yet", http.StatusNotFound)
		return
	}

	objID, err := primitive.ObjectIDFromHex(*rideData.DriverID)
	if err != nil {
		http.Error(w, "Invalid driver ID", http.StatusBadRequest)
		return
	}

	driver, err := h.AuthStore.FindDriverByID(r.Context(), objID)
	if err != nil || driver == nil {
		http.Error(w, "Driver not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(driver)
}

// HandleGetUserDetails is used by the Driver App to get the user's info
func (h *RideHandler) HandleGetUserDetails(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RideID string `json:"ride_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	rideData, err := h.Dispatcher.RideStore.GetRideByID(r.Context(), req.RideID)
	if err != nil || rideData == nil {
		http.Error(w, "Ride not found", http.StatusNotFound)
		return
	}

	objID, err := primitive.ObjectIDFromHex(rideData.UserID)
	if err != nil {
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	user, err := h.AuthStore.FindUserByID(r.Context(), objID)
	if err != nil || user == nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

func (h *RideHandler) HandleRoutePreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OriginLat  float64 `json:"origin_lat"`
		OriginLng  float64 `json:"origin_lng"`
		DestLat    float64 `json:"dest_lat"`
		DestLng    float64 `json:"dest_lng"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	route, err := h.RouteClient.CalculateETA(r.Context(), req.OriginLat, req.OriginLng, req.DestLat, req.DestLng)
	if err != nil {
		http.Error(w, "Failed to compute route: "+err.Error(), http.StatusInternalServerError)
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
		DistanceKm float64 `json:"distance_km"`
		AmbTypeID  string  `json:"amb_type_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if req.DistanceKm <= 0 {
		http.Error(w, "distance_km must be positive", http.StatusBadRequest)
		return
	}

	// If amb_type_id is provided, estimate for that type only
	if req.AmbTypeID != "" {
		ambType, err := h.AdminStore.GetAmbulanceTypeByID(r.Context(), req.AmbTypeID)
		if err != nil || ambType == nil {
			http.Error(w, "Ambulance type not found", http.StatusNotFound)
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
		night := h.PricingEngine.CalculateNightSurcharge(base, time.Now())
		total := float64(int((base+night)*100)) / 100

		dBase := h.PricingEngine.CalculateBaseAndDistanceFare(req.DistanceKm, ambType.DriverShare, pricingTiers)
		dNight := h.PricingEngine.CalculateNightSurcharge(dBase, time.Now())
		driverShare := float64(int((dBase+dNight)*100)) / 100

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
		http.Error(w, "Failed to load ambulance types", http.StatusInternalServerError)
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
		night := h.PricingEngine.CalculateNightSurcharge(base, time.Now())
		total := float64(int((base+night)*100)) / 100

		dBase := h.PricingEngine.CalculateBaseAndDistanceFare(req.DistanceKm, ambType.DriverShare, pricingTiers)
		dNight := h.PricingEngine.CalculateNightSurcharge(dBase, time.Now())
		driverShare := float64(int((dBase+dNight)*100)) / 100

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
