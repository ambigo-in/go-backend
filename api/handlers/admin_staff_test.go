package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ambigo-backend/api/middleware"
	"ambigo-backend/internal/ids"
)

func TestHandleCreateStaff_Validation(t *testing.T) {
	h := &AdminHandler{}

	tests := []struct {
		name       string
		payload    map[string]interface{}
		callerRole string
		wantStatus int
	}{
		{
			name: "username too short",
			payload: map[string]interface{}{
				"username": "ab",
				"name":     "Test Admin",
				"password": "password123",
				"role":     "admin",
			},
			callerRole: "super_admin",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "password too short",
			payload: map[string]interface{}{
				"username": "testadmin",
				"name":     "Test Admin",
				"password": "123",
				"role":     "admin",
			},
			callerRole: "super_admin",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "invalid role",
			payload: map[string]interface{}{
				"username": "testadmin",
				"name":     "Test Admin",
				"password": "password123",
				"role":     "invented_role",
			},
			callerRole: "super_admin",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "non-super_admin creating super_admin forbidden",
			payload: map[string]interface{}{
				"username": "testadmin",
				"name":     "Test Admin",
				"password": "password123",
				"role":     "super_admin",
			},
			callerRole: "admin",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := json.Marshal(tt.payload)
			req := httptest.NewRequest(http.MethodPost, "/api/v2/admin/staff/create", bytes.NewReader(b))
			ctx := context.WithValue(req.Context(), middleware.AdminRoleKey, tt.callerRole)
			ctx = context.WithValue(ctx, middleware.UserIDKey, ids.New())
			req = req.WithContext(ctx)

			rr := httptest.NewRecorder()
			h.HandleCreateStaff(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d; body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
		})
	}
}

func TestHandleToggleStaff_Validation(t *testing.T) {
	h := &AdminHandler{}
	callerID := ids.New()

	// Cannot toggle self
	payload := map[string]interface{}{
		"id": callerID,
	}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v2/admin/staff/toggle", bytes.NewReader(b))
	ctx := context.WithValue(req.Context(), middleware.UserIDKey, callerID)
	ctx = context.WithValue(ctx, middleware.AdminRoleKey, "super_admin")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.HandleToggleStaff(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when toggling self, got %d", rr.Code)
	}

	// Invalid ID
	payloadInvalid := map[string]interface{}{
		"id": "not-a-uuid",
	}
	bInv, _ := json.Marshal(payloadInvalid)
	reqInv := httptest.NewRequest(http.MethodPost, "/api/v2/admin/staff/toggle", bytes.NewReader(bInv))
	ctxInv := context.WithValue(reqInv.Context(), middleware.UserIDKey, callerID)
	reqInv = reqInv.WithContext(ctxInv)

	rrInv := httptest.NewRecorder()
	h.HandleToggleStaff(rrInv, reqInv)

	if rrInv.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid ID, got %d", rrInv.Code)
	}
}

func TestHandleDeleteStaff_Validation(t *testing.T) {
	h := &AdminHandler{}
	callerID := ids.New()

	// Cannot delete self
	payload := map[string]interface{}{
		"id": callerID,
	}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v2/admin/staff/delete", bytes.NewReader(b))
	ctx := context.WithValue(req.Context(), middleware.UserIDKey, callerID)
	ctx = context.WithValue(ctx, middleware.AdminRoleKey, "super_admin")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.HandleDeleteStaff(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when deleting self, got %d", rr.Code)
	}
}

func TestHandleAdminRegister_Validation(t *testing.T) {
	h := &AdminHandler{}

	tests := []struct {
		name       string
		payload    map[string]interface{}
		wantStatus int
	}{
		{
			name: "missing name",
			payload: map[string]interface{}{
				"username": "testadmin",
				"password": "password123",
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "username too short",
			payload: map[string]interface{}{
				"name":     "Test Admin",
				"username": "ab",
				"password": "password123",
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "password too short",
			payload: map[string]interface{}{
				"name":     "Test Admin",
				"username": "testadmin",
				"password": "123",
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := json.Marshal(tt.payload)
			req := httptest.NewRequest(http.MethodPost, "/api/v2/admin/register", bytes.NewReader(b))
			rr := httptest.NewRecorder()
			h.HandleAdminRegister(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d; body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
		})
	}
}

func TestHandleAdminMe_Unauthorized(t *testing.T) {
	h := &AdminHandler{}

	req := httptest.NewRequest(http.MethodGet, "/api/v2/admin/me", nil)
	rr := httptest.NewRecorder()
	h.HandleAdminMe(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized without admin ID context, got %d", rr.Code)
	}
}
