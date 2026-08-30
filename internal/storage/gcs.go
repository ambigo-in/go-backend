package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"

	"cloud.google.com/go/storage"
)

type StorageService struct {
	client     *storage.Client
	bucketName string
}

func NewStorageService(ctx context.Context, bucketName string) (*StorageService, error) {
	if bucketName == "" {
		bucketName = "ambigo-driver-docs"
	}
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize gcs client: %w", err)
	}
	return &StorageService{
		client:     client,
		bucketName: bucketName,
	}, nil
}

// UploadBase64IfImage checks if data is a base64 string.
// If it is base64, decodes it, uploads it to GCS at objectPath, and returns the public HTTPS URL.
// If it is already an http/https URL or empty, returns data unchanged.
func (s *StorageService) UploadBase64IfImage(ctx context.Context, objectPath string, data string) (string, error) {
	data = strings.TrimSpace(data)
	if data == "" {
		return "", nil
	}
	// Skip upload if already a URL
	if strings.HasPrefix(data, "http://") || strings.HasPrefix(data, "https://") {
		return data, nil
	}

	// Handle optional data URI prefix (e.g. data:image/jpeg;base64,...)
	rawBase64 := data
	contentType := "image/jpeg"
	if idx := strings.Index(data, ";base64,"); idx != -1 {
		header := data[:idx]
		if strings.HasPrefix(header, "data:") {
			contentType = strings.TrimPrefix(header, "data:")
		}
		rawBase64 = data[idx+8:]
	}

	decoded, err := base64.StdEncoding.DecodeString(rawBase64)
	if err != nil {
		// Fallback to URL-safe base64 decoding
		decoded, err = base64.URLEncoding.DecodeString(rawBase64)
		if err != nil {
			return "", fmt.Errorf("invalid base64 image data: %w", err)
		}
	}

	// Detect MIME type if not specified
	if contentType == "" || contentType == "image/jpeg" {
		mime := http.DetectContentType(decoded)
		if strings.HasPrefix(mime, "image/") {
			contentType = mime
		}
	}

	bucket := s.client.Bucket(s.bucketName)
	obj := bucket.Object(objectPath)

	writer := obj.NewWriter(ctx)
	writer.ContentType = contentType
	writer.CacheControl = "public, max-age=31536000"

	if _, err := io.Copy(writer, bytes.NewReader(decoded)); err != nil {
		_ = writer.Close()
		return "", fmt.Errorf("failed to upload image bytes to GCS: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to complete GCS upload: %w", err)
	}

	url := fmt.Sprintf("https://storage.googleapis.com/%s/%s", s.bucketName, objectPath)
	return url, nil
}
