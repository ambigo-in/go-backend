package middleware

import (
	"context"
	"net/http"
	"strings"

	"ambigo-backend/api/response"
	"ambigo-backend/internal/auth"
	"github.com/golang-jwt/jwt/v5"
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
	UserIDKey      contextKey = "userID"
	UserRoleKey    contextKey = "userRole"
	AdminRoleKey   contextKey = "adminRole"
)

// JWTAuth creates a middleware that validates a Bearer token
// Optional audience/issuer are passed for token validation.
func JWTAuth(secret string, jwtOpts ...jwt.ParserOption) func(http.Handler) http.Handler {
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
			claims, err := auth.ValidateToken(tokenString, secret, jwtOpts...)
			if err != nil {
				response.Error(w, "Invalid or expired token", http.StatusUnauthorized)
				return
			}

			// Add the extracted ID and Role to the request context
			ctx := context.WithValue(r.Context(), UserIDKey, claims.ID)
			ctx = context.WithValue(ctx, UserRoleKey, claims.Role)
			ctx = context.WithValue(ctx, AdminRoleKey, claims.AdminRole)
			
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdminPermission is a middleware that checks the admin's stored DB role.
// If adminRole is empty (backward compat with existing tokens), all admins are allowed.
// Otherwise, the admin's stored role must be in the allowedRoles list.
func RequireAdminPermission(allowedRoles []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adminRole, ok := r.Context().Value(AdminRoleKey).(string)
		if !ok {
			response.Error(w, "Forbidden: admin role not found", http.StatusForbidden)
			return
		}
		// Backward compat: empty role = full access (existing tokens)
		if adminRole != "" {
			allowed := false
			for _, ar := range allowedRoles {
				if ar == adminRole {
					allowed = true
					break
				}
			}
			if !allowed {
				response.Error(w, "Forbidden: insufficient admin permissions", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAnyRole is a middleware that ensures the JWT role is in the allowed list
func RequireAnyRole(allowedRoles []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role, ok := r.Context().Value(UserRoleKey).(string)
		if !ok {
			response.Error(w, "Forbidden: insufficient permissions", http.StatusForbidden)
			return
		}
		for _, ar := range allowedRoles {
			if ar == role {
				next.ServeHTTP(w, r)
				return
			}
		}
		response.Error(w, "Forbidden: insufficient permissions", http.StatusForbidden)
	})
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
