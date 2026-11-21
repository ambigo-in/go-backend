package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestIsAllowedOrigin(t *testing.T) {
	tests := []struct {
		origin string
		want   bool
	}{
		{"", true},
		{"https://ambigo.in", true},
		{"https://admin.ambigo.in", true},
		{"https://admin-website-ops.onrender.com", true},
		{"https://another-preview.onrender.com", true},
		{"http://localhost:5180", true},
		{"http://localhost:8080", true},
		{"capacitor://localhost", true},
		{"https://malicious-site.com", false},
		{"https://render.com.evil.com", false},
	}

	for _, tt := range tests {
		got := isAllowedOrigin(tt.origin)
		if got != tt.want {
			t.Errorf("isAllowedOrigin(%q) = %v, want %v", tt.origin, got, tt.want)
		}
	}
}

func TestCORS_PreflightOptions(t *testing.T) {
	handler := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/api/v2/admin/login", nil)
	req.Header.Set("Origin", "https://admin-website-ops.onrender.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type, X-API-Key")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 No Content for OPTIONS, got %d", rec.Code)
	}

	allowOrigin := rec.Header().Get("Access-Control-Allow-Origin")
	if allowOrigin != "https://admin-website-ops.onrender.com" {
		t.Errorf("expected Access-Control-Allow-Origin to be %q, got %q", "https://admin-website-ops.onrender.com", allowOrigin)
	}
}

func TestCORS_CustomEnvOrigins(t *testing.T) {
	os.Setenv("ALLOWED_ORIGINS", "https://custom-partner.com, https://another-portal.org")
	defer os.Unsetenv("ALLOWED_ORIGINS")

	if !isAllowedOrigin("https://custom-partner.com") {
		t.Error("expected custom origin to be allowed")
	}
	if !isAllowedOrigin("https://another-portal.org") {
		t.Error("expected second custom origin to be allowed")
	}
	if isAllowedOrigin("https://unknown.com") {
		t.Error("expected unknown origin to be blocked")
	}
}
