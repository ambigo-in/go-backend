package middleware

import (
	"context"
	"net/http"
	"strings"

	"ambigo-backend/api/response"
	"ambigo-backend/internal/auth"
)

// APIKeyAuth creates a middleware that validates the X-API-Key header.
func APIKeyAuth(expectedKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("X-API-Key")
			if key == "" || key != expectedKey {
				response.Error(w, "Forbidden: invalid API key", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type contextKey string

const (
	UserIDKey   contextKey = "userID"
	UserRoleKey contextKey = "userRole"
)

// JWTAuth creates a middleware that validates a Bearer token
func JWTAuth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				response.Error(w, "Missing Authorization Header", http.StatusUnauthorized)
				return
			}

			// Expect "Bearer <token>"
			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || parts[0] != "Bearer" {
				response.Error(w, "Invalid Authorization Header format", http.StatusUnauthorized)
				return
			}

			tokenString := parts[1]
			claims, err := auth.ValidateToken(tokenString, secret)
			if err != nil {
				response.Error(w, "Invalid or expired token", http.StatusUnauthorized)
				return
			}

			// Add the extracted ID and Role to the request context
			ctx := context.WithValue(r.Context(), UserIDKey, claims.ID)
			ctx = context.WithValue(ctx, UserRoleKey, claims.Role)
			
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole is a middleware that ensures the JWT role matches the required role
func RequireRole(requiredRole string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role, ok := r.Context().Value(UserRoleKey).(string)
		if !ok || role != requiredRole {
			response.Error(w, "Forbidden: insufficient permissions", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
