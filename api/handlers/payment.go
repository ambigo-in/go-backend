package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/payment"
	"ambigo-backend/internal/requestid"

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
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	role, ok := r.Context().Value(middleware.UserRoleKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
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
		response.Error(w, "Failed to fetch pending payments", http.StatusInternalServerError)
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
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	reqID := requestid.FromContext(r.Context())

	var req struct {
		PaymentID    string `json:"payment_id" validate:"required"`
		RzpPaymentID string `json:"rzp_payment_id" validate:"required"`
		RzpSignature string `json:"rzp_signature" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	objID, err := primitive.ObjectIDFromHex(req.PaymentID)
	if err != nil {
		response.Error(w, "Invalid payment ID", http.StatusBadRequest)
		return
	}

	pmt, err := h.Store.FindPaymentByID(r.Context(), objID)
	if err != nil || pmt == nil {
		response.Error(w, "Payment not found", http.StatusNotFound)
		return
	}

	if pmt.Paid {
		json.NewEncoder(w).Encode(map[string]string{"detail": "Payment already processed"})
		return
	}

	// Verify cryptographic signature
	if pmt.RazorpayOrderID == nil {
		response.Error(w, "Payment does not have a Razorpay Order ID", http.StatusBadRequest)
		return
	}

	isValid := h.RazorpayService.VerifySignature(*pmt.RazorpayOrderID, req.RzpPaymentID, req.RzpSignature)
	if !isValid {
		response.Error(w, "Invalid payment signature, processing failed!", http.StatusBadRequest)
		return
	}

	err = h.Store.MarkPaymentPaid(r.Context(), objID, req.RzpPaymentID, payment.ModeOnline)
	if err != nil {
		response.Error(w, "Failed to mark payment as paid", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelPaymentCompleted, eventbus.PaymentCompletedPayload{
		PaymentID: req.PaymentID, RideID: pmt.RideID,
		UserID: pmt.UserID, DriverID: pmt.PartnerID,
		Amount: pmt.ChargedAmount, Mode: "online", RequestID: reqID,
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Payment processed successfully"})
}

// HandleProcessDriverPayment is for Driver cash payments
func (h *PaymentHandler) HandleProcessDriverPayment(w http.ResponseWriter, r *http.Request) {
	_, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	reqID := requestid.FromContext(r.Context())

	var req struct {
		ID       string `json:"_id"`
		LegacyID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if req.ID == "" && req.LegacyID == "" {
		response.Error(w, "payment_id is required", http.StatusBadRequest)
		return
	}

	paymentID := req.ID
	if paymentID == "" {
		paymentID = req.LegacyID
	}
	objID, err := primitive.ObjectIDFromHex(paymentID)
	if err != nil {
		response.Error(w, "Invalid payment ID", http.StatusBadRequest)
		return
	}

	pmt, err := h.Store.FindPaymentByID(r.Context(), objID)
	if err != nil || pmt == nil {
		response.Error(w, "Payment not found", http.StatusNotFound)
		return
	}

	if pmt.Paid {
		response.Error(w, "Payment has already been completed", http.StatusBadRequest)
		return
	}

	err = h.Store.MarkPaymentPaid(r.Context(), objID, "", payment.ModeCash)
	if err != nil {
		response.Error(w, "Failed to mark payment as paid", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelPaymentCompleted, eventbus.PaymentCompletedPayload{
		PaymentID: objID.Hex(), RideID: pmt.RideID,
		UserID: pmt.UserID, DriverID: pmt.PartnerID,
		Amount: pmt.ChargedAmount, Mode: "cash", RequestID: reqID,
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Payment processed successfully"})
}

// HandleGetByRide fetches a payment using ride_id
func (h *PaymentHandler) HandleGetByRide(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RideID string `json:"ride_id" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	callerID, _ := r.Context().Value(middleware.UserIDKey).(string)
	callerRole, _ := r.Context().Value(middleware.UserRoleKey).(string)

	pmt, err := h.Store.FindPaymentByRideID(r.Context(), req.RideID)
	if err != nil || pmt == nil {
		response.Error(w, "Payment not found", http.StatusNotFound)
		return
	}

	// A1: Ownership check — only the ride's user, driver, or admin can view payment
	if callerRole != "admin" && callerID != pmt.UserID && callerID != pmt.PartnerID {
		response.Error(w, "Forbidden: you do not own this payment", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pmt)
}

// HandleRazorpayWebhook receives server-to-server payment.captured events from Razorpay.
// It verifies the webhook signature using the Razorpay webhook secret, then marks the payment as paid.
func (h *PaymentHandler) HandleRazorpayWebhook(w http.ResponseWriter, r *http.Request) {
	log := logger.Ctx(r.Context())

	if h.RazorpayWebhookSec == "" {
		response.Error(w, "Webhook secret not configured", http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		response.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	sig := r.Header.Get("x-razorpay-signature")
	if sig == "" {
		response.Error(w, "Missing signature", http.StatusBadRequest)
		return
	}

	mac := hmac.New(sha256.New, []byte(h.RazorpayWebhookSec))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		response.Error(w, "Invalid signature", http.StatusUnauthorized)
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
		response.Error(w, "Invalid webhook payload", http.StatusBadRequest)
		return
	}

	if event.Event != "payment.captured" {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored"})
		return
	}

	pmt, err := h.Store.FindPaymentByRazorpayOrderID(r.Context(), event.Payload.Payment.Entity.OrderID)
	if err != nil {
		log.Error().Err(err).Msg("PaymentWebhook DB error")
		response.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	if pmt == nil {
		log.Warn().Str("order_id", event.Payload.Payment.Entity.OrderID).Msg("PaymentWebhook no payment found for order")
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
		log.Error().Err(err).Str("payment_id", pmt.ID.Hex()).Msg("PaymentWebhook failed to mark payment as paid")
		response.Error(w, "Failed to update payment", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelPaymentCompleted, eventbus.PaymentCompletedPayload{
		PaymentID: pmt.ID.Hex(), RideID: pmt.RideID,
		UserID: pmt.UserID, DriverID: pmt.PartnerID,
		Amount: pmt.ChargedAmount, Mode: "online",
	})

	log.Info().Str("payment_id", pmt.ID.Hex()).Str("order_id", event.Payload.Payment.Entity.OrderID).Msg("PaymentWebhook payment marked paid")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "processed"})
}
