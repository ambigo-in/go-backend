package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"

	"ambigo-backend/api/middleware"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/payment"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type PaymentHandler struct {
	Store              *payment.Store
	EventBus           *eventbus.InMemoryBus
	RazorpayService    *payment.RazorpayService
	RazorpayWebhookSec string
}

func NewPaymentHandler(store *payment.Store, eventBus *eventbus.InMemoryBus, rzp *payment.RazorpayService, webhookSec string) *PaymentHandler {
	return &PaymentHandler{
		Store:              store,
		EventBus:           eventBus,
		RazorpayService:    rzp,
		RazorpayWebhookSec: webhookSec,
	}
}

// HandleGetPending fetches an unpaid payment for a user or driver
func (h *PaymentHandler) HandleGetPending(w http.ResponseWriter, r *http.Request) {
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	role, ok := r.Context().Value(middleware.UserRoleKey).(string)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var pmt *payment.Payment
	var err error

	if role == "user" {
		pmt, err = h.Store.FindPendingPaymentByUserID(r.Context(), uidStr)
	} else if role == "driver" {
		pmt, err = h.Store.FindPendingPaymentByPartnerID(r.Context(), uidStr)
	}

	if err != nil {
		http.Error(w, "Failed to fetch pending payments", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if pmt == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"found": false, "data": nil})
	} else {
		json.NewEncoder(w).Encode(map[string]interface{}{"found": true, "data": pmt})
	}
}

// HandleProcessUserPayment verifies Razorpay Signature and marks payment as paid for User online payments
func (h *PaymentHandler) HandleProcessUserPayment(w http.ResponseWriter, r *http.Request) {
	_, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		PaymentID    string `json:"payment_id"`
		RzpPaymentID string `json:"rzp_payment_id"`
		RzpSignature string `json:"rzp_signature"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	objID, err := primitive.ObjectIDFromHex(req.PaymentID)
	if err != nil {
		http.Error(w, "Invalid payment ID", http.StatusBadRequest)
		return
	}

	pmt, err := h.Store.FindPaymentByID(r.Context(), objID)
	if err != nil || pmt == nil {
		http.Error(w, "Payment not found", http.StatusNotFound)
		return
	}

	if pmt.Paid {
		json.NewEncoder(w).Encode(map[string]string{"detail": "Payment already processed"})
		return
	}

	// Verify cryptographic signature
	if pmt.RazorpayOrderID == nil {
		http.Error(w, "Payment does not have a Razorpay Order ID", http.StatusBadRequest)
		return
	}

	isValid := h.RazorpayService.VerifySignature(*pmt.RazorpayOrderID, req.RzpPaymentID, req.RzpSignature)
	if !isValid {
		http.Error(w, "Invalid payment signature, processing failed!", http.StatusBadRequest)
		return
	}

	err = h.Store.MarkPaymentPaid(r.Context(), objID, req.RzpPaymentID, payment.ModeOnline)
	if err != nil {
		http.Error(w, "Failed to mark payment as paid", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelPaymentCompleted, eventbus.PaymentCompletedPayload{
		PaymentID: req.PaymentID, RideID: pmt.RideID,
		UserID: pmt.UserID, DriverID: pmt.PartnerID,
		Amount: pmt.ChargedAmount, Mode: "online",
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Payment processed successfully"})
}

// HandleProcessDriverPayment is for Driver cash payments
func (h *PaymentHandler) HandleProcessDriverPayment(w http.ResponseWriter, r *http.Request) {
	_, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		ID       string `json:"_id"`
		LegacyID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	paymentID := req.ID
	if paymentID == "" {
		paymentID = req.LegacyID
	}
	objID, err := primitive.ObjectIDFromHex(paymentID)
	if err != nil {
		http.Error(w, "Invalid payment ID", http.StatusBadRequest)
		return
	}

	pmt, err := h.Store.FindPaymentByID(r.Context(), objID)
	if err != nil || pmt == nil {
		http.Error(w, "Payment not found", http.StatusNotFound)
		return
	}

	if pmt.Paid {
		http.Error(w, "Payment has already been completed", http.StatusBadRequest)
		return
	}

	err = h.Store.MarkPaymentPaid(r.Context(), objID, "", payment.ModeCash)
	if err != nil {
		http.Error(w, "Failed to mark payment as paid", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelPaymentCompleted, eventbus.PaymentCompletedPayload{
		PaymentID: objID.Hex(), RideID: pmt.RideID,
		UserID: pmt.UserID, DriverID: pmt.PartnerID,
		Amount: pmt.ChargedAmount, Mode: "cash",
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Payment processed successfully"})
}

// HandleGetByRide fetches a payment using ride_id
func (h *PaymentHandler) HandleGetByRide(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RideID string `json:"ride_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	pmt, err := h.Store.FindPaymentByRideID(r.Context(), req.RideID)
	if err != nil || pmt == nil {
		http.Error(w, "Payment not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pmt)
}

// HandleRazorpayWebhook receives server-to-server payment.captured events from Razorpay.
// It verifies the webhook signature using the Razorpay webhook secret, then marks the payment as paid.
func (h *PaymentHandler) HandleRazorpayWebhook(w http.ResponseWriter, r *http.Request) {
	if h.RazorpayWebhookSec == "" {
		http.Error(w, "Webhook secret not configured", http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	sig := r.Header.Get("x-razorpay-signature")
	if sig == "" {
		http.Error(w, "Missing signature", http.StatusBadRequest)
		return
	}

	mac := hmac.New(sha256.New, []byte(h.RazorpayWebhookSec))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		http.Error(w, "Invalid signature", http.StatusUnauthorized)
		return
	}

	var event struct {
		Event string `json:"event"`
		Payload struct {
			Payment struct {
				Entity struct {
					ID      string `json:"id"`
					OrderID string `json:"order_id"`
					Status  string `json:"status"`
				} `json:"entity"`
			} `json:"payment"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "Invalid webhook payload", http.StatusBadRequest)
		return
	}

	if event.Event != "payment.captured" {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored"})
		return
	}

	pmt, err := h.Store.FindPaymentByRazorpayOrderID(r.Context(), event.Payload.Payment.Entity.OrderID)
	if err != nil {
		log.Printf("[PaymentWebhook] DB error: %v", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	if pmt == nil {
		log.Printf("[PaymentWebhook] No payment found for order %s", event.Payload.Payment.Entity.OrderID)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "not_found"})
		return
	}

	if pmt.Paid {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "already_paid"})
		return
	}

	if err := h.Store.MarkPaymentPaid(r.Context(), pmt.ID, event.Payload.Payment.Entity.ID, payment.ModeOnline); err != nil {
		log.Printf("[PaymentWebhook] Failed to mark payment %s as paid: %v", pmt.ID.Hex(), err)
		http.Error(w, "Failed to update payment", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelPaymentCompleted, eventbus.PaymentCompletedPayload{
		PaymentID: pmt.ID.Hex(), RideID: pmt.RideID,
		UserID: pmt.UserID, DriverID: pmt.PartnerID,
		Amount: pmt.ChargedAmount, Mode: "online",
	})

	log.Printf("[PaymentWebhook] Payment %s marked paid via Razorpay webhook (order: %s)", pmt.ID.Hex(), event.Payload.Payment.Entity.OrderID)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "processed"})
}
