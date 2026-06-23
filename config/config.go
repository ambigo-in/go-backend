package config

import (
	"log"
	"os"
)

// AppConfig holds all environment variables required by the application
type AppConfig struct {
	MongoURI          string
	JWTSecret         string
	JWTAlgorithm      string
	JWTValidityMs     int
	APIKey            string
	GoogleMapsAPIKey  string
	RazorpayKeyID          string
	RazorpayKeySecret      string
	RazorpayWebhookSecret string
	CloudshopeToken   string
	CloudshopeNumber  string
	ZwitchKey         string
	ZwitchSecret      string
	ZwitchAccountID   string
	FirebaseCredentialsPath string
	Port              string
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
		APIKey:            os.Getenv("API_KEY"),
		GoogleMapsAPIKey:  os.Getenv("GOOGLE_MAPS_API_KEY"),
		RazorpayKeyID:          os.Getenv("RAZORPAY_KEY_ID"),
		RazorpayKeySecret:      os.Getenv("RAZORPAY_KEY_SECRET"),
		RazorpayWebhookSecret: os.Getenv("RAZORPAY_WEBHOOK_SECRET"),
		CloudshopeToken:   os.Getenv("CLOUDSHOPE_TOKEN"),
		CloudshopeNumber:  os.Getenv("CLOUDSHOPE_NUMBER"),
		ZwitchKey:         os.Getenv("ZWITCH_KEY"),
		ZwitchSecret:      os.Getenv("ZWITCH_SECRET"),
		ZwitchAccountID:   os.Getenv("ZWITCH_ACCOUNT_ID"),
		FirebaseCredentialsPath: os.Getenv("FIREBASE_CREDENTIALS_PATH"),
		Port:              port,
	}

	if cfg.JWTSecret == "" {
		log.Fatal("JWT_SECRET is required")
	}
	if cfg.APIKey == "" {
		log.Fatal("API_KEY is required")
	}

	return cfg
}
