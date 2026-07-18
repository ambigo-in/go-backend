package config

import (
	"os"

	"ambigo-backend/internal/logger"
)

// AppConfig holds all environment variables required by the application
type AppConfig struct {
	MongoURI          string
	JWTSecret         string
	JWTAlgorithm      string
	JWTValidityMs     int
	JWTAudience       string
	JWTIssuer         string
	APIKey            string
	GoogleMapsAPIKey  string

	// SMS Country (OTP)
	SMSAPIBaseURL string
	SMSSenderID   string
	SMSCC         string // country code prefix

	// Razorpay
	RazorpayKeyID          string
	RazorpayKeySecret      string
	RazorpayWebhookSecret string

	// Zwitch (Bank Payouts)
	ZwitchKey       string
	ZwitchSecret    string
	ZwitchAccountID string
	ZwitchAPIBaseURL string
	ZwitchProxyURL  string

	// Cloudshope (Call Masking)
	CloudshopeToken      string
	CloudshopeNumber     string
	CloudshopeAPIBaseURL string

	// Google APIs
	GoogleRoutesAPIURL     string
	GoogleTranslateAPIURL  string

	FirebaseCredentialsPath string
	Port                    string
	AllowStaleRefreshChain  bool
}

// LoadConfig reads configuration from environment variables
func LoadConfig() *AppConfig {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mongoURI := os.Getenv("MONGODB_URI")
	if mongoURI == "" {
		// Default to local MongoDB for development
		mongoURI = "mongodb://localhost:27017/"
	}

	cfg := &AppConfig{
		MongoURI:          mongoURI,
		JWTSecret:         os.Getenv("JWT_SECRET"),
		JWTAlgorithm:      os.Getenv("JWT_ALGORITHM"),
		JWTAudience:       os.Getenv("JWT_AUDIENCE"),
		JWTIssuer:         os.Getenv("JWT_ISSUER"),
		APIKey:            os.Getenv("API_KEY"),
		GoogleMapsAPIKey:  os.Getenv("GOOGLE_MAPS_API_KEY"),

		SMSAPIBaseURL:     envOrDefault("SMS_API_BASE_URL", "https://restapi.smscountry.com/v0.1/Accounts/%s/SMSes/"),
		SMSSenderID:       envOrDefault("SMS_SENDER_ID", "AMBHPL"),
		SMSCC:             envOrDefault("SMS_COUNTRY_CODE", "91"),

		RazorpayKeyID:          os.Getenv("RAZORPAY_KEY_ID"),
		RazorpayKeySecret:      os.Getenv("RAZORPAY_KEY_SECRET"),
		RazorpayWebhookSecret: os.Getenv("RAZORPAY_WEBHOOK_SECRET"),

		ZwitchKey:         os.Getenv("ZWITCH_KEY"),
		ZwitchSecret:      os.Getenv("ZWITCH_SECRET"),
		ZwitchAccountID:   os.Getenv("ZWITCH_ACCOUNT_ID"),
		ZwitchAPIBaseURL:  envOrDefault("ZWITCH_API_BASE_URL", "https://api.zwitch.io/v1"),
		ZwitchProxyURL:    os.Getenv("ZWITCH_PROXY_URL"),

		CloudshopeToken:      os.Getenv("CLOUDSHOPE_TOKEN"),
		CloudshopeNumber:     os.Getenv("CLOUDSHOPE_NUMBER"),
		CloudshopeAPIBaseURL: envOrDefault("CLOUDSHOPE_API_BASE_URL", "https://apiv2.cloudshope.com/api/outboundCall"),

		GoogleRoutesAPIURL:    envOrDefault("GOOGLE_ROUTES_API_URL", "https://routes.googleapis.com/directions/v2:computeRoutes"),
		GoogleTranslateAPIURL: envOrDefault("GOOGLE_TRANSLATE_API_URL", "https://translate.googleapis.com/translate_a/single"),

		FirebaseCredentialsPath: os.Getenv("FIREBASE_CREDENTIALS_PATH"),
		Port:                    port,
		AllowStaleRefreshChain:  os.Getenv("ALLOW_STALE_REFRESH_CHAIN") == "true",
	}

	if cfg.JWTSecret == "" {
		logger.Log.Fatal().Msg("JWT_SECRET is required")
	}
	if cfg.APIKey == "" {
		logger.Log.Fatal().Msg("API_KEY is required")
	}

	return cfg
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
