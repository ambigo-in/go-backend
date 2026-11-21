package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ambigo-backend/api/response"
	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/logger"
)

type AdminAuditHandler struct {
	AuditStore *admin.AuditStore
}

type ListAuditLogsRequest struct {
	Domain    string     `json:"domain"`
	Channels  []string   `json:"channels"`
	Search    string     `json:"search"`
	ActorID   string     `json:"actor_id"`
	TargetID  string     `json:"target_id"`
	RequestID string     `json:"request_id"`
	DateFrom  *time.Time `json:"date_from"`
	DateTo    *time.Time `json:"date_to"`
	Limit     int        `json:"limit"`
	Offset    int        `json:"offset"`
	SortOrder string     `json:"sort_order"`
}

type AuditActorResponse struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Role string `json:"role,omitempty"`
	Type string `json:"type"` // "staff", "driver", "user", "system"
}

type AuditTargetResponse struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"` // "staff", "ride", "driver", "hospital", "ambulance_type", "offer", "user", "payment", "referral"
}

type AuditItemResponse struct {
	ID        string                 `json:"id"`
	EventType string                 `json:"event_type"`
	Channel   string                 `json:"channel"`
	Domain    string                 `json:"domain"`
	Actor     AuditActorResponse     `json:"actor"`
	Target    AuditTargetResponse    `json:"target"`
	RequestID string                 `json:"request_id,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
	Payload   map[string]interface{} `json:"payload"`
}

type ListAuditLogsResponse struct {
	Items  []AuditItemResponse `json:"items"`
	Total  int64               `json:"total"`
	Limit  int                 `json:"limit"`
	Offset int                 `json:"offset"`
}

func (h *AdminAuditHandler) HandleListAuditLogs(w http.ResponseWriter, r *http.Request) {
	if h.AuditStore == nil {
		logger.Log.Error().Msg("AuditStore is not configured on AdminAuditHandler")
		response.Error(w, "Audit store unavailable", http.StatusInternalServerError)
		return
	}

	var req ListAuditLogsRequest
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*64)).Decode(&req)
	}

	limit := req.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}

	filter := admin.AuditLogFilter{
		Domain:    req.Domain,
		Channels:  req.Channels,
		Search:    req.Search,
		ActorID:   req.ActorID,
		TargetID:  req.TargetID,
		RequestID: req.RequestID,
		DateFrom:  req.DateFrom,
		DateTo:    req.DateTo,
		Limit:     limit,
		Offset:    offset,
		SortOrder: req.SortOrder,
	}

	result, err := h.AuditStore.ListAuditLogs(r.Context(), filter)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to list audit logs")
		response.Error(w, "Failed to retrieve audit logs", http.StatusInternalServerError)
		return
	}

	items := make([]AuditItemResponse, 0, len(result.Items))
	for _, rec := range result.Items {
		var raw map[string]interface{}
		_ = json.Unmarshal(rec.Payload, &raw)
		if raw == nil {
			raw = make(map[string]interface{})
		}

		domain := admin.ChannelDomain(rec.Channel)
		actor, target := extractActorAndTarget(rec.Channel, raw)

		items = append(items, AuditItemResponse{
			ID:        rec.ID,
			EventType: rec.EventType,
			Channel:   rec.Channel,
			Domain:    domain,
			Actor:     actor,
			Target:    target,
			RequestID: rec.RequestID,
			CreatedAt: rec.CreatedAt,
			Payload:   raw,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(ListAuditLogsResponse{
		Items:  items,
		Total:  result.Total,
		Limit:  result.Limit,
		Offset: result.Offset,
	})
}

type AuditSummaryRequest struct {
	DateFrom *time.Time `json:"date_from"`
	DateTo   *time.Time `json:"date_to"`
}

func (h *AdminAuditHandler) HandleAuditSummary(w http.ResponseWriter, r *http.Request) {
	if h.AuditStore == nil {
		logger.Log.Error().Msg("AuditStore is not configured on AdminAuditHandler")
		response.Error(w, "Audit store unavailable", http.StatusInternalServerError)
		return
	}

	var req AuditSummaryRequest
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*16)).Decode(&req)
	}

	summary, err := h.AuditStore.GetAuditSummary(r.Context(), req.DateFrom, req.DateTo)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to get audit summary")
		response.Error(w, "Failed to retrieve audit summary", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(summary)
}

// LegacyAuditLog matches the schema expected by ambigo-admin-portal (AuditLog interface)
type LegacyAuditLog struct {
	ID             string  `json:"id"`
	Action         string  `json:"action"`
	DriverID       *string `json:"driver_id"`
	DriverName     *string `json:"driver_name"`
	TargetID       *string `json:"target_id"`
	TargetName     *string `json:"target_name"`
	TargetType     *string `json:"target_type"`
	ApprovedBy     string  `json:"approved_by"`
	ApprovedByName string  `json:"approved_by_name"`
	ApprovedByRole string  `json:"approved_by_role"`
	ApprovedAt     string  `json:"approved_at"`
	Notes          *string `json:"notes"`
}

func (h *AdminAuditHandler) HandleLegacyAuditLogs(w http.ResponseWriter, r *http.Request) {
	if h.AuditStore == nil {
		logger.Log.Error().Msg("AuditStore is not configured on AdminAuditHandler")
		response.Error(w, "Audit store unavailable", http.StatusInternalServerError)
		return
	}

	query := r.URL.Query()
	targetType := strings.TrimSpace(query.Get("target_type"))
	search := strings.TrimSpace(query.Get("search"))

	limit := 200
	if l := query.Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}
	skip := 0
	if s := query.Get("skip"); s != "" {
		if parsed, err := strconv.Atoi(s); err == nil && parsed >= 0 {
			skip = parsed
		}
	}

	filter := admin.AuditLogFilter{
		Search: search,
		Limit:  limit,
		Offset: skip,
	}

	if targetType != "" {
		switch strings.ToLower(targetType) {
		case "staff":
			filter.Domain = "admin"
			filter.Channels = []string{"admin:staff_created", "admin:staff_updated", "admin:staff_toggled", "admin:staff_deleted"}
		case "hospital":
			filter.Domain = "admin"
			filter.Channels = []string{"admin:hospital_added", "admin:hospital_updated", "admin:hospital_deleted"}
		case "ambulance_type":
			filter.Domain = "admin"
			filter.Channels = []string{"admin:ambulance_type_created", "admin:ambulance_type_deleted"}
		case "driver":
			filter.Channels = []string{"auth:driver_approved", "admin:driver_rejected"}
		case "role":
			filter.Domain = "admin"
		}
	}

	result, err := h.AuditStore.ListAuditLogs(r.Context(), filter)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to list legacy audit logs")
		response.Error(w, "Failed to retrieve audit logs", http.StatusInternalServerError)
		return
	}

	legacyItems := make([]LegacyAuditLog, 0, len(result.Items))
	for _, rec := range result.Items {
		var raw map[string]interface{}
		_ = json.Unmarshal(rec.Payload, &raw)
		if raw == nil {
			raw = make(map[string]interface{})
		}

		actor, target := extractActorAndTarget(rec.Channel, raw)

		var driverID *string
		var driverName *string
		if did, ok := raw["driver_id"].(string); ok && did != "" {
			driverID = &did
			if dname, ok := raw["driver_name"].(string); ok && dname != "" {
				driverName = &dname
			}
		}

		var targetID *string
		var targetName *string
		var targetTypePtr *string
		if target.ID != "" {
			targetID = &target.ID
		}
		if target.Name != "" {
			targetName = &target.Name
		}
		if target.Type != "" {
			targetTypePtr = &target.Type
		}

		var notes *string
		if n, ok := raw["action"].(string); ok && n != "" {
			notes = &n
		} else if rsn, ok := raw["reason"].(string); ok && rsn != "" {
			notes = &rsn
		}

		approvedBy := actor.ID
		if approvedBy == "" {
			approvedBy = "system"
		}
		approvedByName := actor.Name
		if approvedByName == "" {
			approvedByName = "System"
		}
		approvedByRole := actor.Role
		if approvedByRole == "" {
			approvedByRole = actor.Type
		}

		legacyItems = append(legacyItems, LegacyAuditLog{
			ID:             rec.ID,
			Action:         rec.EventType,
			DriverID:       driverID,
			DriverName:     driverName,
			TargetID:       targetID,
			TargetName:     targetName,
			TargetType:     targetTypePtr,
			ApprovedBy:     approvedBy,
			ApprovedByName: approvedByName,
			ApprovedByRole: approvedByRole,
			ApprovedAt:     rec.CreatedAt.Format(time.RFC3339),
			Notes:          notes,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(legacyItems)
}

func extractActorAndTarget(channel string, raw map[string]interface{}) (AuditActorResponse, AuditTargetResponse) {
	actor := AuditActorResponse{Type: "system", Name: "System"}
	target := AuditTargetResponse{}

	// Actor extraction
	if aid, ok := raw["actor_id"].(string); ok && aid != "" {
		actor.Type = "staff"
		actor.ID = aid
		if name, ok := raw["actor_name"].(string); ok && name != "" {
			actor.Name = name
		} else {
			actor.Name = "Co-Admin"
		}
		if role, ok := raw["role"].(string); ok {
			actor.Role = role
		}
	} else if adminID, ok := raw["admin_id"].(string); ok && strings.HasPrefix(channel, "admin:") {
		actor.Type = "staff"
		actor.ID = adminID
		if name, ok := raw["name"].(string); ok && name != "" {
			actor.Name = name
		} else if username, ok := raw["username"].(string); ok && username != "" {
			actor.Name = username
		}
		if role, ok := raw["role"].(string); ok {
			actor.Role = role
		}
	} else if driverID, ok := raw["driver_id"].(string); ok && (strings.HasPrefix(channel, "ride:") || strings.HasPrefix(channel, "safety:") || strings.HasPrefix(channel, "wallet:")) {
		actor.Type = "driver"
		actor.ID = driverID
		if name, ok := raw["driver_name"].(string); ok && name != "" {
			actor.Name = name
		} else if mobile, ok := raw["mobile"].(string); ok && mobile != "" {
			actor.Name = mobile
		} else {
			actor.Name = "Driver"
		}
	} else if userID, ok := raw["user_id"].(string); ok && (strings.HasPrefix(channel, "ride:") || strings.HasPrefix(channel, "payment:") || strings.HasPrefix(channel, "auth:")) {
		actor.Type = "user"
		actor.ID = userID
		if name, ok := raw["name"].(string); ok && name != "" {
			actor.Name = name
		} else if mobile, ok := raw["mobile"].(string); ok && mobile != "" {
			actor.Name = mobile
		} else {
			actor.Name = "User"
		}
	}

	// Target extraction
	if rideID, ok := raw["ride_id"].(string); ok && rideID != "" {
		target.Type = "ride"
		target.ID = rideID
	} else if staffID, ok := raw["admin_id"].(string); ok && strings.HasPrefix(channel, "admin:staff") {
		target.Type = "staff"
		target.ID = staffID
		if username, ok := raw["username"].(string); ok {
			target.Name = username
		}
	} else if hospID, ok := raw["hospital_id"].(string); ok && hospID != "" {
		target.Type = "hospital"
		target.ID = hospID
		if name, ok := raw["name"].(string); ok {
			target.Name = name
		}
	} else if ambID, ok := raw["amb_type_id"].(string); ok && ambID != "" {
		target.Type = "ambulance_type"
		target.ID = ambID
		if name, ok := raw["name"].(string); ok {
			target.Name = name
		}
	} else if offerID, ok := raw["offer_id"].(string); ok && offerID != "" {
		target.Type = "offer"
		target.ID = offerID
		if desc, ok := raw["description"].(string); ok {
			target.Name = desc
		}
	} else if drvID, ok := raw["driver_id"].(string); ok && drvID != "" {
		target.Type = "driver"
		target.ID = drvID
		if name, ok := raw["driver_name"].(string); ok {
			target.Name = name
		}
	} else if pmtID, ok := raw["payment_id"].(string); ok && pmtID != "" {
		target.Type = "payment"
		target.ID = pmtID
	} else if recID, ok := raw["record_id"].(string); ok && recID != "" {
		target.Type = "referral"
		target.ID = recID
	} else if uid, ok := raw["user_id"].(string); ok && uid != "" {
		target.Type = "user"
		target.ID = uid
	}

	return actor, target
}
