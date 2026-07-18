package payment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/logger"
)

type ZwitchService struct {
	KeyID       string
	Secret      string
	AccountID   string
	APIBaseURL  string
	Client      *http.Client
}

func NewZwitchService(key, secret, accountID, apiBaseURL, proxyURL string) *ZwitchService {
	transport := &http.Transport{}
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	}
	return &ZwitchService{
		KeyID:      key,
		Secret:     secret,
		AccountID:  accountID,
		APIBaseURL: apiBaseURL,
		Client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
		},
	}
}

func (s *ZwitchService) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s:%s", s.KeyID, s.Secret))
	req.Header.Set("Content-Type", "application/json")
}

func (s *ZwitchService) VerifyBankAccount(acc *auth.WalletDetails, referenceID string) (string, error) {
	url := s.APIBaseURL + "/verifications/bank-account"
	payload := map[string]interface{}{
		"force_penny_drop":        false,
		"force_penny_drop_amount": 1,
		"bank_account_number":     acc.AccountNo,
		"bank_ifsc_code":          acc.IFSCCode,
		"merchant_reference_id":   referenceID,
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(body))
	s.setHeaders(req)

	resp, err := s.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("zwitch verification failed: %d", resp.StatusCode)
	}

	var data map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&data)
	if status, ok := data["status"].(string); ok {
		return status, nil
	}
	return "", errors.New("missing status")
}

func (s *ZwitchService) CreateBeneficiary(acc *auth.WalletDetails, driverID string) (string, error) {
	url := fmt.Sprintf("%s/accounts/%s/beneficiaries", s.APIBaseURL, s.AccountID)
	payload := map[string]interface{}{
		"type":                   "account_number",
		"name_of_account_holder": acc.BenfName,
		"bank_account_number":    acc.AccountNo,
		"bank_ifsc_code":         acc.IFSCCode,
		"metadata": map[string]string{
			"driver_uid": driverID,
		},
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(body))
	s.setHeaders(req)

	resp, err := s.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		logger.Log.Error().Int("status", resp.StatusCode).Str("body", string(respBody)).Msg("Zwitch create beneficiary failed")
		return "", fmt.Errorf("zwitch create beneficiary failed: %d - %s", resp.StatusCode, string(respBody))
	}

	var data map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&data)
	if id, ok := data["id"].(string); ok {
		return id, nil
	}
	return "", errors.New("missing id")
}

func (s *ZwitchService) UpdateBeneficiaryName(acc *auth.WalletDetails) error {
	url := fmt.Sprintf("%s/accounts/beneficiaries/%s", s.APIBaseURL, acc.BenfID)
	payload := map[string]interface{}{
		"name_of_account_holder": acc.BenfName,
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(body))
	s.setHeaders(req)

	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("zwitch update beneficiary failed: %d", resp.StatusCode)
	}

	return nil
}

func (s *ZwitchService) DeleteBeneficiary(benfID string) error {
	url := fmt.Sprintf("%s/accounts/beneficiaries/%s", s.APIBaseURL, benfID)
	req, _ := http.NewRequest("DELETE", url, nil)
	s.setHeaders(req)

	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("zwitch delete beneficiary failed: %d", resp.StatusCode)
	}
	return nil
}

func (s *ZwitchService) CreateTransfer(acc *auth.WalletDetails, amount float64, referenceID string) (map[string]interface{}, error) {
	url := s.APIBaseURL + "/transfers"
	payload := map[string]interface{}{
		"type":                  "account_number",
		"currency_code":         "inr",
		"debit_account_id":      s.AccountID,
		"beneficiary_id":        acc.BenfID,
		"amount":                amount,
		"payment_mode":          "neft",
		"merchant_reference_id": referenceID,
		"async":                 false,
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(body))
	s.setHeaders(req)

	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&data)

	if resp.StatusCode >= 400 {
		return data, fmt.Errorf("zwitch transfer failed: %d", resp.StatusCode)
	}

	return data, nil
}
