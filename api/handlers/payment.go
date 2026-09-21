package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/ids"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/payment"
	"ambigo-backend/internal/requestid"

	"github.com/jackc/pgx/v5"
)

type PaymentHandler struct {
	Store              *payment.Store
	EventBus           *eventbus.InMemoryBus
	RazorpayService    *payment.RazorpayService
	WalletStore        *payment.WalletStore
	RazorpayWebhookSec string
}

func NewPaymentHandler(store *payment.Store, eventBus *eventbus.InMemoryBus, rzp *payment.RazorpayService, walletStore *payment.WalletStore, webhookSec string) *PaymentHandler {
	return &PaymentHandler{
		Store:              store,
		EventBus:           eventBus,
		RazorpayService:    rzp,
		WalletStore:        walletStore,
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

// ownsPayment allows the ride's user, its driver, or an admin.
// Settle endpoints previously checked only "logged in" — any authenticated
// caller with a payment_id could settle anyone's bill (IDOR).
func ownsPayment(r *http.Request, pmt *payment.Payment) bool {
	callerID, _ := r.Context().Value(middleware.UserIDKey).(string)
	callerRole, _ := r.Context().Value(middleware.UserRoleKey).(string)
	if callerRole == "admin" {
		return true
	}
	return callerID != "" && (callerID == pmt.UserID || callerID == pmt.PartnerID)
}

// settleOnlineTx marks the bill paid-online and credits the driver's share
// in one transaction, with a ledger row. ErrAlreadyPaid (concurrent confirm
// vs webhook) is idempotent success WITHOUT a second credit.
// An app-confirm claim key dedupes double-taps/retries of the same receipt.
func (h *PaymentHandler) settleOnlineTx(r *http.Request, pmt *payment.Payment, rzpPaymentID string) error {
	return payment.WithTx(r.Context(), h.Store.Pool(), func(tx pgx.Tx) error {
		if cerr := payment.ClaimEventTx(r.Context(), tx, "app:"+pmt.ID+":"+rzpPaymentID, "app_confirm", nil); cerr != nil {
			return cerr
		}
		sTx := h.Store.WithTx(tx)
		wTx := h.WalletStore.WithTx(tx)
		if err := sTx.MarkPaymentPaid(r.Context(), pmt.ID, rzpPaymentID, payment.ModeOnline); err != nil {
			return err
		}
		// Priority order when the driver link is broken: the rider's money is
		// already captured, so the bill MUST be marked paid (else the rider
		// pays twice on retry). The missing credit is parked for ops via a
		// critical log with full context — never fail the settlement.
		if !ids.IsValid(pmt.PartnerID) || pmt.PartnerID == "" || pmt.DriverShare <= 0 {
			logger.Log.Error().Str("payment_id", pmt.ID).Str("partner_id", pmt.PartnerID).Float64("driver_share", pmt.DriverShare).Float64("charged", pmt.ChargedAmount).Msg("PARKED FOR OPS: online payment settled without wallet credit — invalid partner or share")
			return nil
		}
		var exists bool
		if qerr := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM drivers WHERE id=$1)`, pmt.PartnerID).Scan(&exists); qerr != nil {
			return qerr
		}
		if !exists {
			logger.Log.Error().Str("payment_id", pmt.ID).Str("partner_id", pmt.PartnerID).Float64("driver_share", pmt.DriverShare).Msg("PARKED FOR OPS: online payment settled without wallet credit — driver not found")
			return nil
		}
		if err := wTx.UpdateWalletBalance(r.Context(), pmt.PartnerID, pmt.DriverShare); err != nil {
			return err
		}
		bal, berr := wTx.GetBalance(r.Context(), pmt.PartnerID)
		if berr != nil {
			return berr
		}
		if err := wTx.InsertLedgerEntry(r.Context(), &payment.WalletTransaction{
			DriverID:    pmt.PartnerID,
			Amount:      pmt.DriverShare,
			Direction:   "credit",
			TxnType:     "ride_credit",
			ReferenceID: pmt.ID,
			Status:      "success",
			BalanceAfter: &bal,
		}); err != nil {
			return err
		}
		return nil
	})
}

// settleCashTx marks the bill paid-cash and debits the platform commission
// in one transaction, with a ledger row. Commission uses OriginalAmount
// (pre-discount fare): referral discounts are absorbed by the platform, so
// the driver is never penalized for a discount they did not grant. The
// discount itself is recorded on the ride fare (ReferralDiscount) for finance.
func (h *PaymentHandler) settleCashTx(r *http.Request, pmt *payment.Payment) error {
	return payment.WithTx(r.Context(), h.Store.Pool(), func(tx pgx.Tx) error {
		if cerr := payment.ClaimEventTx(r.Context(), tx, "cash:"+pmt.ID, "cash_confirm", nil); cerr != nil {
			return cerr
		}
		sTx := h.Store.WithTx(tx)
		wTx := h.WalletStore.WithTx(tx)
		if err := sTx.MarkPaymentPaid(r.Context(), pmt.ID, "", payment.ModeCash); err != nil {
			return err
		}
		commission := pmt.OriginalAmount - pmt.DriverShare
		if commission <= 0 {
			return nil
		}
		// Same priority as online: settle the bill even if the driver link is
		// broken; park the missing debit for ops instead of failing.
		if !ids.IsValid(pmt.PartnerID) || pmt.PartnerID == "" {
			logger.Log.Error().Str("payment_id", pmt.ID).Str("partner_id", pmt.PartnerID).Float64("commission", commission).Msg("PARKED FOR OPS: cash payment settled without commission debit — invalid partner")
			return nil
		}
		var exists bool
		if qerr := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM drivers WHERE id=$1)`, pmt.PartnerID).Scan(&exists); qerr != nil {
			return qerr
		}
		if !exists {
			logger.Log.Error().Str("payment_id", pmt.ID).Str("partner_id", pmt.PartnerID).Float64("commission", commission).Msg("PARKED FOR OPS: cash payment settled without commission debit — driver not found")
			return nil
		}
		if err := wTx.UpdateWalletBalance(r.Context(), pmt.PartnerID, -commission); err != nil {
			return err
		}
		bal, berr := wTx.GetBalance(r.Context(), pmt.PartnerID)
		if berr != nil {
			return berr
		}
		return wTx.InsertLedgerEntry(r.Context(), &payment.WalletTransaction{
			DriverID:    pmt.PartnerID,
			Amount:      commission,
			Direction:   "debit",
			TxnType:     "commission_debit",
			ReferenceID: pmt.ID,
			Status:      "success",
			BalanceAfter: &bal,
		})
	})
}

// verifyCapturedAmount fetches the payment from Razorpay and proves the
// captured paise and order match the local bill. A valid HMAC only proves
// the receipt pair is genuine — not that the money matches the bill.
func (h *PaymentHandler) verifyCapturedAmount(pmt *payment.Payment, rzpPaymentID string) error {
	fp, err := h.RazorpayService.FetchPayment(rzpPaymentID)
	if err != nil {
		return err
	}
	if pmt.RazorpayOrderID == nil || fp.OrderID != *pmt.RazorpayOrderID {
		return errors.New("razorpay order mismatch")
	}
	if fp.Status != "captured" {
		return errors.New("razorpay payment not captured")
	}
	if fp.AmountPaise != payment.ToPaise(pmt.ChargedAmount) {
		logger.Log.Error().Str("payment_id", pmt.ID).Int("expected_paise", payment.ToPaise(pmt.ChargedAmount)).Int("captured_paise", fp.AmountPaise).Msg("Captured amount mismatch")
		return errors.New("captured amount does not match bill")
	}
	return nil
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

	if !ids.IsValid(req.PaymentID) {
		response.Error(w, "Invalid payment ID", http.StatusBadRequest)
		return
	}

	pmt, err := h.Store.FindPaymentByID(r.Context(), req.PaymentID)
	if err != nil || pmt == nil {
		response.Error(w, "Payment not found", http.StatusNotFound)
		return
	}

	if !ownsPayment(r, pmt) {
		response.Error(w, "Forbidden: you do not own this payment", http.StatusForbidden)
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

	// Provider-side truth: prove the captured paise and order match the bill
	// before crediting anyone. Fail closed on any doubt.
	if err := h.verifyCapturedAmount(pmt, req.RzpPaymentID); err != nil {
		logger.Log.Error().Err(err).Str("payment_id", req.PaymentID).Str("request_id", reqID).Msg("Captured amount verification failed")
		response.Error(w, "Payment verification failed: "+err.Error(), http.StatusConflict)
		return
	}

	// Atomic: mark paid + credit wallet + ledger row in single Tx.
	err = h.settleOnlineTx(r, pmt, req.RzpPaymentID)
	if err != nil {
		if errors.Is(err, payment.ErrAlreadyPaid) || errors.Is(err, payment.ErrDuplicateEvent) {
			json.NewEncoder(w).Encode(map[string]string{"detail": "Payment already processed"})
			return
		}
		logger.Log.Error().Err(err).Str("payment_id", req.PaymentID).Str("request_id", reqID).Msg("Failed to process online payment transaction")
		response.Error(w, "Failed to process payment", http.StatusInternalServerError)
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
	if !ids.IsValid(paymentID) {
		response.Error(w, "Invalid payment ID", http.StatusBadRequest)
		return
	}

	pmt, err := h.Store.FindPaymentByID(r.Context(), paymentID)
	if err != nil || pmt == nil {
		response.Error(w, "Payment not found", http.StatusNotFound)
		return
	}

	if !ownsPayment(r, pmt) {
		response.Error(w, "Forbidden: you do not own this payment", http.StatusForbidden)
		return
	}

	if pmt.Paid {
		response.Error(w, "Payment has already been completed", http.StatusBadRequest)
		return
	}

	err = h.settleCashTx(r, pmt)
	if err != nil {
		if errors.Is(err, payment.ErrAlreadyPaid) || errors.Is(err, payment.ErrDuplicateEvent) {
			response.Error(w, "Payment has already been completed", http.StatusBadRequest)
			return
		}
		logger.Log.Error().Err(err).Str("payment_id", paymentID).Str("request_id", reqID).Msg("Failed to process cash payment transaction")
		response.Error(w, "Failed to process payment", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelPaymentCompleted, eventbus.PaymentCompletedPayload{
		PaymentID: paymentID, RideID: pmt.RideID,
		UserID: pmt.UserID, DriverID: pmt.PartnerID,
		Amount: pmt.ChargedAmount, Mode: "cash", RequestID: reqID,
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Payment processed successfully"})
}

// HandleProcessUserCashPayment lets a user mark an unpaid payment as cash (switch from online to cash)
func (h *PaymentHandler) HandleProcessUserCashPayment(w http.ResponseWriter, r *http.Request) {
	_, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	reqID := requestid.FromContext(r.Context())

	var req struct {
		PaymentID string `json:"payment_id" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	if !ids.IsValid(req.PaymentID) {
		response.Error(w, "Invalid payment ID", http.StatusBadRequest)
		return
	}

	pmt, err := h.Store.FindPaymentByID(r.Context(), req.PaymentID)
	if err != nil || pmt == nil {
		response.Error(w, "Payment not found", http.StatusNotFound)
		return
	}

	if !ownsPayment(r, pmt) {
		response.Error(w, "Forbidden: you do not own this payment", http.StatusForbidden)
		return
	}

	if pmt.Paid {
		response.Error(w, "Payment has already been completed", http.StatusBadRequest)
		return
	}

	err = h.settleCashTx(r, pmt)
	if err != nil {
		if errors.Is(err, payment.ErrAlreadyPaid) || errors.Is(err, payment.ErrDuplicateEvent) {
			response.Error(w, "Payment has already been completed", http.StatusBadRequest)
			return
		}
		logger.Log.Error().Err(err).Str("payment_id", req.PaymentID).Str("request_id", reqID).Msg("Failed to process user cash payment transaction")
		response.Error(w, "Failed to process payment", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelPaymentCompleted, eventbus.PaymentCompletedPayload{
		PaymentID: req.PaymentID, RideID: pmt.RideID,
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
		ID    string `json:"id"`
		Payload struct {
			Payment struct {
				Entity struct {
					ID      string `json:"id"`
					OrderID string `json:"order_id"`
					Status  string `json:"status"`
					Amount  int    `json:"amount"`
				} `json:"entity"`
			} `json:"payment"`
			Refund struct {
				Entity struct {
					ID        string `json:"id"`
					PaymentID string `json:"payment_id"`
					Amount    int    `json:"amount"`
					Status    string `json:"status"`
				} `json:"entity"`
			} `json:"refund"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		response.Error(w, "Invalid webhook payload", http.StatusBadRequest)
		return
	}

	// Provider event id for idempotent handling (Razorpay retries for 24h on
	// non-2xx; without dedupe every retry re-applies the business effect).
	// Fallback chain guarantees non-empty: an empty key would fail the claim
	// on every delivery and poison the retry queue for 24h.
	eventID := r.Header.Get("x-razorpay-event-id")
	if eventID == "" {
		eventID = event.ID
	}
	if eventID == "" {
		eventID = "order:" + event.Payload.Payment.Entity.OrderID + ":" + event.Event
	}

	switch event.Event {
	case "payment.failed":
		h.handleRazorpayPaymentFailed(w, r, eventID, event.Payload.Payment.Entity.ID, event.Payload.Payment.Entity.OrderID)
		return
	case "refund.processed", "refund.created":
		log.Warn().Str("event", event.Event).Str("refund_id", event.Payload.Refund.Entity.ID).Msg("Refund event recorded, no ledger action (refunds settle via gateway)")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "recorded"})
		return
	case "payment.captured":
		// fall through to capture handling below
	default:
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

	// Provider-side truth before fulfilling: the webhook HMAC only proves the
	// event came from Razorpay, not that the money matches the bill.
	fp, ferr := h.RazorpayService.FetchPayment(event.Payload.Payment.Entity.ID)
	if ferr != nil {
		log.Error().Err(ferr).Str("payment_id", pmt.ID).Msg("PaymentWebhook amount verification fetch failed")
		response.Error(w, "Failed to verify payment", http.StatusInternalServerError)
		return
	}
	if pmt.RazorpayOrderID == nil || fp.OrderID != *pmt.RazorpayOrderID || fp.Status != "captured" || fp.AmountPaise != payment.ToPaise(pmt.ChargedAmount) {
		log.Error().Str("payment_id", pmt.ID).Str("order_id", event.Payload.Payment.Entity.OrderID).Int("expected_paise", payment.ToPaise(pmt.ChargedAmount)).Int("captured_paise", fp.AmountPaise).Str("status", fp.Status).Msg("PaymentWebhook captured amount/order mismatch")
		response.Error(w, "Captured amount does not match bill", http.StatusConflict)
		return
	}

	// Atomic: claim event-id + mark paid + credit wallet + ledger row.
	// ClaimEvent inside the Tx makes redelivery idempotent even when two
	// workers race: exactly one Tx commits both the claim and the credit.
	err = payment.WithTx(r.Context(), h.Store.Pool(), func(tx pgx.Tx) error {
		if cerr := payment.ClaimEventTx(r.Context(), tx, eventID, event.Event, body); cerr != nil {
			return cerr
		}
		sTx := h.Store.WithTx(tx)
		wTx := h.WalletStore.WithTx(tx)
		if err := sTx.MarkPaymentPaid(r.Context(), pmt.ID, event.Payload.Payment.Entity.ID, payment.ModeOnline); err != nil {
			return err
		}
		if !ids.IsValid(pmt.PartnerID) || pmt.PartnerID == "" || pmt.DriverShare <= 0 {
			logger.Log.Error().Str("payment_id", pmt.ID).Str("partner_id", pmt.PartnerID).Float64("driver_share", pmt.DriverShare).Msg("PARKED FOR OPS: webhook settled without wallet credit — invalid partner or share")
			return nil
		}
		var exists bool
		if qerr := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM drivers WHERE id=$1)`, pmt.PartnerID).Scan(&exists); qerr != nil {
			return qerr
		}
		if !exists {
			logger.Log.Error().Str("payment_id", pmt.ID).Str("partner_id", pmt.PartnerID).Float64("driver_share", pmt.DriverShare).Msg("PARKED FOR OPS: webhook settled without wallet credit — driver not found")
			return nil
		}
		if err := wTx.UpdateWalletBalance(r.Context(), pmt.PartnerID, pmt.DriverShare); err != nil {
			return err
		}
		bal, berr := wTx.GetBalance(r.Context(), pmt.PartnerID)
		if berr != nil {
			return berr
		}
		return wTx.InsertLedgerEntry(r.Context(), &payment.WalletTransaction{
			DriverID:    pmt.PartnerID,
			Amount:      pmt.DriverShare,
			Direction:   "credit",
			TxnType:     "ride_credit",
			ReferenceID: pmt.ID,
			Status:      "success",
			BalanceAfter: &bal,
		})
	})
	if err != nil {
		if errors.Is(err, payment.ErrDuplicateEvent) || errors.Is(err, payment.ErrAlreadyPaid) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "already_paid"})
			return
		}
		log.Error().Err(err).Str("payment_id", pmt.ID).Msg("PaymentWebhook failed to mark payment as paid")
		response.Error(w, "Failed to update payment", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelPaymentCompleted, eventbus.PaymentCompletedPayload{
		PaymentID: pmt.ID, RideID: pmt.RideID,
		UserID: pmt.UserID, DriverID: pmt.PartnerID,
		Amount: pmt.ChargedAmount, Mode: "online",
	})

	log.Info().Str("payment_id", pmt.ID).Str("order_id", event.Payload.Payment.Entity.OrderID).Msg("PaymentWebhook payment marked paid")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "processed"})
}

// handleRazorpayPaymentFailed records a failed capture so the bill is never
// left in limbo: the rider sees a failure (not a spinner), ops gets a log,
// and a later successful retry still settles normally via payment.captured.
func (h *PaymentHandler) handleRazorpayPaymentFailed(w http.ResponseWriter, r *http.Request, eventID, rzpPaymentID, orderID string) {
	log := logger.Ctx(r.Context())
	if orderID == "" {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored"})
		return
	}
	pmt, err := h.Store.FindPaymentByRazorpayOrderID(r.Context(), orderID)
	if err != nil {
		log.Error().Err(err).Msg("PaymentWebhook failed-event DB error")
		response.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	if pmt == nil || pmt.Paid {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored"})
		return
	}
	log.Warn().Str("payment_id", pmt.ID).Str("order_id", orderID).Str("rzp_payment_id", rzpPaymentID).Str("event_id", eventID).Msg("Razorpay payment failed for bill")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "recorded"})
}
