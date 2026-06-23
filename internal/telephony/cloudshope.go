package telephony

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type CloudshopeService struct {
	Token     string
	CLINumber string
	Client    *http.Client
}

func NewCloudshopeService(token, cliNumber string) *CloudshopeService {
	return &CloudshopeService{
		Token:     token,
		CLINumber: cliNumber,
		Client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (s *CloudshopeService) InitiateCallMasking(fromNumber, toNumber string) (string, error) {
	url := "https://apiv2.cloudshope.com/api/outboundCall"

	payload := map[string]string{
		"from_number":   fromNumber,
		"mobile_number": toNumber,
		"cli_number":    s.CLINumber,
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.Token))
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", errors.New(fmt.Sprintf("Cloudshope returned status: %d", resp.StatusCode))
	}

	// Returns the CLI Number in International format so the caller knows who to expect a call from
	return fmt.Sprintf("+91%s", s.CLINumber), nil
}
