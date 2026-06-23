package handlers

import (
	"encoding/json"
	"net/http"

	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/translation"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type AdminHandler struct {
	Store         *admin.Store
	AuthStore     *auth.Store
	EventBus      *eventbus.InMemoryBus
	HospitalStore *admin.HospitalStore
}

func NewAdminHandler(store *admin.Store, authStore *auth.Store, eventBus *eventbus.InMemoryBus, hStore *admin.HospitalStore) *AdminHandler {
	return &AdminHandler{
		Store:         store,
		AuthStore:     authStore,
		EventBus:      eventBus,
		HospitalStore: hStore,
	}
}

// HandleAdminLogin validates admin credentials (simplistic implementation for prototype)
func (h *AdminHandler) HandleAdminLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	// Hardcoded prototype admin credentials
	if req.Username == "admin" && req.Password == "ambigo2024" {
		// Generate JWT token
		// Normally we'd use the secret from the config, but we'll mock it or use middleware
		// Since we don't have the secret here easily, we'll assume the front-end will just
		// receive a success response and we'll trust a static token for the prototype.
		// For a real app, inject the JWT config.
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"detail": "Admin Login Successful",
			"token": "admin_mock_jwt_token", // Placeholder
		})
		return
	}

	http.Error(w, "Invalid credentials", http.StatusUnauthorized)
}

func (h *AdminHandler) HandleCreateAmbulanceType(w http.ResponseWriter, r *http.Request) {
	var amb admin.AmbulanceType
	if err := json.NewDecoder(r.Body).Decode(&amb); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	if err := h.Store.CreateAmbulanceType(r.Context(), &amb); err != nil {
		http.Error(w, "Failed to create: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminAmbTypeCreated, eventbus.AdminAmbTypePayload{
		AmbTypeID: amb.ID.Hex(), Name: amb.Name,
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
		http.Error(w, "Failed to fetch list: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func (h *AdminHandler) HandleDeleteAmbulanceType(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	objID, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	err = h.Store.DeleteAmbulanceType(r.Context(), objID)
	if err != nil {
		http.Error(w, "Failed to delete: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminAmbTypeDeleted, eventbus.AdminAmbTypePayload{
		AmbTypeID: idStr,
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Deleted"})
}

// HandleApproveDriver flips an unverified driver to a verified driver
func (h *AdminHandler) HandleApproveDriver(w http.ResponseWriter, r *http.Request) {
	var driver auth.Driver
	if err := json.NewDecoder(r.Body).Decode(&driver); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	
	err := h.AuthStore.ApproveDriver(r.Context(), &driver)
	if err != nil {
		http.Error(w, "Failed to approve driver: "+err.Error(), http.StatusBadRequest)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAuthDriverApproved, eventbus.AuthDriverApprovedPayload{
		DriverID: driver.ID.Hex(), Name: driver.Name, Mobile: driver.Mobile,
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
	Name        string `json:"name"`
	Address     string `json:"address"`
	City        string `json:"city"`
	Coordinates struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	} `json:"coordinates"`
	AlwaysOpen bool     `json:"always_open"`
	Services   []string `json:"services"`
	Timing     *struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"timing"`
}

func (h *AdminHandler) HandleAddHospital(w http.ResponseWriter, r *http.Request) {
	var req HospitalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
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
		http.Error(w, "Hospital add failed", http.StatusBadRequest)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminHospitalAdded, eventbus.AdminHospitalPayload{
		HospitalID: hospital.ID.Hex(), Name: hospital.Name["en_US"],
	})
	json.NewEncoder(w).Encode(map[string]string{"detail": "Hospital added successfully"})
}

func (h *AdminHandler) HandleUpdateHospital(w http.ResponseWriter, r *http.Request) {
	var req HospitalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "Invalid payload or missing ID", http.StatusBadRequest)
		return
	}

	objID, err := primitive.ObjectIDFromHex(req.ID)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
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
		http.Error(w, "Hospital updated failed", http.StatusBadRequest)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminHospitalUpdated, eventbus.AdminHospitalPayload{
		HospitalID: req.ID,
	})
	json.NewEncoder(w).Encode(map[string]string{"detail": "Hospital updated successfully"})
}

func (h *AdminHandler) HandleDeleteHospital(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HospitalID string `json:"hospital_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	objID, err := primitive.ObjectIDFromHex(req.HospitalID)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if err := h.HospitalStore.DeleteHospital(r.Context(), objID); err != nil {
		http.Error(w, "Hospital delete failed", http.StatusBadRequest)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminHospitalDeleted, eventbus.AdminHospitalPayload{
		HospitalID: req.HospitalID,
	})
	json.NewEncoder(w).Encode(map[string]string{"detail": "Hospital deleted successfully"})
}

