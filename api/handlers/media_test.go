package handlers

import (
	"testing"
)

func TestIsPublicDisplayMedia(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		// Public display photos
		{"drivers/a572e8b4-9cc9-47ae-8933-6260c035b9d6/portrait.jpg", true},
		{"drivers/123/portrait.jpeg", true},
		{"drivers/123/portrait.png", true},
		{"drivers/123/amb_front.jpg", true},
		{"drivers/123/amb_inside.jpg", true},
		{"ambulances/als.jpg", true},
		{"ambulance_types/bls.png", true},
		{"types/oxygen.png", true},
		{"hospitals/logos/apollo.png", true},
		{"public/banner.jpg", true},
		{"users/456/avatar.jpg", true},

		// Sensitive KYC documents (MUST NOT be public display media)
		{"drivers/a572e8b4-9cc9-47ae-8933-6260c035b9d6/dl.jpg", false},
		{"drivers/a572e8b4-9cc9-47ae-8933-6260c035b9d6/rc.jpg", false},
		{"drivers/a572e8b4-9cc9-47ae-8933-6260c035b9d6/poi.jpg", false},
		{"drivers/123/insurance.pdf", false},
		{"drivers/123/pan_card.jpg", false},

		// Path traversal attempts (MUST be rejected)
		{"../etc/passwd", false},
		{"drivers/123/../../secrets/portrait.jpg", false},
		{"drivers/123/..%2fportrait.jpg", false},
	}

	for _, tt := range tests {
		actual := isPublicDisplayMedia(tt.path)
		if actual != tt.expected {
			t.Errorf("isPublicDisplayMedia(%q) = %v, expected %v", tt.path, actual, tt.expected)
		}
	}
}
