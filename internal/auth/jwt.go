package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AccessTokenExpiry is the user/driver/attendant access-token lifetime.
// Default 15 minutes (production). Load-test backends override it via the
// JWT_VALIDITY_MS env (wired in cmd/server/main.go) so long suites never
// outlive their tokens mid-run.
var AccessTokenExpiry = 15 * time.Minute

type Claims struct {
	ID         string `json:"_id"`
	Role       string `json:"role"`
	AdminRole  string `json:"admin_role,omitempty"`
	HospitalID string `json:"hospital_id,omitempty"`
	jwt.RegisteredClaims
}

func ValidateToken(tokenString string, secret string, opts ...jwt.ParserOption) (*Claims, error) {
	allOpts := append([]jwt.ParserOption{
		jwt.WithValidMethods([]string{"HS256"}),
	}, opts...)

	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	}, allOpts...)
	if err != nil {
		return nil, err
	}
	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}
	return nil, errors.New("invalid token")
}

func GenerateAccessToken(id string, role string, secret string) (string, error) {
	claims := Claims{
		ID:   id,
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(AccessTokenExpiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// GenerateJWT generates admin tokens with 24-hour expiry and embeds the admin's stored role
func GenerateJWT(id string, role string, adminRole string, secret string) (string, error) {
	claims := Claims{
		ID:        id,
		Role:      role,
		AdminRole: adminRole,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func GenerateHospitalAccessToken(id, role, hospitalID, secret string) (string, error) {
	claims := Claims{
		ID:         id,
		Role:       role,
		HospitalID: hospitalID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(AccessTokenExpiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func GenerateHospitalJWT(id, role, hospitalID, secret string) (string, error) {
	claims := Claims{
		ID:         id,
		Role:       role,
		HospitalID: hospitalID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}
