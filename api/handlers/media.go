package handlers

import (
	"io"
	"net/http"
	"strings"

	"ambigo-backend/api/response"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/storage"
)

type MediaHandler struct {
	StorageSvc *storage.StorageService
	JWTSecret  string
}

func NewMediaHandler(storageSvc *storage.StorageService, jwtSecret string) *MediaHandler {
	return &MediaHandler{
		StorageSvc: storageSvc,
		JWTSecret:  jwtSecret,
	}
}

// HandleViewMedia proxies and streams private GCS media objects to authenticated clients.
// GET /api/v2/media/view?path=drivers/{driver_id}/{doc}.jpg&token=<jwt_token>
func (h *MediaHandler) HandleViewMedia(w http.ResponseWriter, r *http.Request) {
	if h.StorageSvc == nil {
		response.Error(w, "Storage service uninitialized", http.StatusInternalServerError)
		return
	}

	objectPath := r.URL.Query().Get("path")
	if objectPath == "" {
		response.Error(w, "Missing path query parameter", http.StatusBadRequest)
		return
	}
	objectPath = strings.TrimPrefix(objectPath, "/")

	// Validate JWT token from Authorization header or ?token= query parameter
	var tokenString string
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		tokenString = strings.TrimPrefix(authHeader, "Bearer ")
	} else if tokenParam := r.URL.Query().Get("token"); tokenParam != "" {
		tokenString = tokenParam
	}

	if tokenString == "" {
		response.Error(w, "Missing authentication token", http.StatusUnauthorized)
		return
	}

	claims, err := auth.ValidateToken(tokenString, h.JWTSecret)
	if err != nil {
		response.Error(w, "Invalid or expired token", http.StatusUnauthorized)
		return
	}

	// Authorization Check:
	// Admin can view any document path.
	// Driver can only view document paths containing their own driver ID.
	isAdmin := claims.Role == "admin" || claims.AdminRole != ""
	isOwner := claims.ID != "" && strings.Contains(objectPath, claims.ID)

	if !isAdmin && !isOwner {
		response.Error(w, "Forbidden: you do not have permission to view this media", http.StatusForbidden)
		return
	}

	// Fetch reader stream from GCS
	reader, contentType, err := h.StorageSvc.StreamObject(r.Context(), objectPath)
	if err != nil {
		logger.Log.Error().Err(err).Str("path", objectPath).Msg("Failed to stream GCS media object")
		if strings.Contains(err.Error(), "not exist") {
			response.Error(w, "Media object not found", http.StatusNotFound)
			return
		}
		response.Error(w, "Failed to load media object", http.StatusInternalServerError)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=86400")

	if _, err := io.Copy(w, reader); err != nil {
		logger.Log.Error().Err(err).Str("path", objectPath).Msg("Error streaming bytes to client")
	}
}
