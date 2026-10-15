package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/ride"
	"ambigo-backend/internal/websocket"

	"ambigo-backend/internal/ids"
)

type HospitalDashboardHandler struct {
	RideStore     *ride.Store
	HospitalStore *admin.HospitalStore
	AdminStore    *admin.Store
	AuthStore     *auth.Store
	WSManager     *websocket.Manager
	EventBus      *eventbus.InMemoryBus
}

func NewHospitalDashboardHandler(rideStore *ride.Store, hospitalStore *admin.HospitalStore, adminStore *admin.Store, authStore *auth.Store, wsManager *websocket.Manager, bus *eventbus.InMemoryBus) *HospitalDashboardHandler {
	return &HospitalDashboardHandler{
		RideStore:     rideStore,
		HospitalStore: hospitalStore,
		AdminStore:    adminStore,
		AuthStore:     authStore,
		WSManager:     wsManager,
		EventBus:      bus,
	}
}

func hospitalIDFromContext(r *http.Request) (string, bool) {
	hid, ok := r.Context().Value(middleware.HospitalIDKey).(string)
	if !ok || hid == "" {
		return "", false
	}
	return hid, true
}

func (h *HospitalDashboardHandler) HandleHospitalStats(w http.ResponseWriter, r *http.Request) {
	hid, ok := hospitalIDFromContext(r)
	if !ok {
		response.Error(w, "Hospital not linked to account", http.StatusBadRequest)
		return
	}
	incomingStatuses := []ride.RideStatus{ride.StatusSearching, ride.StatusAssigned, ride.StatusArrived, ride.StatusInProgress}
	incoming, _ := h.RideStore.CountRidesByHospital(r.Context(), hid, incomingStatuses)
	completed, _ := h.RideStore.CountRidesByHospital(r.Context(), hid, []ride.RideStatus{ride.StatusCompleted})
	cancelled, _ := h.RideStore.CountRidesByHospital(r.Context(), hid, []ride.RideStatus{ride.StatusCancelled})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"incoming":  incoming,
		"completed": completed,
		"cancelled": cancelled,
		"total":     incoming + completed,
	})
}

func (h *HospitalDashboardHandler) HandleHospitalIncomingRides(w http.ResponseWriter, r *http.Request) {
	hid, ok := hospitalIDFromContext(r)
	if !ok {
		response.Error(w, "Hospital not linked", http.StatusBadRequest)
		return
	}
	var req struct {
		Limit int64 `json:"limit"`
		Skip  int64 `json:"skip"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Limit <= 0 || req.Limit > 100 {
		req.Limit = 20
	}
	statuses := []ride.RideStatus{ride.StatusSearching, ride.StatusAssigned, ride.StatusArrived, ride.StatusInProgress}
	rides, err := h.RideStore.ListRidesByHospital(r.Context(), hid, statuses, req.Limit, req.Skip)
	if err != nil {
		response.Error(w, "Failed to fetch", http.StatusInternalServerError)
		return
	}
	// Populate full chat history (drawer -> screen fix)
	if err := h.RideStore.PopulateConditionUpdates(r.Context(), rides); err != nil {
		// non-fatal: still return rides with latest_condition only
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h.enrichRides(r.Context(), rides))
}

// enrichedRide is a ride plus desk-facing enrichments (all non-fatal,
// all resolved server-side so the portal never shows raw IDs).
type enrichedRide struct {
	*ride.Ride
	DriverLocation  *admin.GeoJSON   `json:"driver_location,omitempty"`
	Readiness       *ride.Readiness `json:"readiness,omitempty"`
	AmbTypeName     string          `json:"amb_type_name,omitempty"`
	AttendantMobile string          `json:"attendant_mobile,omitempty"`
	DriverName      string          `json:"driver_name,omitempty"`
	DriverMobile    string          `json:"driver_mobile,omitempty"`
}

// enrichRides batches readiness, ambulance names, driver identity, attendant
// number, and live driver location onto rides. Every lookup is non-fatal: a
// failure leaves that field empty instead of failing the list.
func (h *HospitalDashboardHandler) enrichRides(ctx context.Context, rides []*ride.Ride) []enrichedRide {
	readinessByRide := map[string]*ride.Readiness{}
	if len(rides) > 0 {
		ids := make([]string, 0, len(rides))
		for _, rd := range rides {
			ids = append(ids, rd.ID)
		}
		if m, err := h.RideStore.GetReadinessForRides(ctx, ids); err == nil {
			readinessByRide = m
		}
	}
	nameByAmbID := map[string]string{}
	if ambTypes, err := h.AdminStore.ListAmbulanceTypes(ctx); err == nil {
		for _, t := range ambTypes {
			nameByAmbID[t.ID] = t.Name
		}
	}
	out := make([]enrichedRide, 0, len(rides))
	for _, rd := range rides {
		er := enrichedRide{Ride: rd, Readiness: readinessByRide[rd.ID]}
		if rd.AmbTypeID != nil {
			er.AmbTypeName = nameByAmbID[*rd.AmbTypeID]
		}
		if rd.DriverID != nil && *rd.DriverID != "" {
			if drv, err := h.AuthStore.FindDriverByID(ctx, *rd.DriverID); err == nil && drv != nil {
				er.DriverName = drv.Name
				er.DriverMobile = drv.Mobile
			}
			if atts, err := h.AuthStore.ListAttendantsByDriver(ctx, *rd.DriverID); err == nil {
				for _, a := range atts {
					if a.Active && a.Mobile != "" {
						er.AttendantMobile = a.Mobile
						break
					}
				}
				if er.AttendantMobile == "" && len(atts) > 0 {
					er.AttendantMobile = atts[0].Mobile
				}
			}
			if h.WSManager != nil && h.WSManager.LocStore != nil {
				if lat, lng, err := h.WSManager.LocStore.GetLocation(*rd.DriverID); err == nil {
					er.DriverLocation = &admin.GeoJSON{Type: "Point", Coordinates: []float64{lng, lat}}
				}
			}
		}
		out = append(out, er)
	}
	return out
}

func (h *HospitalDashboardHandler) HandleHospitalHistory(w http.ResponseWriter, r *http.Request) {
	hid, ok := hospitalIDFromContext(r)
	if !ok {
		response.Error(w, "Hospital not linked", http.StatusBadRequest)
		return
	}
	var req struct {
		Limit int64 `json:"limit"`
		Skip  int64 `json:"skip"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Limit <= 0 || req.Limit > 100 {
		req.Limit = 20
	}
	statuses := []ride.RideStatus{ride.StatusCompleted, ride.StatusCancelled}
	rides, err := h.RideStore.ListRidesByHospital(r.Context(), hid, statuses, req.Limit, req.Skip)
	if err != nil {
		response.Error(w, "Failed to fetch", http.StatusInternalServerError)
		return
	}
	_ = h.RideStore.PopulateConditionUpdates(r.Context(), rides)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h.enrichRides(r.Context(), rides))
}

func (h *HospitalDashboardHandler) HandleHospitalRideDetail(w http.ResponseWriter, r *http.Request) {
	hid, ok := hospitalIDFromContext(r)
	if !ok {
		response.Error(w, "Hospital not linked", http.StatusBadRequest)
		return
	}
	// ride id from path /api/v2/hospital/rides/{id}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	rideID := parts[len(parts)-1]
	if rideID == "detail" {
		var body struct {
			RideID string `json:"ride_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		rideID = body.RideID
	}
	if rideID == "" {
		response.Error(w, "ride_id required", http.StatusBadRequest)
		return
	}
	rideDoc, err := h.RideStore.GetRideByID(r.Context(), rideID)
	if err != nil || rideDoc == nil {
		response.Error(w, "Ride not found", http.StatusNotFound)
		return
	}
	if rideDoc.HospitalID == nil || *rideDoc.HospitalID != hid {
		response.Error(w, "Forbidden: different hospital", http.StatusForbidden)
		return
	}
	// Attach full timeline for chat view
	if ups, err := h.RideStore.ListConditionUpdates(r.Context(), rideDoc.ID); err == nil {
		rideDoc.ConditionUpdates = ups
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rideDoc)
}

func (h *HospitalDashboardHandler) HandleHospitalProfile(w http.ResponseWriter, r *http.Request) {
	hid, ok := hospitalIDFromContext(r)
	if !ok {
		response.Error(w, "Hospital not linked", http.StatusBadRequest)
		return
	}
	if !ids.IsValid(hid) {
		response.Error(w, "Invalid hospital ID", http.StatusBadRequest)
		return
	}
	hospital, err := h.HospitalStore.FindByID(r.Context(), hid)
	if err != nil || hospital == nil {
		response.Error(w, "Hospital not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(hospital)
}

func (h *HospitalDashboardHandler) HandleUpdateHospitalProfile(w http.ResponseWriter, r *http.Request) {
	hid, ok := hospitalIDFromContext(r)
	if !ok {
		response.Error(w, "Hospital not linked", http.StatusBadRequest)
		return
	}
	if !ids.IsValid(hid) {
		response.Error(w, "Invalid hospital ID", http.StatusBadRequest)
		return
	}
	var req struct {
		Timing     *admin.Timing `json:"timing"`
		AlwaysOpen *bool         `json:"always_open"`
		Services   []string      `json:"services"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	hospital, err := h.HospitalStore.FindByID(r.Context(), hid)
	if err != nil || hospital == nil {
		response.Error(w, "Hospital not found", http.StatusNotFound)
		return
	}
	if req.Timing != nil {
		hospital.Timing = req.Timing
	}
	if req.AlwaysOpen != nil {
		hospital.AlwaysOpen = *req.AlwaysOpen
	}
	if req.Services != nil {
		hospital.Services = req.Services
	}
	if hospital.Services == nil {
		hospital.Services = []string{}
	}
	if err := h.HospitalStore.UpdateHospital(r.Context(), hospital); err != nil {
		response.Error(w, "Failed to update", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"detail": "Hospital updated"})
}

func (h *HospitalDashboardHandler) HandleHospitalAnalytics(w http.ResponseWriter, r *http.Request) {
	hid, ok := hospitalIDFromContext(r)
	if !ok {
		response.Error(w, "Hospital not linked", http.StatusBadRequest)
		return
	}
	var req struct {
		Range string `json:"range"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	days := 7
	if req.Range == "30d" {
		days = 30
	}
	since := time.Now().AddDate(0, 0, -days)
	rides, err := h.RideStore.ListRidesByHospitalSince(r.Context(), hid, since)
	if err != nil {
		response.Error(w, "Failed to fetch analytics", http.StatusInternalServerError)
		return
	}
	// Resolve ambulance type IDs to names
	nameByID := map[string]string{}
	if ambTypes, err := h.AdminStore.ListAmbulanceTypes(r.Context()); err == nil {
		for _, t := range ambTypes {
			nameByID[t.ID] = t.Name
		}
	}
	byCondition := map[string]int64{"stable": 0, "serious": 0, "critical": 0, "worsening": 0}
	byAmbulanceType := map[string]int64{}
	byDate := map[string]map[string]int64{}
	byStatus := map[string]int64{}
	byHour := make([]int64, 24)
	cancelReasons := map[string]int64{}
	var sosCount int64
	var doorMinutesSum float64
	var doorMinutesN int64
	for _, ride := range rides {
		byStatus[string(ride.Status)]++
		if ride.EmergencyPriority > 0 {
			sosCount++
		}
		if ride.LatestCondition != nil {
			byCondition[ride.LatestCondition.Level]++
		}
		rawKey := "Unknown"
		if ride.AmbTypeID != nil && *ride.AmbTypeID != "" {
			rawKey = *ride.AmbTypeID
		}
		displayKey := rawKey
		if n, ok := nameByID[rawKey]; ok && n != "" {
			displayKey = n
		} else if rawKey == "Unknown" {
			displayKey = "Unknown"
		}
		byAmbulanceType[displayKey]++
		dateKey := ride.Time.CreatedAt.Format("2006-01-02")
		day, ok := byDate[dateKey]
		if !ok {
			day = map[string]int64{"total": 0, "critical": 0}
		}
		day["total"]++
		if ride.LatestCondition != nil && (ride.LatestCondition.Level == "critical" || ride.LatestCondition.Level == "worsening") {
			day["critical"]++
		}
		byDate[dateKey] = day
		byHour[ride.Time.CreatedAt.Hour()]++
		// Door time: trip start to completion, the number the desk staffs by.
		if string(ride.Status) == "COMPLETED" && ride.Time.StartedAt != nil && ride.Time.CompletedAt != nil {
			if mins := ride.Time.CompletedAt.Sub(*ride.Time.StartedAt).Minutes(); mins >= 0 && mins < 24*60 {
				doorMinutesSum += mins
				doorMinutesN++
			}
		}
		if string(ride.Status) == "CANCELLED" && ride.CancellationReason != "" {
			cancelReasons[ride.CancellationReason]++
		}
	}
	var avgDoorMinutes *float64
	if doorMinutesN > 0 {
		v := doorMinutesSum / float64(doorMinutesN)
		avgDoorMinutes = &v
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"byCondition":     byCondition,
		"byAmbulanceType": byAmbulanceType,
		"byDate":          byDate,
		"byStatus":        byStatus,
		"byHour":          byHour,
		"cancelReasons":   cancelReasons,
		"sos":             sosCount,
		"avgDoorMinutes":  avgDoorMinutes,
		"total":           len(rides),
	})
}

// HandleUpdateRideCondition is called by the user/attendant during an ongoing ride
func (h *HospitalDashboardHandler) HandleUpdateRideCondition(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	role, _ := r.Context().Value(middleware.UserRoleKey).(string)
	// ride id from path
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	rideID := ""
	for i, p := range parts {
		if p == "rides" && i+1 < len(parts) && parts[i+1] != "condition" {
			rideID = parts[i+1]
			break
		}
	}
	var req struct {
		Level  string `json:"level"`
		Note   string `json:"note"`
		RideID string `json:"ride_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.RideID != "" {
		rideID = req.RideID
	}
	// Fallback: if rideID still empty, try to get from already decoded req (above)
	if rideID == "" {
		response.Error(w, "ride_id required", http.StatusBadRequest)
		return
	}
	level := strings.ToLower(req.Level)
	if level == "" {
		response.Error(w, "level required: stable|serious|critical|worsening", http.StatusBadRequest)
		return
	}
	if level != "stable" && level != "serious" && level != "critical" && level != "worsening" {
		response.Error(w, "invalid level", http.StatusBadRequest)
		return
	}
	rideDoc, err := h.RideStore.GetRideByID(r.Context(), rideID)
	if err != nil || rideDoc == nil {
		response.Error(w, "Ride not found", http.StatusNotFound)
		return
	}
	source := "user"
	if role == "attendant" {
		source = "attendant"
		if ids.IsValid(userID) {
			if att, _ := h.AuthStore.FindAmbulanceAttendantByID(r.Context(), userID); att != nil && att.AssignedDriverID != nil && rideDoc.DriverID != nil {
				if *att.AssignedDriverID != *rideDoc.DriverID {
					response.Error(w, "Forbidden: not assigned to this ride's driver", http.StatusForbidden)
					return
				}
			}
		}
	} else if rideDoc.UserID != userID {
		response.Error(w, "Forbidden: not ride owner", http.StatusForbidden)
		return
	}
	if rideDoc.Status != ride.StatusAssigned && rideDoc.Status != ride.StatusArrived && rideDoc.Status != ride.StatusInProgress {
		response.Error(w, "Condition can only be updated during active ride", http.StatusBadRequest)
		return
	}
	upd := ride.ConditionUpdate{
		Level:     level,
		Severity:  ride.ConditionSeverity(level),
		Note:      req.Note,
		Source:    source,
		CreatedAt: time.Now(),
	}
	if err := h.RideStore.AppendConditionUpdate(r.Context(), rideID, upd); err != nil {
		response.Error(w, "Failed to update", http.StatusInternalServerError)
		return
	}
	// Push to driver via WS
	if rideDoc.DriverID != nil {
		h.WSManager.SendToClient("driver", *rideDoc.DriverID, "CONDITION_UPDATE", map[string]interface{}{
			"ride_id": rideID,
			"level":   level,
			"severity": upd.Severity,
			"note":    req.Note,
		})
	}
	// Push to hospital watchers if hospital linked
	if rideDoc.HospitalID != nil {
		h.WSManager.SendToRideWatchers(rideID, "CONDITION_UPDATE", map[string]interface{}{
			"ride_id": rideID,
			"level":   level,
			"severity": upd.Severity,
			"note":    req.Note,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"detail": "Condition updated", "level": level})
}

func (h *HospitalDashboardHandler) HandleAttendantHospitalContact(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RideID string `json:"ride_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RideID == "" {
		response.Error(w, "ride_id required", http.StatusBadRequest)
		return
	}
	rideDoc, err := h.RideStore.GetRideByID(r.Context(), req.RideID)
	if err != nil || rideDoc == nil {
		response.Error(w, "Ride not found", http.StatusNotFound)
		return
	}
	if rideDoc.HospitalID == nil {
		response.Error(w, "Ride has no hospital", http.StatusBadRequest)
		return
	}
	hid := *rideDoc.HospitalID
	hospital, _ := h.HospitalStore.FindByID(r.Context(), hid)
	md, _ := h.AuthStore.FindHospitalMDByHospitalID(r.Context(), hid)
	var officialNumber, email, hospitalName string
	if hospital != nil {
		hospitalName = hospital.Name["en_US"]
	}
	if md != nil {
		officialNumber = md.OfficialNumber
		email = md.Email
		if hospitalName == "" {
			hospitalName = md.Name
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"hospital_id":     *rideDoc.HospitalID,
		"hospital_name":   hospitalName,
		"official_number": officialNumber,
		"email":           email,
		"address":         rideDoc.DropAddress,
	})
}

func (h *HospitalDashboardHandler) HandleAttendantCurrentRide(w http.ResponseWriter, r *http.Request) {
	attendantIDStr, _ := r.Context().Value(middleware.UserIDKey).(string)
	if attendantIDStr != "" {
		if ids.IsValid(attendantIDStr) {
			if att, _ := h.AuthStore.FindAmbulanceAttendantByID(r.Context(), attendantIDStr); att != nil && att.AssignedDriverID != nil {
				// Find current ride for the assigned driver
				ride, _ := h.RideStore.GetCurrentRide(r.Context(), *att.AssignedDriverID, "driver")
				if ride != nil {
					if ups, err := h.RideStore.ListConditionUpdates(r.Context(), ride.ID); err == nil {
						ride.ConditionUpdates = ups
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]interface{}{"found": true, "ride": ride})
					return
				}
			}
		}
	}
	// Fallback: most recent active ride (for demo / unassigned attendant)
	statuses := []ride.RideStatus{ride.StatusInProgress, ride.StatusAssigned, ride.StatusArrived, ride.StatusSearching}
	rides, err := h.RideStore.ListRidesByStatus(r.Context(), statuses, 1, 0)
	if err != nil || len(rides) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"found": false})
		return
	}
	if ups, err := h.RideStore.ListConditionUpdates(r.Context(), rides[0].ID); err == nil {
		rides[0].ConditionUpdates = ups
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"found": true, "ride": rides[0]})
}

// HandleAcknowledgeRide records the desk's triage acknowledgement for one of
// its own hospital's incoming rides. The ack silences that ride's alarm on
// the dashboard and survives refresh/shift change. Cross-hospital writes
// are rejected.
func (h *HospitalDashboardHandler) HandleAcknowledgeRide(w http.ResponseWriter, r *http.Request) {
	hid, ok := hospitalIDFromContext(r)
	if !ok {
		response.Error(w, "Hospital not linked", http.StatusBadRequest)
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	var req struct {
		RideID string `json:"ride_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RideID == "" {
		response.Error(w, "ride_id required", http.StatusBadRequest)
		return
	}
	if !ids.IsValid(req.RideID) {
		response.Error(w, "Invalid ride_id", http.StatusBadRequest)
		return
	}
	rideDoc, err := h.RideStore.GetRideByID(r.Context(), req.RideID)
	if err != nil || rideDoc == nil {
		response.Error(w, "Ride not found", http.StatusNotFound)
		return
	}
	if rideDoc.HospitalID == nil || *rideDoc.HospitalID != hid {
		response.Error(w, "Forbidden: different hospital", http.StatusForbidden)
		return
	}
	rd, err := h.RideStore.UpsertReadiness(r.Context(), req.RideID, hid, userID)
	if err != nil {
		response.Error(w, "Failed to save", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rd)
}

// declineRerouteRadiusKm bounds the auto-reroute search around the driver's
// live position (mid-ride). FindNearby covers ~25-30km via H3 ring-2.
const declineRerouteRadiusKm = 30.0

// hospitalAddressDisplay returns the en_US address, falling back to any locale.
func hospitalAddressDisplay(addr map[string]string) string {
	if addr == nil {
		return ""
	}
	if s := strings.TrimSpace(addr["en_US"]); s != "" {
		return s
	}
	for _, s := range addr {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// HandleDeclineRide declines a mid-ride inbound patient and immediately
// reroutes the ride to the nearest other hospital (auto-nearest), then
// notifies the driver of the changed destination via WS.
// Contract (agreed with portal):
//   POST /api/v2/hospital/rides/decline {ride_id, reason?}
//   200 {detail, ride_id, from_hospital_id, to_hospital_id, to_hospital_name, drop_address}
//   409 "No nearby hospital available ..." -> portal keeps the ride ringing.
// Only SEARCHING/ASSIGNED/ARRIVED/IN_PROGRESS rides can be declined;
// COMPLETED/CANCELLED are terminal (409). Cross-hospital writes are 403.
func (h *HospitalDashboardHandler) HandleDeclineRide(w http.ResponseWriter, r *http.Request) {
	hid, ok := hospitalIDFromContext(r)
	if !ok {
		response.Error(w, "Hospital not linked", http.StatusBadRequest)
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	var req struct {
		RideID string `json:"ride_id"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RideID == "" {
		response.Error(w, "ride_id required", http.StatusBadRequest)
		return
	}
	if !ids.IsValid(req.RideID) {
		response.Error(w, "Invalid ride_id", http.StatusBadRequest)
		return
	}
	rideDoc, err := h.RideStore.GetRideByID(r.Context(), req.RideID)
	if err != nil || rideDoc == nil {
		response.Error(w, "Ride not found", http.StatusNotFound)
		return
	}
	if rideDoc.HospitalID == nil || *rideDoc.HospitalID != hid {
		response.Error(w, "Forbidden: different hospital", http.StatusForbidden)
		return
	}
	switch rideDoc.Status {
	case ride.StatusSearching, ride.StatusAssigned, ride.StatusArrived, ride.StatusInProgress:
		// declinable
	default:
		response.Error(w, "Ride already "+string(rideDoc.Status)+" — cannot decline", http.StatusConflict)
		return
	}

	// Origin = live driver position (mid-ride), fallback to pickup.
	var lng, lat float64
	var haveOrigin bool
	if rideDoc.DriverID != nil && *rideDoc.DriverID != "" && h.WSManager != nil && h.WSManager.LocStore != nil {
		if la, ln, err := h.WSManager.LocStore.GetLocation(*rideDoc.DriverID); err == nil {
			lat, lng, haveOrigin = la, ln, true
		}
	}
	if !haveOrigin && len(rideDoc.Pickup.Coordinates) == 2 {
		lng, lat, haveOrigin = rideDoc.Pickup.Coordinates[0], rideDoc.Pickup.Coordinates[1], true
	}
	if !haveOrigin {
		response.Error(w, "Cannot locate ride for reroute — ride stays with you", http.StatusConflict)
		return
	}

	// Nearest-first candidates, excluding the declining desk. Prefer
	// always-open hospitals first so the patient never lands on a closed desk.
	candidates, err := h.HospitalStore.FindNearby(r.Context(), lng, lat, declineRerouteRadiusKm)
	if err != nil {
		response.Error(w, "Failed to search nearby hospitals", http.StatusInternalServerError)
		return
	}
	var target *admin.Hospital
	for i := range candidates {
		if candidates[i].ID == hid {
			continue
		}
		if candidates[i].AlwaysOpen {
			target = &candidates[i]
			break
		}
	}
	if target == nil {
		for i := range candidates {
			if candidates[i].ID == hid {
				continue
			}
			target = &candidates[i]
			break
		}
	}
	if target == nil {
		response.Error(w, "No nearby hospital available — ride stays with you", http.StatusConflict)
		return
	}
	targetHosp, err := h.HospitalStore.FindByID(r.Context(), target.ID)
	if err != nil || targetHosp == nil || len(targetHosp.Location.Coordinates) != 2 {
		response.Error(w, "Reroute target unavailable — ride stays with you", http.StatusConflict)
		return
	}

	newDropAddr := hospitalAddressDisplay(targetHosp.Address)
	if newDropAddr == "" {
		newDropAddr = admin.HospitalDisplayName(targetHosp.Name)
	}
	newDrop := ride.GeoJSONPoint{Type: "Point", Coordinates: append([]float64(nil), targetHosp.Location.Coordinates...)}

	// Atomic move guarded by expected current hospital (no double-move).
	if err := h.RideStore.ReassignRideHospital(r.Context(), rideDoc.ID, hid, targetHosp.ID, newDrop, newDropAddr); err != nil {
		response.Error(w, "Ride already moved — refreshing", http.StatusConflict)
		return
	}
	// Audit + clear stale ack (non-fatal: reroute already committed).
	_, _ = h.RideStore.RecordDecline(r.Context(), rideDoc.ID, hid, &targetHosp.ID, userID, req.Reason)
	_ = h.RideStore.DeleteReadiness(r.Context(), rideDoc.ID)

	targetName := admin.HospitalDisplayName(targetHosp.Name)
	newLat := targetHosp.Location.Coordinates[1]
	newLng := targetHosp.Location.Coordinates[0]
	driverID := ""
	if rideDoc.DriverID != nil {
		driverID = *rideDoc.DriverID
	}

	// Full destination payload: the user app applies it directly, no
	// detail re-fetch needed to show the new hospital + address.
	fullDest := map[string]interface{}{
		"ride_id":          rideDoc.ID,
		"hospital_id":      targetHosp.ID,
		"hospital_name":    targetName,
		"drop":             newDrop,
		"drop_address":     newDropAddr,
		"drop_lat":         newLat,
		"drop_lng":         newLng,
		"lat":              newLat,
		"lng":              newLng,
		"status":           string(rideDoc.Status),
		"from_hospital_id": hid,
	}

	// Notify driver of the changed destination (direct push).
	if driverID != "" && h.WSManager != nil {
		h.WSManager.SendToClient("driver", driverID, websocket.EventDestinationChanged, fullDest)
	}
	// Notify the riding user directly with the FULL payload (foreground:
	// instant screen update, no re-fetch). Background delivery is via FCM
	// below (eventbus), since WS can't reach a killed app.
	if h.WSManager != nil {
		h.WSManager.SendToClient("user", rideDoc.UserID, websocket.EventDestinationChanged, fullDest)
	}
	if h.WSManager != nil {
		// Old desk: drop instantly instead of waiting for the 10s poll.
		h.WSManager.SendToRideWatchers(rideDoc.ID, websocket.EventRideReassignedAway, map[string]interface{}{
			"ride_id":          rideDoc.ID,
			"from_hospital_id": hid,
			"to_hospital_id":   targetHosp.ID,
			"to_hospital_name": targetName,
		})
		// New desk + watchers: full payload so screens update in place.
		// (Older clients that only read ride_id/status/hospital_id ignore
		// the extra keys and fall back to their existing reload path.)
		h.WSManager.SendToRideWatchers(rideDoc.ID, websocket.EventRideUpdate, fullDest)
		h.WSManager.SendToRideWatchers(rideDoc.ID, websocket.EventNewIncoming, map[string]interface{}{
			"ride_id":     rideDoc.ID,
			"hospital_id": targetHosp.ID,
		})
		h.WSManager.BroadcastToRole("admin", websocket.EventRideUpdate, map[string]interface{}{
			"ride_id":          rideDoc.ID,
			"status":           string(rideDoc.Status),
			"from_hospital_id": hid,
			"to_hospital_id":   targetHosp.ID,
		})
	}

	// FCM for background/killed user app (non-fatal: WS above already
	// covers the foreground case).
	if h.EventBus != nil {
		_ = h.EventBus.PublishEvent(eventbus.ChannelRideDestinationChanged, eventbus.RideDestinationChangedPayload{
			RideID:       rideDoc.ID,
			UserID:       rideDoc.UserID,
			DriverID:     driverID,
			HospitalID:   targetHosp.ID,
			HospitalName: targetName,
			DropLat:      newLat,
			DropLng:      newLng,
			DropAddress:  newDropAddr,
			FromHospital: hid,
			Status:       string(rideDoc.Status),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"detail":           "Declined — rerouted to nearest hospital. Driver notified.",
		"ride_id":          rideDoc.ID,
		"from_hospital_id": hid,
		"to_hospital_id":   targetHosp.ID,
		"to_hospital_name": targetName,
		"drop_address":     newDropAddr,
	})
}
