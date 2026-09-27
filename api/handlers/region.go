package handlers

import (
	"encoding/json"
	"net/http"

	"ambigo-backend/api/response"
	"ambigo-backend/internal/pricing"
)

type RegionHandler struct {
	Regions *pricing.RegionStore
}

func NewRegionHandler(rs *pricing.RegionStore) *RegionHandler {
	return &RegionHandler{Regions: rs}
}

func (h *RegionHandler) HandleListRegions(w http.ResponseWriter, r *http.Request) {
	list, err := h.Regions.ListRegions(r.Context())
	if err != nil {
		response.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response.Success(w, http.StatusOK, map[string]interface{}{"regions": list})
}

// HandleSearchRegions is a local text search over seeded regions — instant,
// no network. Used by the admin picker.
func (h *RegionHandler) HandleSearchRegions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	places, err := h.Regions.SearchRegions(r.Context(), req.Query)
	if err != nil {
		response.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response.Success(w, http.StatusOK, map[string]interface{}{"places": places})
}

// HandleFetchRegionFromOSM now creates circle regions only (villages without a
// seeded border). Seeded state/district borders come from data/areas_ap_tg.geojson
// via cmd/seed_regions — no live map calls, so rate limits are impossible.
func (h *RegionHandler) HandleFetchRegionFromOSM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string  `json:"name"`
		Lat     float64 `json:"lat"`
		Lng     float64 `json:"lng"`
		RadiusM float64 `json:"radius_m"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		response.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if req.Lat == 0 && req.Lng == 0 {
		response.Error(w, "lat/lng required for circle regions", http.StatusBadRequest)
		return
	}
	if req.RadiusM <= 0 {
		req.RadiusM = 30000
	}
	cells, err := pricing.FillCellsForCircle(req.Lat, req.Lng, req.RadiusM)
	if err != nil {
		response.Error(w, "Could not build circle: "+err.Error(), http.StatusBadGateway)
		return
	}
	region, err := h.Regions.CreateRegionFull(r.Context(), req.Name, "", 0, "", nil, cells, pricing.RegionCellRes, "circle", nil, "circle")
	if err != nil {
		response.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response.Success(w, http.StatusCreated, map[string]interface{}{"region": region, "cells": len(cells), "res": pricing.RegionCellRes, "matched": "circle"})
}

// HandleRefreshRegion recomputes H3 cells from the stored polygon — fully
// local (e.g. after a resolution change). Circle regions have no polygon:
// delete + re-add.
func (h *RegionHandler) HandleRefreshRegion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		response.Error(w, "id required", http.StatusBadRequest)
		return
	}
	existing, err := h.Regions.GetRegion(r.Context(), req.ID)
	if err != nil || existing == nil {
		response.Error(w, "region not found", http.StatusNotFound)
		return
	}
	if existing.OsmID == 0 && len(existing.Cells) > 0 {
		// Circle or legacy row without polygon — nothing to recompute from.
		response.Success(w, http.StatusOK, map[string]interface{}{"region": existing, "cells": len(existing.Cells), "res": existing.CellRes, "matched": "unchanged"})
		return
	}
	rows, err := h.Regions.PolygonFor(r.Context(), req.ID)
	if err != nil || len(rows) == 0 {
		response.Error(w, "no stored polygon — delete + re-add", http.StatusBadRequest)
		return
	}
	cells, res, err := pricing.FillCellsForGeometryRes(rows)
	if err != nil || len(cells) == 0 {
		response.Error(w, "Could not build H3 cover: "+err.Error(), http.StatusBadGateway)
		return
	}
	updated, err := h.Regions.RefreshRegionCells(r.Context(), req.ID, cells, res, rows)
	if err != nil {
		response.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response.Success(w, http.StatusOK, map[string]interface{}{"region": updated, "cells": len(cells), "res": res, "matched": updated.DisplayName})
}

func (h *RegionHandler) HandleDeleteRegion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		response.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if err := h.Regions.DeleteRegion(r.Context(), id); err != nil {
		response.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response.Success(w, http.StatusOK, map[string]string{"detail": "deleted"})
}

func (h *RegionHandler) HandleUpsertRegionPrice(w http.ResponseWriter, r *http.Request) {
	var p pricing.RegionPrice
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if p.RegionID == "" || p.AmbTypeID == "" {
		response.Error(w, "region_id + amb_type_id required", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &p) {
		return
	}
	if err := h.Regions.UpsertRegionPrice(r.Context(), &p); err != nil {
		response.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response.Success(w, http.StatusOK, map[string]string{"detail": "saved"})
}

func (h *RegionHandler) HandleGetRegionPrice(w http.ResponseWriter, r *http.Request) {
	regionID := r.URL.Query().Get("region_id")
	ambTypeID := r.URL.Query().Get("amb_type_id")
	if regionID == "" || ambTypeID == "" {
		response.Error(w, "region_id + amb_type_id required", http.StatusBadRequest)
		return
	}
	p, err := h.Regions.GetRegionPrice(r.Context(), regionID, ambTypeID)
	if err != nil {
		response.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if p == nil {
		// Parent-state fallback for the editor preview (mirrors ride pricing).
		if region, rerr := h.Regions.GetRegion(r.Context(), regionID); rerr == nil && region != nil && region.ParentID != nil && *region.ParentID != "" {
			if pp, perr := h.Regions.GetRegionPrice(r.Context(), *region.ParentID, ambTypeID); perr == nil && pp != nil {
				response.Success(w, http.StatusOK, map[string]interface{}{"price": pp, "inherited_from": *region.ParentID})
				return
			}
		}
		response.Error(w, "not found, using global", http.StatusNotFound)
		return
	}
	response.Success(w, http.StatusOK, p)
}
