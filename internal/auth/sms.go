package auth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

var smsClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:    5,
		IdleConnTimeout: 90 * time.Second,
	},
}

// SMSCountryConfig holds configuration for SMSCountry API
type SMSCountryConfig struct {
	APIKey     string
	APIToken   string
	APIBaseURL string
	SenderID   string
	CC         string // country code prefix
}

// SendSMS calls the SMSCountry API to send an OTP
func SendSMS(cfg SMSCountryConfig, number string, otp string, appSignature string) error {
	if cfg.APIKey == "" || cfg.APIToken == "" {
		return fmt.Errorf("SMS_COUNTRY_KEY or SMS_COUNTRY_TOKEN is not set in environment")
	}

	credentials := fmt.Sprintf("%s:%s", cfg.APIKey, cfg.APIToken)
	encodedCredentials := base64.StdEncoding.EncodeToString([]byte(credentials))
	url := fmt.Sprintf(cfg.APIBaseURL, cfg.APIKey)

	// Build the message exactly like V1
	msgContent := fmt.Sprintf("Your Ambigo verification code is: %s. Please do not share it with anyone else.", otp)
	if appSignature != "" {
		msgContent += "\n\n" + appSignature
	}

	payload := map[string]string{
		"Number":   cfg.CC + number,
		"Text":     msgContent,
		"SenderId": cfg.SenderID,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Basic "+encodedCredentials)
	req.Header.Set("Content-Type", "application/json")

	resp, err := smsClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("SMS provider returned status code %d", resp.StatusCode)
	}

	return nil
}
