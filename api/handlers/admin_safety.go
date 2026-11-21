package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/safety"
	"ambigo-backend/internal/websocket"
)

type AdminSafetyHandler struct {
	Store     *safety.AlertStore
	WSManager *websocket.Manager
}

func (h *AdminSafetyHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	alerts, err := h.Store.List(r.Context(), limit)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to list safety alerts")
		response.Error(w, "Failed to load safety alerts", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"alerts": alerts, "generated_at": time.Now(),
	})
}

func (h *AdminSafetyHandler) HandleAcknowledge(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil || request.ID == "" {
		response.Error(w, "Alert ID is required", http.StatusBadRequest)
		return
	}
	adminID, _ := r.Context().Value(middleware.UserIDKey).(string)
	alert, err := h.Store.Acknowledge(r.Context(), request.ID, adminID)
	h.writeTransition(w, alert, err)
}

func (h *AdminSafetyHandler) HandleResolve(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID         string `json:"id"`
		Resolution string `json:"resolution"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil || request.ID == "" {
		response.Error(w, "Alert ID is required", http.StatusBadRequest)
		return
	}
	if request.Resolution == "" {
		request.Resolution = "resolved_by_operator"
	}
	adminID, _ := r.Context().Value(middleware.UserIDKey).(string)
	alert, err := h.Store.Resolve(r.Context(), request.ID, adminID, request.Resolution)
	h.writeTransition(w, alert, err)
}

func (h *AdminSafetyHandler) writeTransition(w http.ResponseWriter, alert *safety.Alert, err error) {
	if errors.Is(err, safety.ErrAlertNotActionable) {
		response.Error(w, "Alert was already handled", http.StatusConflict)
		return
	}
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to update safety alert")
		response.Error(w, "Failed to update safety alert", http.StatusInternalServerError)
		return
	}
	h.WSManager.BroadcastToRole("admin", websocket.EventSafetyAlertUpdated, alert)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(alert)
}
