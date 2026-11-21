package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ambigo-backend/internal/admin"
)

func TestAdminAuditHandler_NilStore(t *testing.T) {
	h := &AdminAuditHandler{AuditStore: nil}

	t.Run("HandleListAuditLogs nil store", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v2/admin/audit-logs/list", bytes.NewBufferString("{}"))
		w := httptest.NewRecorder()
		h.HandleListAuditLogs(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500, got %d", w.Code)
		}
	})

	t.Run("HandleAuditSummary nil store", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v2/admin/audit/summary", bytes.NewBufferString("{}"))
		w := httptest.NewRecorder()
		h.HandleAuditSummary(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500, got %d", w.Code)
		}
	})

	t.Run("HandleLegacyAuditLogs nil store", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/admin/audit-logs/list", nil)
		w := httptest.NewRecorder()
		h.HandleLegacyAuditLogs(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500, got %d", w.Code)
		}
	})
}

func TestExtractActorAndTarget(t *testing.T) {
	t.Run("Staff CRUD payload", func(t *testing.T) {
		raw := map[string]interface{}{
			"admin_id":   "staff-123",
			"username":   "ops.dispatcher",
			"role":       "admin",
			"action":     "created",
			"actor_id":   "admin-master",
			"actor_name": "Super Admin",
		}
		actor, target := extractActorAndTarget("admin:staff_created", raw)

		if actor.Type != "staff" || actor.ID != "admin-master" || actor.Name != "Super Admin" {
			t.Errorf("unexpected actor: %+v", actor)
		}
		if target.Type != "staff" || target.ID != "staff-123" || target.Name != "ops.dispatcher" {
			t.Errorf("unexpected target: %+v", target)
		}
	})

	t.Run("Ride lifecycle payload", func(t *testing.T) {
		raw := map[string]interface{}{
			"ride_id":     "ride-999",
			"driver_id":   "driver-888",
			"driver_name": "John Driver",
		}
		actor, target := extractActorAndTarget("ride:completed", raw)

		if actor.Type != "driver" || actor.ID != "driver-888" || actor.Name != "John Driver" {
			t.Errorf("unexpected actor: %+v", actor)
		}
		if target.Type != "ride" || target.ID != "ride-999" {
			t.Errorf("unexpected target: %+v", target)
		}
	})

	t.Run("Hospital payload", func(t *testing.T) {
		raw := map[string]interface{}{
			"hospital_id": "hosp-456",
			"name":        "Apollo Hospital",
			"actor_id":    "admin-root",
		}
		actor, target := extractActorAndTarget("admin:hospital_added", raw)

		if actor.Type != "staff" || actor.ID != "admin-root" {
			t.Errorf("unexpected actor: %+v", actor)
		}
		if target.Type != "hospital" || target.ID != "hosp-456" || target.Name != "Apollo Hospital" {
			t.Errorf("unexpected target: %+v", target)
		}
	})

	t.Run("System fallback", func(t *testing.T) {
		raw := map[string]interface{}{
			"system_alert": true,
		}
		actor, target := extractActorAndTarget("safety:system", raw)

		if actor.Type != "system" || actor.Name != "System" {
			t.Errorf("unexpected actor: %+v", actor)
		}
		if target.ID != "" {
			t.Errorf("expected empty target, got %+v", target)
		}
	})
}

func TestDomainChannelsAndChannelDomain(t *testing.T) {
	adminChannels := admin.DomainChannels("admin")
	if len(adminChannels) == 0 {
		t.Errorf("expected admin channels to be non-empty")
	}

	rideChannels := admin.DomainChannels("rides")
	if len(rideChannels) == 0 {
		t.Errorf("expected ride channels to be non-empty")
	}

	if dom := admin.ChannelDomain("admin:staff_created"); dom != "admin" {
		t.Errorf("expected admin, got %s", dom)
	}

	if dom := admin.ChannelDomain("ride:completed"); dom != "ride" {
		t.Errorf("expected ride, got %s", dom)
	}

	if dom := admin.ChannelDomain("auth:otp_requested"); dom != "auth" {
		t.Errorf("expected auth, got %s", dom)
	}

	if dom := admin.ChannelDomain("payment:completed"); dom != "finance" {
		t.Errorf("expected finance, got %s", dom)
	}

	if dom := admin.ChannelDomain("safety:stopped_warning"); dom != "safety" {
		t.Errorf("expected safety, got %s", dom)
	}
}

func TestAuditResponseSerialization(t *testing.T) {
	item := AuditItemResponse{
		ID:        "test-uuid",
		EventType: "admin_staff_created",
		Channel:   "admin:staff_created",
		Domain:    "admin",
		Actor: AuditActorResponse{
			ID:   "admin-1",
			Name: "Super Admin",
			Role: "super_admin",
			Type: "staff",
		},
		Target: AuditTargetResponse{
			ID:   "staff-2",
			Name: "dispatcher",
			Type: "staff",
		},
		RequestID: "req-12345",
		CreatedAt: time.Now(),
		Payload: map[string]interface{}{
			"username": "dispatcher",
		},
	}

	resp := ListAuditLogsResponse{
		Items:  []AuditItemResponse{item},
		Total:  1,
		Limit:  50,
		Offset: 0,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded ListAuditLogsResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Total != 1 || len(decoded.Items) != 1 {
		t.Errorf("unexpected decoded result: %+v", decoded)
	}
	if decoded.Items[0].Actor.Name != "Super Admin" {
		t.Errorf("unexpected actor name: %s", decoded.Items[0].Actor.Name)
	}
}
