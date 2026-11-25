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
	if strings.Contains(objectPath, "..") {
		response.Error(w, "Invalid path query parameter", http.StatusBadRequest)
		return
	}

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
	// 1. Admin can view any document path (including sensitive KYC like dl.jpg, rc.jpg, poi.jpg).
	// 2. Owner can view any document path containing their own ID (e.g. driver viewing their own KYC).
	// 3. Any authenticated user (passenger, driver, hospital staff) can view public display media
	//    such as driver profile photos (portrait.jpg), ambulance exterior/interior photos (amb_front.jpg,
	//    amb_inside.jpg), and ambulance category icons.
	// Sensitive identity & KYC documents remain strictly restricted to admin and owner.
	isAdmin := claims.Role == "admin" || claims.AdminRole != ""
	isOwner := claims.ID != "" && strings.Contains(objectPath, claims.ID)
	isDisplayMedia := isPublicDisplayMedia(objectPath)

	if !isAdmin && !isOwner && !isDisplayMedia {
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

// isPublicDisplayMedia returns true for media assets intended for general authenticated
// display (e.g. driver portrait/avatar, ambulance vehicle exterior/interior photos,
// vehicle category icons, hospital logos), while sensitive KYC/identity documents (driver's license
// 'dl.jpg', registration 'rc.jpg', proof of identity 'poi.jpg') remain strictly protected.
func isPublicDisplayMedia(path string) bool {
	cleanPath := strings.ToLower(strings.TrimPrefix(path, "/"))

	// Check path traversal attempt
	if strings.Contains(cleanPath, "..") {
		return false
	}

	// Allowed public prefixes (ambulance types, logos, public assets)
	publicPrefixes := []string{
		"ambulances/",
		"ambulance_types/",
		"types/",
		"hospitals/logos/",
		"public/",
	}
	for _, prefix := range publicPrefixes {
		if strings.HasPrefix(cleanPath, prefix) {
			return true
		}
	}

	// Extract the base filename (after last slash)
	filename := cleanPath
	if idx := strings.LastIndex(cleanPath, "/"); idx != -1 {
		filename = cleanPath[idx+1:]
	}

	// Allowed display file names (avatars, portraits, vehicle photos)
	displayFiles := []string{
		"portrait.jpg", "portrait.jpeg", "portrait.png", "portrait.webp",
		"avatar.jpg", "avatar.jpeg", "avatar.png", "avatar.webp",
		"amb_front.jpg", "amb_front.jpeg", "amb_front.png", "amb_front.webp",
		"amb_inside.jpg", "amb_inside.jpeg", "amb_inside.png", "amb_inside.webp",
	}
	for _, df := range displayFiles {
		if filename == df {
			return true
		}
	}

	return false
}
