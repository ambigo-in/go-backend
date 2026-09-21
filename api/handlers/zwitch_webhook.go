package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"ambigo-backend/api/response"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/payment"

	"github.com/jackc/pgx/v5"
)

// zwitchEvent is the provider webhook envelope. The legacy Python handler
// observed {event, data{id, status, bank_reference_number, reason_for_error}};
// merchant_reference_id is carried through when Zwitch echoes it.
type zwitchEvent struct {
	Event string `json:"event"`
	Data  struct {
		ID                 string `json:"id"`
		Status             string `json:"status"`
		BankReferenceNo    string `json:"bank_reference_number"`
		ReasonForError     string `json:"reason_for_error"`
		MerchantReference  string `json:"merchant_reference_id"`
	} `json:"data"`
}

// verifyZwitchSignature checks X-Zwitch-Signature = HMAC-SHA256 hex of the raw
// body. Candidates in order: dedicated webhook secret (preferred), API secret,
// key id (legacy compat — the old Python handler signed with ZWITCH_KEY).
func (h *WalletHandler) verifyZwitchSignature(r *http.Request, body []byte) bool {
	sig := r.Header.Get("x-zwitch-signature")
	if sig == "" {
		return false
	}
	keys := []string{h.ZwitchWebhookSecret, h.ZwitchService.Secret, h.ZwitchService.KeyID}
	for _, k := range keys {
		if k == "" {
			continue
		}
		mac := hmac.New(sha256.New, []byte(k))
		mac.Write(body)
		if hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(strings.TrimSpace(sig))) {
			return true
		}
	}
	return false
}

// HandleZwitchWebhook receives transfer + verification events from Zwitch.
// It is the real-time signal; the 5-minute sweeper remains the backstop for
// anything missed. Served on both the canonical V2 path and the two legacy
// dashboard URLs so existing Zwitch endpoint configs keep working.
func (h *WalletHandler) HandleZwitchWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		response.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	if !h.verifyZwitchSignature(r, body) {
		response.Error(w, "Invalid signature", http.StatusUnauthorized)
		return
	}

	var event zwitchEvent
	if err := json.Unmarshal(body, &event); err != nil {
		response.Error(w, "Invalid webhook payload", http.StatusBadRequest)
		return
	}

	switch event.Event {
	case "transfers.updated", "transfer.updated", "payout.updated":
		h.handleZwitchTransferUpdate(w, r, body, &event)
	case "verifications.bank_account.created", "verification.updated", "bank_account.verified":
		h.handleZwitchVerification(w, r, body, &event)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored"})
	}
}

// handleZwitchTransferUpdate settles one withdrawal from provider truth.
// Claim + status update (+refund on failure) commit atomically; redelivery
// resolves from the stored row.
func (h *WalletHandler) handleZwitchTransferUpdate(w http.ResponseWriter, r *http.Request, body []byte, event *zwitchEvent) {
	log := logger.Ctx(r.Context())
	d := event.Data
	if d.ID == "" && d.MerchantReference == "" {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored"})
		return
	}

	claimKey := "zw:transfer:" + d.ID
	if d.ID == "" {
		claimKey = "zw:transfer:ref:" + d.MerchantReference
	}
	status := strings.ToLower(strings.TrimSpace(d.Status))
	failed := status == "failed" || status == "rejected" || status == "cancelled" || status == "reversed"

	// Read first for the post-commit event (never trust the Tx closure for it).
	notifyRow, _ := h.findWithdrawalTx(r, h.WalletStore, d.ID, d.MerchantReference)

	err := payment.WithTx(r.Context(), h.WalletStore.Pool(), func(tx pgx.Tx) error {
		if cerr := payment.ClaimEventTx(r.Context(), tx, claimKey, event.Event, body); cerr != nil {
			return cerr
		}
		wTx := h.WalletStore.WithTx(tx)
		row, ferr := h.findWithdrawalTx(r, wTx, d.ID, d.MerchantReference)
		if ferr != nil {
			return ferr
		}
		if row == nil {
			return payment.ErrNoTransaction
		}
		if uerr := wTx.UpdateTransactionStatus(r.Context(), row.DriverID, row.MerchantReferenceID, d.Status, d.BankReferenceNo, d.ID, d.ReasonForError); uerr != nil {
			return uerr
		}
		if failed {
			gross, gerr := withdrawalGross(r, wTx, row.DriverID, row.MerchantReferenceID)
			if gerr != nil {
				return gerr
			}
			bal, rerr := wTx.AdjustWalletBalance(r.Context(), row.DriverID, gross)
			if rerr != nil {
				return rerr
			}
			return wTx.InsertLedgerEntry(r.Context(), &payment.WalletTransaction{
				DriverID:    row.DriverID,
				Amount:      gross,
				Direction:   "credit",
				TxnType:     "refund",
				ReferenceID: row.MerchantReferenceID,
				Status:      "success",
				BalanceAfter: &bal,
			})
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, payment.ErrDuplicateEvent) || errors.Is(err, payment.ErrNoTransaction) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "already_processed"})
			return
		}
		log.Error().Err(err).Str("transfer_id", d.ID).Msg("Zwitch transfer webhook failed")
		response.Error(w, "Failed to process transfer update", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelWalletWithdrawal, eventbus.WalletWithdrawalPayload{
		DriverID: notifyDriverID(notifyRow),
		Amount:   notifyAmount(notifyRow),
		Status:   d.Status,
	})
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "processed"})
}

// handleZwitchVerification flips the first-payout gate when the provider
// confirms the penny-drop / name-match. Correlated by the merchant reference
// persisted with the verification request — never by client identity.
func (h *WalletHandler) handleZwitchVerification(w http.ResponseWriter, r *http.Request, body []byte, event *zwitchEvent) {
	log := logger.Ctx(r.Context())
	ref := strings.TrimSpace(event.Data.MerchantReference)
	if ref == "" {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored"})
		return
	}
	if !isVerificationSuccess(event.Data.Status) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "recorded"})
		return
	}

	err := payment.WithTx(r.Context(), h.WalletStore.Pool(), func(tx pgx.Tx) error {
		if cerr := payment.ClaimEventTx(r.Context(), tx, "zw:verify:"+ref, event.Event, body); cerr != nil {
			return cerr
		}
		wTx := h.WalletStore.WithTx(tx)
		driverID, ferr := wTx.FindDriverByVerifyRef(r.Context(), ref)
		if ferr != nil {
			return ferr
		}
		if driverID == "" {
			return payment.ErrNoTransaction
		}
		return wTx.SetWalletVerified(r.Context(), driverID, true)
	})
	if err != nil {
		if errors.Is(err, payment.ErrDuplicateEvent) || errors.Is(err, payment.ErrNoTransaction) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "already_processed"})
			return
		}
		log.Error().Err(err).Str("ref", ref).Msg("Zwitch verification webhook failed")
		response.Error(w, "Failed to process verification", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "processed"})
}

func isVerificationSuccess(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "success", "successful", "verified", "completed", "approved":
		return true
	default:
		return false
	}
}

// findWithdrawalTx locates the withdrawal row by provider transfer id,
// falling back to our merchant reference.
func (h *WalletHandler) findWithdrawalTx(r *http.Request, wTx *payment.WalletStore, transferID, merchantRef string) (*payment.WalletTransaction, error) {
	if transferID != "" {
		if row, err := wTx.FindByTransferID(r.Context(), transferID); err != nil {
			return nil, err
		} else if row != nil {
			return row, nil
		}
	}
	if merchantRef != "" {
		return wTx.FindByMerchantRefAny(r.Context(), merchantRef)
	}
	return nil, nil
}

// withdrawalGross recomputes the deductible gross (net + fee debits) so
// refunds never underpay by the fee, and detects an already-posted refund.
func withdrawalGross(r *http.Request, wTx *payment.WalletStore, driverID, merchantRef string) (float64, error) {
	rows, err := wTx.FindByReference(r.Context(), driverID, merchantRef)
	if err != nil {
		return 0, err
	}
	gross := 0.0
	for _, row := range rows {
		if row.TxnType == "refund" && row.Direction == "credit" {
			return 0, payment.ErrNoTransaction // already compensated
		}
		if row.Direction == "debit" && (row.TxnType == "withdrawal" || row.TxnType == "fee") {
			gross += row.Amount
		}
	}
	return gross, nil
}

func notifyDriverID(row *payment.WalletTransaction) string {
	if row == nil {
		return ""
	}
	return row.DriverID
}

func notifyAmount(row *payment.WalletTransaction) float64 {
	if row == nil {
		return 0
	}
	return row.Amount
}
