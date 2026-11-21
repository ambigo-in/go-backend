package middleware

import (
	"net/http"
	"os"
	"strings"
)

var allowedOrigins = []string{
	"https://ambigo.in",
	"https://admin.ambigo.in",
	"https://hospital.ambigo.in",
	"https://hospital-ambigo.netlify.app",
	"https://ambigo-hospital-portal.onrender.com",
	"https://admin-website-ops.onrender.com",
	"https://ambigo-server-559193701066.asia-south1.run.app",
	"https://go-backend-viu3.onrender.com",
	"http://localhost:",
	"capacitor://",
}

func isAllowedOrigin(origin string) bool {
	if origin == "" {
		return true // non-browser (Flutter) — no Origin header
	}
	for _, ao := range allowedOrigins {
		if origin == ao || (len(origin) >= len(ao) && origin[:len(ao)] == ao) {
			return true
		}
	}
	// Allow Netlify preview deploys: e.g. https://deploy-preview-123--hospital-ambigo.netlify.app
	if strings.HasSuffix(origin, ".netlify.app") && strings.Contains(origin, "hospital-ambigo") {
		return true
	}
	// Allow any subdomain of ambigo.in (hospital/admin etc)
	if strings.HasSuffix(origin, ".ambigo.in") {
		return true
	}
	// Allow Render deployments (admin-website-ops.onrender.com, etc.)
	if strings.HasSuffix(origin, ".onrender.com") {
		return true
	}
	// Allow any custom origins specified via ALLOWED_ORIGINS env var (comma-separated)
	if custom := os.Getenv("ALLOWED_ORIGINS"); custom != "" {
		for _, co := range strings.Split(custom, ",") {
			co = strings.TrimSpace(co)
			if co != "" && (origin == co || strings.HasPrefix(origin, co)) {
				return true
			}
		}
	}
	return false
}

func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if isAllowedOrigin(origin) {
			if origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, X-Request-ID")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
