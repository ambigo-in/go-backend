package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"ambigo-backend/api/response"
	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/adminstats"
	"ambigo-backend/internal/location"
	"ambigo-backend/internal/logger"
)

type AdminStatsHandler struct {
	Store         *adminstats.Store
	LocationStore *location.MemoryStore
	AuditStore    *admin.AuditStore
}

func analyticsDays(w http.ResponseWriter, r *http.Request) (int, bool) {
	var req struct {
		Range string `json:"range"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		response.Error(w, "Invalid request body", http.StatusBadRequest)
		return 0, false
	}
	switch req.Range {
	case "", "14d":
		return 14, true
	case "7d":
		return 7, true
	case "30d":
		return 30, true
	default:
		response.Error(w, "range must be 7d, 14d or 30d", http.StatusBadRequest)
		return 0, false
	}
}

func (h *AdminStatsHandler) HandleDashboardStats(w http.ResponseWriter, r *http.Request) {
	days, ok := analyticsDays(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	at := time.Now()
	snapshot, err := h.Store.Snapshot(ctx, at)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to read admin dashboard snapshot")
		response.Error(w, "Failed to load dashboard", http.StatusInternalServerError)
		return
	}
	trends, err := h.Store.Analytics(ctx, days, at)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to read admin dashboard trends")
		response.Error(w, "Failed to load dashboard", http.StatusInternalServerError)
		return
	}
	available, busy := h.LocationStore.OnlineCounts()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(struct {
		*adminstats.Snapshot
		Trends      *adminstats.Analytics `json:"trends"`
		Drivers     map[string]int        `json:"drivers"`
		GeneratedAt time.Time             `json:"generated_at"`
		Timezone    string                `json:"timezone"`
	}{snapshot, trends, map[string]int{"available": available, "busy": busy, "total": available + busy}, at, "Asia/Kolkata"})
}

func (h *AdminStatsHandler) HandleFleetSnapshot(w http.ResponseWriter, r *http.Request) {
	at := time.Now()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(struct {
		Drivers           []location.DriverLocation `json:"drivers"`
		GeneratedAt       time.Time                 `json:"generated_at"`
		StaleAfterSeconds int                       `json:"stale_after_seconds"`
	}{h.LocationStore.FleetSnapshot(), at, 15})
}

func (h *AdminStatsHandler) HandleActivity(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := h.AuditStore.ListActivity(r.Context(), limit)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to read admin activity")
		response.Error(w, "Failed to load activity", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"events": events, "generated_at": time.Now(),
	})
}

func (h *AdminStatsHandler) HandleAnalyticsSummary(w http.ResponseWriter, r *http.Request) {
	days, ok := analyticsDays(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	at := time.Now()
	data, err := h.Store.Analytics(ctx, days, at)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to read admin analytics")
		response.Error(w, "Failed to load analytics", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(struct {
		*adminstats.Analytics
		GeneratedAt time.Time `json:"generated_at"`
		Timezone    string    `json:"timezone"`
	}{data, at, "Asia/Kolkata"})
}
