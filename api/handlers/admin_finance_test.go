package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminFinanceHandler_NilPool(t *testing.T) {
	h := &AdminFinanceHandler{}

	t.Run("HandleFinanceSummary nil pool", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v2/admin/finance/summary", bytes.NewBufferString("{}"))
		w := httptest.NewRecorder()
		h.HandleFinanceSummary(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500, got %d", w.Code)
		}
	})

	t.Run("HandleListLedgerTransactions nil pool", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v2/admin/ledger/transactions", bytes.NewBufferString("{}"))
		w := httptest.NewRecorder()
		h.HandleListLedgerTransactions(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500, got %d", w.Code)
		}
	})

	t.Run("HandleListAdminPayments nil pool", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v2/admin/payments/list", bytes.NewBufferString("{}"))
		w := httptest.NewRecorder()
		h.HandleListAdminPayments(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500, got %d", w.Code)
		}
	})
}

func TestFinanceSummaryResponse_Serialization(t *testing.T) {
	var resp FinanceSummaryResponse
	resp.Inflow.GrossVolume = 150000.50
	resp.Inflow.OnlineVolume = 100000.00
	resp.Inflow.OnlineCount = 50
	resp.Inflow.CashVolume = 50000.50
	resp.Inflow.CashCount = 25
	resp.Inflow.PlatformRevenue = 22500.00
	resp.Inflow.DriverEarnings = 127500.50

	resp.Outflow.PayoutsSettledVolume = 85000.00
	resp.Outflow.PayoutsSettledCount = 20
	resp.Outflow.PayoutsPendingVolume = 5000.00
	resp.Outflow.PayoutsPendingCount = 2

	resp.WalletLiability.TotalDriverFloat = 42500.00
	resp.WalletLiability.ActiveWalletsCount = 18

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal FinanceSummaryResponse: %v", err)
	}

	var decoded FinanceSummaryResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal FinanceSummaryResponse: %v", err)
	}

	if decoded.Inflow.GrossVolume != 150000.50 {
		t.Errorf("expected gross volume 150000.50, got %f", decoded.Inflow.GrossVolume)
	}
	if decoded.Outflow.PayoutsPendingCount != 2 {
		t.Errorf("expected pending count 2, got %d", decoded.Outflow.PayoutsPendingCount)
	}
	if decoded.WalletLiability.ActiveWalletsCount != 18 {
		t.Errorf("expected active wallets 18, got %d", decoded.WalletLiability.ActiveWalletsCount)
	}
}
