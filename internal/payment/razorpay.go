package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/metrics"
	"ambigo-backend/internal/retry"

	"github.com/razorpay/razorpay-go"
	"github.com/sony/gobreaker"
)

type RazorpayService struct {
	client    *razorpay.Client
	KeyID     string
	KeySecret string
	breaker   *gobreaker.CircuitBreaker
}

func newRazorpayBreaker() *gobreaker.CircuitBreaker {
	return gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "razorpay",
		MaxRequests: 20,
		Interval:    60 * time.Second,
		Timeout:     30 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			if counts.Requests < 10 {
				return false
			}
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return failureRatio >= 0.5
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			logger.Log.Info().Str("client", name).Str("from", from.String()).Str("to", to.String()).Msg("circuit breaker state changed")
			var val float64
			switch to {
			case gobreaker.StateClosed:
				val = 0
			case gobreaker.StateOpen:
				val = 1
			case gobreaker.StateHalfOpen:
				val = 2
			}
			metrics.CircuitBreakerState.WithLabelValues(name).Set(val)
			metrics.CircuitBreakerTransitions.WithLabelValues(name, from.String(), to.String()).Inc()
		},
	})
}

func NewRazorpayService(keyID, keySecret string) *RazorpayService {
	client := razorpay.NewClient(keyID, keySecret)
	return &RazorpayService{
		client:    client,
		KeyID:     keyID,
		KeySecret: keySecret,
		breaker:   newRazorpayBreaker(),
	}
}

// toPaise converts rupees to paise with rounding (never truncation —
// truncation silently shaves up to 1 paise per fare, which compounds into
// reconciliation drift across thousands of rides).
func toPaise(amountINR float64) int {
	return int(math.Round(amountINR * 100))
}

// ToPaise is the exported form for handlers that must compare local bills
// against provider-captured paise.
func ToPaise(amountINR float64) int {
	return toPaise(amountINR)
}

// RoundRupees rounds rupees to 2 decimals (bank-consistent rounding).
// Replaces the old float64(int(x*100))/100 truncation pattern, which shaved
// up to 1 paise per computation and drifted reconciliation at scale.
func RoundRupees(amountINR float64) float64 {
	return float64(toPaise(amountINR)) / 100
}

// nonRetryable converts client errors (4xx) into retry.NonRetryableError so
// auth/validation failures fail fast instead of burning 3 attempts and
// tripping the circuit breaker. Detection is by status code in the SDK error
// text; anything unrecognized stays retryable (safe default for money code).
func nonRetryable(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "status: 4") {
		return &retry.NonRetryableError{Err: err}
	}
	return err
}

// CreateOrder generates a new order ID from Razorpay for a given amount (in INR rupees)
func (s *RazorpayService) CreateOrder(amountINR float64, receipt string) (string, error) {
	if amountINR <= 0 {
		return "", &retry.NonRetryableError{Err: fmt.Errorf("razorpay: non-positive order amount %.2f", amountINR)}
	}
	if s.breaker != nil && s.breaker.State() == gobreaker.StateOpen {
		return "", fmt.Errorf("razorpay circuit breaker open: %w", gobreaker.ErrOpenState)
	}
	var orderID string
	_, err := s.breaker.Execute(func() (interface{}, error) {
		return nil, retry.Do(context.Background(), retry.Default, func(ctx context.Context) error {
			// Razorpay expects amount in paise
			amountPaise := toPaise(amountINR)

			data := map[string]interface{}{
				"amount":   amountPaise,
				"currency": "INR",
				"receipt":  receipt,
			}

			body, err := s.client.Order.Create(data, nil)
			if err != nil {
				return nonRetryable(err)
			}

			id, ok := body["id"].(string)
			if !ok {
				return errors.New("invalid response from razorpay: missing order id")
			}

			orderID = id
			return nil
		})
	})
	if err != nil {
		return "", err
	}
	return orderID, nil
}

// FetchedPayment is the provider-side truth for one Razorpay payment.
type FetchedPayment struct {
	AmountPaise int
	Status      string
	OrderID     string
}

// FetchPayment reads a payment from Razorpay's API. Use after signature
// verification and before fulfilling: it proves the captured amount and
// status match the local bill, defeating spoofed/stale/partial captures.
// 4xx (unknown id) is non-retryable; network/5xx retry via retry.Default.
func (s *RazorpayService) FetchPayment(paymentID string) (*FetchedPayment, error) {
	var out *FetchedPayment
	_, err := s.breaker.Execute(func() (interface{}, error) {
		return nil, retry.Do(context.Background(), retry.Default, func(ctx context.Context) error {
			body, err := s.client.Payment.Fetch(paymentID, nil, nil)
			if err != nil {
				return nonRetryable(err)
			}
			fp := &FetchedPayment{}
			switch v := body["amount"].(type) {
			case float64:
				fp.AmountPaise = int(math.Round(v))
			case float32:
				fp.AmountPaise = int(math.Round(float64(v)))
			case int:
				fp.AmountPaise = v
			case int64:
				fp.AmountPaise = int(v)
			case json.Number:
				if n, cerr := v.Int64(); cerr == nil {
					fp.AmountPaise = int(n)
				} else {
					return errors.New("invalid response from razorpay: bad payment amount")
				}
			default:
				return errors.New("invalid response from razorpay: missing payment amount")
			}
			fp.Status, _ = body["status"].(string)
			fp.OrderID, _ = body["order_id"].(string)
			out = fp
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
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
