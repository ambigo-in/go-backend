package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/ids"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/payment"
	"ambigo-backend/internal/requestid"
	"ambigo-backend/internal/retry"

	"github.com/jackc/pgx/v5"
)

type WalletHandler struct {
	AuthStore           *auth.Store
	EventBus            *eventbus.InMemoryBus
	WalletStore         *payment.WalletStore
	ZwitchService       *payment.ZwitchService
	WithdrawalFee       float64
	ZwitchWebhookSecret string
}

func NewWalletHandler(authStore *auth.Store, eventBus *eventbus.InMemoryBus, wStore *payment.WalletStore, zService *payment.ZwitchService) *WalletHandler {
	return &WalletHandler{
		AuthStore:     authStore,
		EventBus:      eventBus,
		WalletStore:   wStore,
		ZwitchService: zService,
		WithdrawalFee: 7,
	}
}

// SetWithdrawalFee overrides the default Rs 7 fee (from WITHDRAWAL_FEE env).
func (h *WalletHandler) SetWithdrawalFee(fee float64) {
	if fee >= 0 {
		h.WithdrawalFee = fee
	}
}

// SetZwitchWebhookSecret sets the HMAC secret for Zwitch webhooks
// (ZWITCH_WEBHOOK_SECRET env). Empty falls back to API secret, then key id
// (legacy compat with the old Python handler).
func (h *WalletHandler) SetZwitchWebhookSecret(secret string) {
	h.ZwitchWebhookSecret = secret
}

func (h *WalletHandler) fee() float64 {
	if h.WithdrawalFee < 0 {
		return 7
	}
	return h.WithdrawalFee
}

func (h *WalletHandler) HandleGetWallet(w http.ResponseWriter, r *http.Request) {
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	driver, err := h.AuthStore.FindDriverByID(r.Context(), uidStr)
	if err != nil || driver == nil {
		response.Error(w, "Driver not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(driver.WalletDetails)
}

func (h *WalletHandler) HandleUpdateWallet(w http.ResponseWriter, r *http.Request) {
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req auth.WalletDetails
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	// Client must never set BenfID (provider-issued). Ignore any submitted
	// value so a forged beneficiary id can never be persisted.
	req.BenfID = ""
	req.AccountNo = strings.TrimSpace(req.AccountNo)
	req.IFSCCode = strings.ToUpper(strings.TrimSpace(req.IFSCCode))
	req.BenfName = strings.TrimSpace(req.BenfName)
	if err := validateWalletDetails(&req); err != nil {
		response.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	driver, err := h.AuthStore.FindDriverByID(r.Context(), uidStr)
	if err != nil || driver == nil {
		response.Error(w, "Driver not found", http.StatusNotFound)
		return
	}

	// WalletDetails is optional at driver creation (admin add); handle nil
	var dbAcc *auth.WalletDetails
	if driver.WalletDetails != nil {
		dbAcc = driver.WalletDetails
	} else {
		dbAcc = &auth.WalletDetails{}
	}
	accountChanged := dbAcc.AccountNo == "" || !strings.EqualFold(dbAcc.AccountNo, req.AccountNo) || !strings.EqualFold(dbAcc.IFSCCode, req.IFSCCode)
	if dbAcc.AccountNo == "" {
		// New beneficiary
		benfID, err := h.ZwitchService.CreateBeneficiary(&req, uidStr)
		if err != nil || benfID == "" {
			response.Error(w, "Bank beneficiary creation failed", http.StatusBadRequest)
			return
		}
		req.BenfID = benfID
	} else if !accountChanged {
		// Same account (number + IFSC): only the holder name may change.
		req.BenfID = dbAcc.BenfID
		if req.BenfName != dbAcc.BenfName {
			if uerr := h.ZwitchService.UpdateBeneficiaryName(&req); uerr != nil {
				logger.Log.Error().Err(uerr).Str("driver_id", uidStr).Msg("Beneficiary rename failed; keeping stored details")
				response.Error(w, "Failed to update beneficiary name", http.StatusBadGateway)
				return
			}
		}
	} else {
		// Account changed entirely: create first, delete old only on success,
		// and never lose the working beneficiary to a failed create.
		benfID, err := h.ZwitchService.CreateBeneficiary(&req, uidStr)
		if err != nil || benfID == "" {
			response.Error(w, "Bank beneficiary creation failed", http.StatusBadRequest)
			return
		}
		req.BenfID = benfID
		if dbAcc.BenfID != "" {
			if derr := h.ZwitchService.DeleteBeneficiary(dbAcc.BenfID); derr != nil {
				// Non-fatal: new beneficiary works; old one leaks at the
				// provider. Log for cleanup instead of failing the driver.
				logger.Log.Error().Err(derr).Str("driver_id", uidStr).Str("benf_id", dbAcc.BenfID).Msg("Stale beneficiary cleanup failed")
			}
		}
	}

	// Verification gate: a changed account must re-verify before it can
	// receive money. Details-write + flag-reset commit in ONE transaction —
	// a crash between them must never leave a new, unverified account under
	// the old `true` (which would let withdrawals flow to an attacker
	// account whose verification failed).
	// Also verify whenever the driver is currently unverified (e.g. a prior
	// verification failed and the driver re-saves the same details).
	needsVerify := accountChanged || !driver.WalletVerified
	if accountChanged {
		if verr := payment.WithTx(r.Context(), h.WalletStore.Pool(), func(tx pgx.Tx) error {
			wTx := h.WalletStore.WithTx(tx)
			if uerr := wTx.UpdateWalletDetails(r.Context(), uidStr, req); uerr != nil {
				return uerr
			}
			return wTx.SetWalletVerified(r.Context(), uidStr, false)
		}); verr != nil {
			logger.Log.Error().Err(verr).Str("driver_id", uidStr).Msg("Failed to save bank details")
			response.Error(w, "Failed to save bank details, please retry", http.StatusInternalServerError)
			return
		}
	} else {
		if err := h.WalletStore.UpdateWalletDetails(r.Context(), uidStr, req); err != nil {
			response.Error(w, "Error updating wallet details", http.StatusInternalServerError)
			return
		}
	}
	if needsVerify {
		if verr := h.verifyAndMarkAccount(r, uidStr, &req); verr != nil {
			logger.Log.Error().Err(verr).Str("driver_id", uidStr).Msg("Bank verification failed")
			json.NewEncoder(w).Encode(map[string]string{"detail": "Bank details saved. Verification failed — please check the account and retry before withdrawing."})
			return
		}
	}

	json.NewEncoder(w).Encode(map[string]string{"detail": "Wallet details updated successfully"})
}

// ifscRe is the RBI IFSC format: 4-letter bank code, 0, 6-char branch code.
var ifscRe = regexp.MustCompile(`^[A-Z]{4}0[A-Z0-9]{6}$`)

// validateWalletDetails rejects empty/malformed bank fields before anything
// is sent to the provider. Previously any string (including "") was trusted
// and paid out.
func validateWalletDetails(d *auth.WalletDetails) error {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, d.AccountNo)
	if len(digits) < 9 || len(digits) > 18 {
		return errors.New("account number must be 9-18 digits")
	}
	if !ifscRe.MatchString(d.IFSCCode) {
		return errors.New("IFSC code format is invalid (expected e.g. HDFC0001234)")
	}
	if len(d.BenfName) < 3 {
		return errors.New("account holder name is required")
	}
	return nil
}

// verifyAndMarkAccount runs bank verification (penny-drop / name-match via
// Zwitch) and flips the first-payout gate on success. Verification is the
// industry-standard control between "driver typed an account" and "money
// leaves the platform". The merchant reference is persisted first so the
// async verifications.bank_account.created webhook can correlate even when
// the synchronous call is inconclusive.
func (h *WalletHandler) verifyAndMarkAccount(r *http.Request, driverID string, acc *auth.WalletDetails) error {
	ref := fmt.Sprintf("V%s", strings.ReplaceAll(ids.New(), "-", ""))
	if serr := h.WalletStore.SetWalletVerifyRef(r.Context(), driverID, ref); serr != nil {
		return serr
	}
	status, err := h.ZwitchService.VerifyBankAccount(acc, ref)
	if err != nil {
		return err
	}
	if !isVerificationSuccess(status) {
		return fmt.Errorf("bank verification inconclusive: %s", status)
	}
	return payment.WithTx(r.Context(), h.WalletStore.Pool(), func(tx pgx.Tx) error {
		wTx := h.WalletStore.WithTx(tx)
		return wTx.SetWalletVerified(r.Context(), driverID, true)
	})
}

func (h *WalletHandler) HandleWithdraw(w http.ResponseWriter, r *http.Request) {
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	reqID := requestid.FromContext(r.Context())

	var req struct {
		Amount float64 `json:"amount" validate:"required,gt=0"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &req) {
		return
	}

	// Normalize to paise-rounded rupees once: every downstream use (debit,
	// journal, debounce compare, provider amount) must agree to the paise,
	// or float dust breaks equality and reconciliation.
	req.Amount = payment.RoundRupees(req.Amount)
	if req.Amount <= 0 {
		response.Error(w, "Invalid amount", http.StatusBadRequest)
		return
	}

	driver, err := h.AuthStore.FindDriverByID(r.Context(), uidStr)
	if err != nil || driver == nil {
		response.Error(w, "Driver not found", http.StatusNotFound)
		return
	}

	if driver.WalletDetails == nil || driver.WalletDetails.BenfID == "" {
		response.Error(w, "Driver Account Details not found", http.StatusBadRequest)
		return
	}

	// First-payout gate: details must have passed bank verification.
	if !driver.WalletVerified {
		response.Error(w, "Bank account not verified yet — please save your bank details and complete verification first", http.StatusForbidden)
		return
	}

	// Platform fee for withdrawal (configurable via WITHDRAWAL_FEE, default Rs 7)
	fee := h.fee()
	amountToTransfer := payment.RoundRupees(req.Amount - fee)
	if amountToTransfer <= 0 {
		response.Error(w, fmt.Sprintf("Amount too low to cover %.0frs fee", fee), http.StatusBadRequest)
		return
	}
	if amountToTransfer < 1 {
		response.Error(w, "Net transfer amount must be at least Rs 1", http.StatusBadRequest)
		return
	}

	// Idempotency key: client may send one (double-tap / timeout retry).
	// Old apps send none — for those, debounce on the stored NET row: a
	// pending withdrawal of the same net amount created within 60s is treated
	// as the same tap. (Same-amount back-to-back withdrawals inside 60s will
	// also collapse — documented tradeoff; retry after a minute.)
	clientKey := strings.TrimSpace(r.Header.Get("X-Idempotency-Key"))
	merchantRefID := clientKey
	if merchantRefID == "" {
		if dup, derr := h.WalletStore.FindRecentPending(r.Context(), uidStr, amountToTransfer, time.Minute); derr != nil {
			logger.Log.Error().Err(derr).Str("driver_id", uidStr).Msg("Debounce lookup failed")
		} else if dup != nil {
			h.writeWithdrawalStatus(w, r, uidStr, dup.MerchantReferenceID)
			return
		}
		merchantRefID = "W" + strings.ReplaceAll(ids.New(), "-", "")
	}
	if len(merchantRefID) > 64 {
		response.Error(w, "Idempotency key too long", http.StatusBadRequest)
		return
	}

	// Saga Tx1: deduct gross + insert rows atomically. Journal integrity:
	// the withdrawal row carries the NET (bank-bound) amount and the fee row
	// carries the platform fee, so rows always sum to the balance delta
	// (-net -fee = -gross). A repeat delivery with the same idempotency key
	// hits UNIQUE(driver_id, merchant_reference_id) and reports the stored
	// outcome instead of double-deducting.
	pendingTx := &payment.WalletTransaction{
		DriverID:            uidStr,
		ZwitchBeneficiaryID: driver.WalletDetails.BenfID,
		Amount:              amountToTransfer,
		AccountNo:           driver.WalletDetails.AccountNo,
		MerchantReferenceID: merchantRefID,
		Direction:           "debit",
		TxnType:             "withdrawal",
		ReferenceID:         merchantRefID,
		Status:              "pending",
	}
	if err := payment.WithTx(r.Context(), h.WalletStore.Pool(), func(tx pgx.Tx) error {
		wTx := h.WalletStore.WithTx(tx)
		if err := wTx.DeductBalance(r.Context(), uidStr, req.Amount); err != nil {
			return err
		}
		bal, berr := wTx.GetBalance(r.Context(), uidStr)
		if berr != nil {
			return berr
		}
		pendingTx.BalanceAfter = &bal
		if err := wTx.InsertTransaction(r.Context(), pendingTx); err != nil {
			return err
		}
		return wTx.InsertLedgerEntry(r.Context(), &payment.WalletTransaction{
			DriverID:    uidStr,
			Amount:      fee,
			Direction:   "debit",
			TxnType:     "fee",
			ReferenceID: merchantRefID,
			Status:      "success",
			BalanceAfter: &bal,
		})
	}); err != nil {
		if isDuplicateKey(err) {
			// Retry of an already-accepted withdrawal: report current state.
			h.writeWithdrawalStatus(w, r, uidStr, merchantRefID)
			return
		}
		if strings.Contains(err.Error(), "insufficient wallet balance") {
			response.Error(w, "Insufficient wallet balance", http.StatusBadRequest)
			return
		}
		logger.Log.Error().Err(err).Str("driver_id", uidStr).Msg("Withdrawal Tx1 failed")
		response.Error(w, "Failed to initiate withdrawal, please retry", http.StatusInternalServerError)
		return
	}

	resp, err := h.ZwitchService.CreateTransfer(driver.WalletDetails, amountToTransfer, merchantRefID)
	log := logger.Ctx(r.Context())
	if err != nil || resp == nil {
		var nre *retry.NonRetryableError
		if errors.As(err, &nre) {
			// Definite provider rejection (4xx: bad beneficiary, bad amount,
			// auth): money certainly did not move — refund now.
			log.Error().Err(err).Msg("Zwitch transfer rejected, refunding wallet")
			if rerr := h.refundWithdrawal(r.Context(), uidStr, merchantRefID, req.Amount, "", "", errText(err)); rerr != nil {
				if errors.Is(rerr, payment.ErrNoTransaction) {
					h.writeWithdrawalOutcome(w, r, uidStr, merchantRefID)
					return
				}
				log.Error().Err(rerr).Str("merchant_ref", merchantRefID).Msg("Refund also failed -- manual intervention required")
			}
			response.Error(w, "Withdrawal Initiation failed", http.StatusBadRequest)
			return
		}
		// Ambiguous (timeout / 5xx / empty response, with or without a body):
		// the provider may HAVE moved the money. Never refund here — leave
		// pending for the sweeper, which settles via FetchTransfer truth.
		log.Error().Err(err).Str("merchant_ref", merchantRefID).Msg("Zwitch transfer ambiguous, leaving pending for sweeper")
		h.writeWithdrawalStatus(w, r, uidStr, merchantRefID)
		return
	}

	// Save transaction result — handle Zwitch status
	status, _ := resp["status"].(string)
	bankRef, _ := resp["bank_reference_number"].(string)
	transferID, _ := resp["id"].(string)
	errMsg := ""
	if reason, ok := resp["reason_for_error"].(string); ok {
		errMsg = reason
	}

	// If Zwitch reports failed, compensating refund + ledger row.
	// ErrNoTransaction means a concurrent settler already closed the row —
	// answer from the stored outcome instead of failing blindly.
	if status == "failed" {
		log.Error().Str("merchant_ref", merchantRefID).Str("status", status).Msg("Zwitch returned failed, refunding")
		if rerr := h.refundWithdrawal(r.Context(), uidStr, merchantRefID, req.Amount, bankRef, transferID, errMsg); rerr != nil {
			if errors.Is(rerr, payment.ErrNoTransaction) {
				h.writeWithdrawalOutcome(w, r, uidStr, merchantRefID)
				return
			}
			log.Error().Err(rerr).Str("merchant_ref", merchantRefID).Msg("Refund also failed -- manual intervention required")
		}
		response.Error(w, "Withdrawal Initiation failed", http.StatusBadRequest)
		return
	}

	// Success or pending — update transaction status in Tx2 (scoped, checked).
	// ErrNoTransaction means a concurrent settler already closed the row:
	// answer from the stored outcome instead of claiming success blindly.
	if updErr := payment.WithTx(r.Context(), h.WalletStore.Pool(), func(tx pgx.Tx) error {
		wTx := h.WalletStore.WithTx(tx)
		return wTx.UpdateTransactionStatus(r.Context(), uidStr, merchantRefID, status, bankRef, transferID, errMsg)
	}); updErr != nil {
		if errors.Is(updErr, payment.ErrNoTransaction) {
			h.writeWithdrawalOutcome(w, r, uidStr, merchantRefID)
			return
		}
		log.Error().Err(updErr).Str("merchant_ref", merchantRefID).Msg("Failed to update wallet transaction status")
	}

	h.EventBus.PublishEvent(eventbus.ChannelWalletWithdrawal, eventbus.WalletWithdrawalPayload{
		DriverID: uidStr, Amount: req.Amount, Status: status, RequestID: reqID,
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Withdrawal initiated, amount will be transferred shortly!!"})
}

// writeWithdrawalStatus answers an idempotent retry with the stored outcome
// instead of creating a second withdrawal.
func (h *WalletHandler) writeWithdrawalStatus(w http.ResponseWriter, r *http.Request, driverID, merchantRefID string) {
	h.writeWithdrawalOutcome(w, r, driverID, merchantRefID)
}

// writeWithdrawalOutcome reports the stored row state: in-flight rows get
// "in progress", settled rows get their terminal outcome. Used whenever a
// settlement race is lost — the stored row, not a guess, decides the answer.
func (h *WalletHandler) writeWithdrawalOutcome(w http.ResponseWriter, r *http.Request, driverID, merchantRefID string) {
	w.Header().Set("Content-Type", "application/json")
	row, err := h.WalletStore.FindByMerchantRef(r.Context(), driverID, merchantRefID)
	if err != nil || row == nil {
		json.NewEncoder(w).Encode(map[string]string{"detail": "Withdrawal status unknown — please check history or retry", "merchant_reference_id": merchantRefID})
		return
	}
	switch row.Status {
	case "failed":
		json.NewEncoder(w).Encode(map[string]string{"detail": "Withdrawal failed", "merchant_reference_id": merchantRefID})
	case "success", "processed", "completed", "settled":
		json.NewEncoder(w).Encode(map[string]string{"detail": "Withdrawal successful", "merchant_reference_id": merchantRefID, "status": row.Status})
	default:
		json.NewEncoder(w).Encode(map[string]string{"detail": "Withdrawal already in progress", "merchant_reference_id": merchantRefID, "status": row.Status})
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// isDuplicateKey reports Postgres unique-violation (SQLSTATE 23505) without
// importing pgconn at the handler layer.
func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key")
}

// StartPendingSweeper resolves stuck withdrawals: every interval it finds
// 'pending' rows older than maxAge, asks Zwitch for the truth, and settles
// each one (success/pending → update status; failed/unknown → refund +
// ledger row). Without this, a crash between provider-success and DB-update
// leaves money deducted-but-unsent forever.
func (h *WalletHandler) StartPendingSweeper(interval, maxAge time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			n, err := h.reconcilePending(ctx, maxAge)
			cancel()
			if err != nil {
				logger.Log.Error().Err(err).Msg("Withdrawal sweeper error")
			} else if n > 0 {
				logger.Log.Info().Int("count", n).Msg("Withdrawal sweeper settled stuck rows")
			}
		}
	}()
}

func (h *WalletHandler) reconcilePending(ctx context.Context, maxAge time.Duration) (int, error) {
	stuck, err := h.WalletStore.ListStuckPending(ctx, maxAge, 100)
	if err != nil {
		return 0, err
	}
	settled := 0
	for _, sw := range stuck {
		// Full journal for this withdrawal: withdrawal (net) + fee rows share
		// reference_id. Gross = sum of debits; a posted refund row means a
		// concurrent settler already compensated — never refund twice.
		rows, rerr := h.WalletStore.FindByReference(ctx, sw.DriverID, sw.MerchantReferenceID)
		if rerr != nil {
			continue
		}
		alreadyRefunded := false
		gross := 0.0
		for _, row := range rows {
			if row.TxnType == "refund" && row.Direction == "credit" {
				alreadyRefunded = true
			}
			if row.Direction == "debit" && (row.TxnType == "withdrawal" || row.TxnType == "fee") {
				gross += row.Amount
			}
		}
		if alreadyRefunded {
			// Money is back; just close the row if it is still pending.
			if uerr := h.WalletStore.UpdateTransactionStatus(ctx, sw.DriverID, sw.MerchantReferenceID, "failed", "", sw.ZwitchTransferID, "already refunded"); uerr != nil && !errors.Is(uerr, payment.ErrNoTransaction) {
				continue
			}
			settled++
			continue
		}
		if gross <= 0 {
			gross = sw.Amount // fallback: legacy row shape
		}
		if sw.ZwitchTransferID == "" {
			// Never reached the provider as far as we know — but an
			// ambiguous transport error looks identical. Refund only after
			// 24h (beyond any plausible settlement); before that, leave it
			// for a later tick. Refunding sent money would double-pay.
			if time.Since(sw.CreatedAt) < 24*time.Hour {
				continue
			}
			if rerr := h.refundWithdrawal(ctx, sw.DriverID, sw.MerchantReferenceID, gross, "", "", "never reached provider"); rerr != nil {
				if errors.Is(rerr, payment.ErrNoTransaction) {
					settled++
					continue
				}
				logger.Log.Error().Err(rerr).Str("merchant_ref", sw.MerchantReferenceID).Msg("Sweeper refund failed")
				continue
			}
			settled++
			continue
		}
		st, ferr := h.ZwitchService.FetchTransfer(sw.ZwitchTransferID)
		if ferr != nil {
			var nre *retry.NonRetryableError
			if errors.As(ferr, &nre) {
				// Provider does not know this transfer (unknown id): it will
				// never settle, so fail + refund instead of stalling forever.
				if rerr := h.refundWithdrawal(ctx, sw.DriverID, sw.MerchantReferenceID, gross, "", sw.ZwitchTransferID, "provider reports unknown transfer"); rerr != nil {
					logger.Log.Error().Err(rerr).Str("merchant_ref", sw.MerchantReferenceID).Msg("Sweeper refund failed")
					continue
				}
				settled++
				continue
			}
			continue // transient: retry next tick
		}
		switch strings.ToLower(st.Status) {
		case "success", "processed", "completed", "settled":
			if uerr := h.WalletStore.UpdateTransactionStatus(ctx, sw.DriverID, sw.MerchantReferenceID, st.Status, st.BankReferenceNo, st.TransferID, ""); uerr != nil {
				continue
			}
			settled++
		case "failed", "rejected", "cancelled", "reversed":
			if rerr := h.refundWithdrawal(ctx, sw.DriverID, sw.MerchantReferenceID, gross, st.BankReferenceNo, st.TransferID, st.ErrorMessage); rerr != nil {
				logger.Log.Error().Err(rerr).Str("merchant_ref", sw.MerchantReferenceID).Msg("Sweeper refund failed")
				continue
			}
			settled++
		default:
			// Still in flight at provider — leave pending for next tick.
		}
	}
	return settled, nil
}

// refundWithdrawal credits the full gross amount back and closes the row as
// failed, with a ledger entry. Shared by the request path and the sweeper.
func (h *WalletHandler) refundWithdrawal(ctx context.Context, driverID, merchantRefID string, gross float64, bankRef, transferID, errMsg string) error {
	return payment.WithTx(ctx, h.WalletStore.Pool(), func(tx pgx.Tx) error {
		wTx := h.WalletStore.WithTx(tx)
		bal, rerr := wTx.AdjustWalletBalance(ctx, driverID, gross)
		if rerr != nil {
			return rerr
		}
		if uerr := wTx.UpdateTransactionStatus(ctx, driverID, merchantRefID, "failed", bankRef, transferID, errMsg); uerr != nil {
			return uerr
		}
		return wTx.InsertLedgerEntry(ctx, &payment.WalletTransaction{
			DriverID:    driverID,
			Amount:      gross,
			Direction:   "credit",
			TxnType:     "refund",
			ReferenceID: merchantRefID,
			Status:      "success",
			BalanceAfter: &bal,
		})
	})
}

func (h *WalletHandler) HandleListTransactions(w http.ResponseWriter, r *http.Request) {
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	limit := 50
	cursor := r.URL.Query().Get("cursor")
	if cursor == "" {
		cursor = r.URL.Query().Get("after_id")
	}
	offset := 0
	if v := r.URL.Query().Get("skip"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	} else if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	// Allow JSON body {limit,cursor,skip} for POST compatibility.
	if r.Body != nil && r.ContentLength != 0 {
		var body struct {
			Limit   *int   `json:"limit"`
			Cursor  string `json:"cursor"`
			AfterID string `json:"after_id"`
			Skip    *int   `json:"skip"`
			Offset  *int   `json:"offset"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Limit != nil && *body.Limit > 0 {
			n := *body.Limit
			if n > 50 {
				n = 50
			}
			limit = n
		}
		if body.Cursor != "" {
			cursor = body.Cursor
		} else if body.AfterID != "" {
			cursor = body.AfterID
		}
		if body.Skip != nil && *body.Skip >= 0 {
			offset = *body.Skip
		} else if body.Offset != nil && *body.Offset >= 0 {
			offset = *body.Offset
		}
	}

	var list []payment.WalletTransaction
	var err error
	if offset > 0 && cursor == "" {
		list, err = h.WalletStore.ListTransactionsWithOffset(r.Context(), uidStr, limit, offset)
	} else {
		list, err = h.WalletStore.ListTransactionsPaginated(r.Context(), uidStr, limit, cursor)
	}
	if err != nil {
		response.Error(w, "Failed to list transactions", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}
