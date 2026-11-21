package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"ambigo-backend/api/response"
	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/ids"
	"ambigo-backend/internal/location"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/ride"
	"ambigo-backend/internal/safety"
	"ambigo-backend/internal/translation"
)

type AdminLiveRideHandler struct {
	RideStore        *ride.Store
	AuthStore        *auth.Store
	AdminStore       *admin.Store
	HospitalStore    *admin.HospitalStore
	AlertStore       *safety.AlertStore
	AuditStore       *admin.AuditStore
	SafetyStateStore *safety.StateStore
	LocationStore    *location.MemoryStore
}

type liveRidePerson struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Mobile string `json:"mobile"`
}

type liveRideDriver struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Mobile              string `json:"mobile"`
	Photo               string `json:"photo,omitempty"`
	VehicleRegistration string `json:"vehicle_registration,omitempty"`
}

type liveRideAmbulance struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Photo          string `json:"photo,omitempty"`
	HelperIncluded bool   `json:"helper_included"`
}

type liveRideHospital struct {
	ID       string          `json:"id"`
	Name     translation.Map `json:"name"`
	Address  translation.Map `json:"address"`
	City     translation.Map `json:"city"`
	Location admin.GeoJSON   `json:"location"`
}

func (h *AdminLiveRideHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	rideID := r.PathValue("id")
	if !ids.IsValid(rideID) {
		response.Error(w, "Invalid ride ID", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	rideDoc, err := h.RideStore.GetRideByID(ctx, rideID)
	if err != nil {
		h.fail(w, err)
		return
	}
	if rideDoc == nil {
		response.Error(w, "Ride not found", http.StatusNotFound)
		return
	}
	rideDoc.StartOTP = ""
	conditionUpdates, err := h.RideStore.ListConditionUpdates(ctx, rideID)
	if err != nil {
		h.fail(w, err)
		return
	}
	rideDoc.ConditionUpdates = conditionUpdates

	user, err := h.AuthStore.FindUserByID(ctx, rideDoc.UserID)
	if err != nil {
		h.fail(w, err)
		return
	}
	var patient *liveRidePerson
	if user != nil {
		patient = &liveRidePerson{ID: user.ID, Name: user.Name, Mobile: user.Mobile}
	}

	var driver *liveRideDriver
	var driverLocation *location.DriverLocation
	if rideDoc.DriverID != nil {
		driverDoc, lookupErr := h.AuthStore.FindDriverByID(ctx, *rideDoc.DriverID)
		if lookupErr != nil {
			h.fail(w, lookupErr)
			return
		}
		if driverDoc != nil {
			driver = &liveRideDriver{
				ID: driverDoc.ID, Name: driverDoc.Name, Mobile: driverDoc.Mobile,
				Photo: driverDoc.Photo, VehicleRegistration: driverDoc.VehicleReg,
			}
		}
		if current, ok := h.LocationStore.FleetDriver(*rideDoc.DriverID); ok {
			driverLocation = &current
		}
	}

	var ambulance *liveRideAmbulance
	if rideDoc.AmbTypeID != nil {
		ambDoc, lookupErr := h.AdminStore.GetAmbulanceTypeByID(ctx, *rideDoc.AmbTypeID)
		if lookupErr != nil {
			h.fail(w, lookupErr)
			return
		}
		if ambDoc != nil {
			ambulance = &liveRideAmbulance{
				ID: ambDoc.ID, Name: ambDoc.Name, Photo: ambDoc.Photo, HelperIncluded: ambDoc.HelperIncluded,
			}
		}
	}

	var hospital *liveRideHospital
	if rideDoc.HospitalID != nil {
		hospitalDoc, lookupErr := h.HospitalStore.FindByID(ctx, *rideDoc.HospitalID)
		if lookupErr != nil {
			h.fail(w, lookupErr)
			return
		}
		if hospitalDoc != nil {
			hospital = &liveRideHospital{
				ID: hospitalDoc.ID, Name: hospitalDoc.Name, Address: hospitalDoc.Address,
				City: hospitalDoc.City, Location: hospitalDoc.Location,
			}
		}
	}

	alerts, err := h.AlertStore.ListByRide(ctx, rideID)
	if err != nil {
		h.fail(w, err)
		return
	}
	activity, err := h.AuditStore.ListRideActivity(ctx, rideID)
	if err != nil {
		h.fail(w, err)
		return
	}
	persistedState, err := h.SafetyStateStore.GetByRide(ctx, rideID)
	if err != nil {
		h.fail(w, err)
		return
	}
	var stoppedState *safety.VehicleStateView
	if persistedState != nil && rideDoc.Status == ride.StatusInProgress {
		view := persistedState.View(time.Now())
		stoppedState = &view
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ride": rideDoc, "patient": patient, "driver": driver,
		"ambulance_type": ambulance, "hospital": hospital,
		"alerts": alerts, "activity": activity,
		"stopped_state": stoppedState, "driver_location": driverLocation,
		"generated_at": time.Now(),
	})
}

func (h *AdminLiveRideHandler) fail(w http.ResponseWriter, err error) {
	logger.Log.Error().Err(err).Msg("Failed to load live ride inspector")
	response.Error(w, "Failed to load live ride", http.StatusInternalServerError)
}
