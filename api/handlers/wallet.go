package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/payment"
	"ambigo-backend/internal/requestid"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type WalletHandler struct {
	AuthStore     *auth.Store
	EventBus      *eventbus.InMemoryBus
	WalletStore   *payment.WalletStore
	ZwitchService *payment.ZwitchService
}

func NewWalletHandler(authStore *auth.Store, eventBus *eventbus.InMemoryBus, wStore *payment.WalletStore, zService *payment.ZwitchService) *WalletHandler {
	return &WalletHandler{
		AuthStore:     authStore,
		EventBus:      eventBus,
		WalletStore:   wStore,
		ZwitchService: zService,
	}
}

func (h *WalletHandler) HandleGetWallet(w http.ResponseWriter, r *http.Request) {
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	objID, _ := primitive.ObjectIDFromHex(uidStr)
	driver, err := h.AuthStore.FindDriverByID(r.Context(), objID)
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
	if !response.Validate(w, &req) {
		return
	}

	objID, _ := primitive.ObjectIDFromHex(uidStr)
	driver, err := h.AuthStore.FindDriverByID(r.Context(), objID)
	if err != nil || driver == nil {
		response.Error(w, "Driver not found", http.StatusNotFound)
		return
	}

	dbAcc := driver.WalletDetails
	if dbAcc.AccountNo == "" {
		// New beneficiary
		benfID, err := h.ZwitchService.CreateBeneficiary(&req, uidStr)
		if err != nil || benfID == "" {
			response.Error(w, "Zwitch Beneficiary Account Creation error", http.StatusBadRequest)
			return
		}
		req.BenfID = benfID
	} else {
		// Update existing
		if dbAcc.AccountNo == req.AccountNo {
			req.BenfID = dbAcc.BenfID
			h.ZwitchService.UpdateBeneficiaryName(&req)
		} else {
			// Account changed entirely, recreate
			benfID, err := h.ZwitchService.CreateBeneficiary(&req, uidStr)
			if err != nil || benfID == "" {
				response.Error(w, "Zwitch Beneficiary Account Creation error", http.StatusBadRequest)
				return
			}
			req.BenfID = benfID
			h.ZwitchService.DeleteBeneficiary(dbAcc.BenfID)
		}
	}

	if err := h.WalletStore.UpdateWalletDetails(r.Context(), objID, req); err != nil {
		response.Error(w, "Error updating wallet details", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"detail": "Wallet details updated successfully"})
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

	objID, _ := primitive.ObjectIDFromHex(uidStr)
	driver, err := h.AuthStore.FindDriverByID(r.Context(), objID)
	if err != nil || driver == nil {
		response.Error(w, "Driver not found", http.StatusNotFound)
		return
	}

	if driver.WalletBalance < req.Amount {
		response.Error(w, "Requested amount is greater than wallet balance!!", http.StatusBadRequest)
		return
	}

	if driver.WalletDetails.BenfID == "" {
		response.Error(w, "Driver Account Details not found", http.StatusBadRequest)
		return
	}

	// Reference ID = random 10 chars, simplified here using timestamp
	merchantRefID := fmt.Sprintf("W%d", time.Now().UnixNano())

	// Rs. 7 fee for transaction
	amountToTransfer := req.Amount - 7
	if amountToTransfer <= 0 {
		response.Error(w, "Amount too low to cover 7rs fee", http.StatusBadRequest)
		return
	}

	resp, err := h.ZwitchService.CreateTransfer(&driver.WalletDetails, amountToTransfer, merchantRefID)
	if err != nil || resp == nil {
		response.Error(w, "Withdrawal Initiation failed", http.StatusBadRequest)
		return
	}

	// Deduct balance
	h.WalletStore.UpdateWalletBalance(r.Context(), objID, -req.Amount)

	// Save transaction log
	status, _ := resp["status"].(string)
	bankRef, _ := resp["bank_reference_number"].(string)
	transferID, _ := resp["id"].(string)

	tx := &payment.WalletTransaction{
		DriverID:            uidStr,
		ZwitchBeneficiaryID: driver.WalletDetails.BenfID,
		Amount:              req.Amount,
		AccountNo:           driver.WalletDetails.AccountNo,
		MerchantReferenceID: merchantRefID,
		BankReferenceNo:     bankRef,
		ZwitchTransferID:    transferID,
		Status:              status,
	}

	if status == "failed" || status == "pending" {
		if reason, ok := resp["reason_for_error"].(string); ok {
			tx.ErrorMessage = reason
		}
	}

	h.WalletStore.InsertTransaction(r.Context(), tx)

	h.EventBus.PublishEvent(eventbus.ChannelWalletWithdrawal, eventbus.WalletWithdrawalPayload{
		DriverID: uidStr, Amount: req.Amount, Status: status, RequestID: reqID,
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Withdrawal initiated, amount will be transferred shortly!!"})
}

func (h *WalletHandler) HandleListTransactions(w http.ResponseWriter, r *http.Request) {
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	list, err := h.WalletStore.ListTransactions(r.Context(), uidStr)
	if err != nil {
		response.Error(w, "Failed to list transactions", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}
