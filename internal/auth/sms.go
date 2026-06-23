package auth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

const SMSHeader = "AMBHPL"

// SendSMS calls the SMSCountry API to send an OTP
func SendSMS(number string, otp string, appSignature string) error {
	authKey := os.Getenv("SMS_COUNTRY_KEY")
	authToken := os.Getenv("SMS_COUNTRY_TOKEN")

	if authKey == "" || authToken == "" {
		return fmt.Errorf("SMS_COUNTRY_KEY or SMS_COUNTRY_TOKEN is not set in environment")
	}

	credentials := fmt.Sprintf("%s:%s", authKey, authToken)
	encodedCredentials := base64.StdEncoding.EncodeToString([]byte(credentials))
	url := fmt.Sprintf("https://restapi.smscountry.com/v0.1/Accounts/%s/SMSes/", authKey)

	// Build the message exactly like V1
	msgContent := fmt.Sprintf("Your Ambigo verification code is: %s. Please do not share it with anyone else.", otp)
	if appSignature != "" {
		msgContent += "\n\n" + appSignature
	}

	payload := map[string]string{
		"Number":   "91" + number,
		"Text":     msgContent,
		"SenderId": SMSHeader,
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

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("SMS provider returned status code %d", resp.StatusCode)
	}

	return nil
}
