package payment

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/razorpay/razorpay-go"
)

type RazorpayService struct {
	client    *razorpay.Client
	KeyID     string
	KeySecret string
}

func NewRazorpayService(keyID, keySecret string) *RazorpayService {
	client := razorpay.NewClient(keyID, keySecret)
	return &RazorpayService{
		client:    client,
		KeyID:     keyID,
		KeySecret: keySecret,
	}
}

// CreateOrder generates a new order ID from Razorpay for a given amount (in INR rupees)
func (s *RazorpayService) CreateOrder(amountINR float64, receipt string) (string, error) {
	// Razorpay expects amount in paise
	amountPaise := int(amountINR * 100)

	data := map[string]interface{}{
		"amount":   amountPaise,
		"currency": "INR",
		"receipt":  receipt,
	}

	body, err := s.client.Order.Create(data, nil)
	if err != nil {
		return "", err
	}

	orderID, ok := body["id"].(string)
	if !ok {
		return "", errors.New("invalid response from razorpay: missing order id")
	}

	return orderID, nil
}

// VerifySignature cryptographically validates the Razorpay callback
func (s *RazorpayService) VerifySignature(orderID, paymentID, signature string) bool {
	// Signature is HMAC SHA256 of "order_id|payment_id"
	data := orderID + "|" + paymentID

	h := hmac.New(sha256.New, []byte(s.KeySecret))
	h.Write([]byte(data))
	expectedSignature := hex.EncodeToString(h.Sum(nil))

	return hmac.Equal([]byte(expectedSignature), []byte(signature))
}
