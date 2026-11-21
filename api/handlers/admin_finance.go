package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"ambigo-backend/api/response"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/payment"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminFinanceHandler serves endpoints for platform financial summaries,
// double-entry wallet transaction ledgers, and ride billing records.
type AdminFinanceHandler struct {
	PaymentStore *payment.Store
	WalletStore  *payment.WalletStore
	AuthStore    *auth.Store
	Pool         *pgxpool.Pool
}

// FinanceSummaryResponse mirrors the spec in 11-admin-payment-and-financial-ledger.md
type FinanceSummaryResponse struct {
	Period struct {
		From *time.Time `json:"from,omitempty"`
		To   *time.Time `json:"to,omitempty"`
	} `json:"period"`
	Inflow struct {
		GrossVolume     float64 `json:"gross_volume"`
		OnlineVolume    float64 `json:"online_volume"`
		OnlineCount     int     `json:"online_count"`
		CashVolume      float64 `json:"cash_volume"`
		CashCount       int     `json:"cash_count"`
		PlatformRevenue float64 `json:"platform_revenue"`
		DriverEarnings  float64 `json:"driver_earnings"`
		UnpaidVolume    float64 `json:"unpaid_volume"`
		UnpaidCount     int     `json:"unpaid_count"`
	} `json:"inflow"`
	Outflow struct {
		PayoutsSettledVolume float64 `json:"payouts_settled_volume"`
		PayoutsSettledCount  int     `json:"payouts_settled_count"`
		PayoutsPendingVolume float64 `json:"payouts_pending_volume"`
		PayoutsPendingCount  int     `json:"payouts_pending_count"`
		PayoutsFailedVolume  float64 `json:"payouts_failed_volume"`
		PayoutsFailedCount   int     `json:"payouts_failed_count"`
		PayoutFeesCollected  float64 `json:"payout_fees_collected"`
	} `json:"outflow"`
	WalletLiability struct {
		TotalDriverFloat    float64 `json:"total_driver_float"`
		NegativeDriverFloat float64 `json:"negative_driver_float"`
		ActiveWalletsCount  int     `json:"active_wallets_count"`
	} `json:"wallet_liability"`
}

// HandleFinanceSummary aggregates gross merchandise value, gateway vs cash split,
// platform margin, driver payouts, and current wallet liabilities.
func (h *AdminFinanceHandler) HandleFinanceSummary(w http.ResponseWriter, r *http.Request) {
	if h.Pool == nil {
		response.Error(w, "Database pool not configured", http.StatusInternalServerError)
		return
	}

	var req struct {
		DateFrom *time.Time `json:"date_from"`
		DateTo   *time.Time `json:"date_to"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	// Default to 30 days if neither is provided
	if req.DateFrom == nil && req.DateTo == nil {
		t := time.Now().AddDate(0, 0, -30)
		req.DateFrom = &t
	}

	ctx := r.Context()
	var resp FinanceSummaryResponse
	resp.Period.From = req.DateFrom
	resp.Period.To = req.DateTo

	// 1. Inflow aggregation from `payments`
	inflowQ := `
		SELECT 
			COALESCE(SUM(charged_amount), 0) AS total_gross,
			COALESCE(SUM(CASE WHEN payment_mode = 'online' AND paid = true THEN charged_amount ELSE 0 END), 0) AS online_vol,
			COALESCE(COUNT(CASE WHEN payment_mode = 'online' AND paid = true THEN 1 END), 0) AS online_cnt,
			COALESCE(SUM(CASE WHEN payment_mode = 'cash' AND paid = true THEN charged_amount ELSE 0 END), 0) AS cash_vol,
			COALESCE(COUNT(CASE WHEN payment_mode = 'cash' AND paid = true THEN 1 END), 0) AS cash_cnt,
			COALESCE(SUM(CASE WHEN paid = true THEN (charged_amount - driver_share) ELSE 0 END), 0) AS platform_rev,
			COALESCE(SUM(CASE WHEN paid = true THEN driver_share ELSE 0 END), 0) AS driver_earn,
			COALESCE(SUM(CASE WHEN paid = false THEN charged_amount ELSE 0 END), 0) AS unpaid_vol,
			COALESCE(COUNT(CASE WHEN paid = false THEN 1 END), 0) AS unpaid_cnt
		FROM payments
		WHERE ($1::timestamptz IS NULL OR created_at >= $1)
		  AND ($2::timestamptz IS NULL OR created_at <= $2)`

	err := h.Pool.QueryRow(ctx, inflowQ, req.DateFrom, req.DateTo).Scan(
		&resp.Inflow.GrossVolume,
		&resp.Inflow.OnlineVolume,
		&resp.Inflow.OnlineCount,
		&resp.Inflow.CashVolume,
		&resp.Inflow.CashCount,
		&resp.Inflow.PlatformRevenue,
		&resp.Inflow.DriverEarnings,
		&resp.Inflow.UnpaidVolume,
		&resp.Inflow.UnpaidCount,
	)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to query payments summary")
		response.Error(w, "Failed to load financial summary", http.StatusInternalServerError)
		return
	}

	// 2. Outflow aggregation from `wallet_transactions`
	outflowQ := `
		SELECT 
			COALESCE(SUM(CASE WHEN txn_type = 'withdrawal' AND status = 'success' THEN amount ELSE 0 END), 0) AS settled_vol,
			COALESCE(COUNT(CASE WHEN txn_type = 'withdrawal' AND status = 'success' THEN 1 END), 0) AS settled_cnt,
			COALESCE(SUM(CASE WHEN txn_type = 'withdrawal' AND status = 'pending' THEN amount ELSE 0 END), 0) AS pending_vol,
			COALESCE(COUNT(CASE WHEN txn_type = 'withdrawal' AND status = 'pending' THEN 1 END), 0) AS pending_cnt,
			COALESCE(SUM(CASE WHEN txn_type = 'withdrawal' AND status = 'failed' THEN amount ELSE 0 END), 0) AS failed_vol,
			COALESCE(COUNT(CASE WHEN txn_type = 'withdrawal' AND status = 'failed' THEN 1 END), 0) AS failed_cnt,
			COALESCE(SUM(CASE WHEN txn_type = 'fee' THEN amount ELSE 0 END), 0) AS fees_vol
		FROM wallet_transactions
		WHERE ($1::timestamptz IS NULL OR created_at >= $1)
		  AND ($2::timestamptz IS NULL OR created_at <= $2)`

	err = h.Pool.QueryRow(ctx, outflowQ, req.DateFrom, req.DateTo).Scan(
		&resp.Outflow.PayoutsSettledVolume,
		&resp.Outflow.PayoutsSettledCount,
		&resp.Outflow.PayoutsPendingVolume,
		&resp.Outflow.PayoutsPendingCount,
		&resp.Outflow.PayoutsFailedVolume,
		&resp.Outflow.PayoutsFailedCount,
		&resp.Outflow.PayoutFeesCollected,
	)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to query wallet transactions summary")
		response.Error(w, "Failed to load outflow summary", http.StatusInternalServerError)
		return
	}

	// 3. Driver wallet liability from `drivers`
	liabilityQ := `
		SELECT 
			COALESCE(SUM(CASE WHEN wallet_balance > 0 THEN wallet_balance ELSE 0 END), 0) AS positive_float,
			COALESCE(SUM(CASE WHEN wallet_balance < 0 THEN wallet_balance ELSE 0 END), 0) AS negative_float,
			COALESCE(COUNT(CASE WHEN wallet_balance <> 0 THEN 1 END), 0) AS active_wallets
		FROM drivers`

	err = h.Pool.QueryRow(ctx, liabilityQ).Scan(
		&resp.WalletLiability.TotalDriverFloat,
		&resp.WalletLiability.NegativeDriverFloat,
		&resp.WalletLiability.ActiveWalletsCount,
	)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to query driver wallet liability")
		response.Error(w, "Failed to load wallet liability", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// EnrichedWalletTransaction represents a ledger item with driver and bank metadata.
type EnrichedWalletTransaction struct {
	ID                  string     `json:"id"`
	IDAlias             string     `json:"_id"`
	DriverID            string     `json:"driver_id"`
	DriverName          string     `json:"driver_name"`
	DriverMobile        string     `json:"driver_mobile"`
	VehicleRegistration string     `json:"vehicle_registration"`
	VehicleType         string     `json:"vehicle_type"`
	Direction           string     `json:"direction"`
	TxnType             string     `json:"txn_type"`
	Amount              float64    `json:"amount"`
	BalanceAfter        *float64   `json:"balance_after,omitempty"`
	Status              string     `json:"status"`
	ReferenceID         string     `json:"reference_id"`
	AccountNo           string     `json:"account_no"`
	BankReferenceNo     string     `json:"bank_reference_no"`
	ZwitchTransferID    string     `json:"zwitch_transfer_id"`
	MerchantReferenceID string     `json:"merchant_reference_id"`
	ErrorMessage        string     `json:"error_message"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           *time.Time `json:"updated_at,omitempty"`
}

// HandleListLedgerTransactions returns paginated, enriched wallet transactions.
func (h *AdminFinanceHandler) HandleListLedgerTransactions(w http.ResponseWriter, r *http.Request) {
	if h.Pool == nil {
		response.Error(w, "Database pool not configured", http.StatusInternalServerError)
		return
	}

	var req struct {
		TxnType   string     `json:"txn_type"`
		Direction string     `json:"direction"`
		Status    string     `json:"status"`
		DriverID  string     `json:"driver_id"`
		Search    string     `json:"search"`
		DateFrom  *time.Time `json:"date_from"`
		DateTo    *time.Time `json:"date_to"`
		Limit     *int       `json:"limit"`
		Offset    *int       `json:"offset"`
		Skip      *int       `json:"skip"`
	}

	if r.Body != nil && r.ContentLength != 0 {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	limit := 50
	if req.Limit != nil && *req.Limit > 0 {
		limit = *req.Limit
		if limit > 50 {
			limit = 50
		}
	}
	offset := 0
	if req.Offset != nil && *req.Offset >= 0 {
		offset = *req.Offset
	} else if req.Skip != nil && *req.Skip >= 0 {
		offset = *req.Skip
	}

	req.TxnType = strings.TrimSpace(req.TxnType)
	req.Direction = strings.TrimSpace(req.Direction)
	req.Status = strings.TrimSpace(req.Status)
	req.DriverID = strings.TrimSpace(req.DriverID)
	req.Search = strings.TrimSpace(req.Search)

	// Normalize "all" strings to empty
	if strings.EqualFold(req.TxnType, "all") {
		req.TxnType = ""
	}
	if strings.EqualFold(req.Direction, "all") {
		req.Direction = ""
	}
	if strings.EqualFold(req.Status, "all") {
		req.Status = ""
	}

	ctx := r.Context()

	// Base count query
	countQ := `
		SELECT COUNT(*)
		FROM wallet_transactions w
		LEFT JOIN drivers d ON d.id::text = w.driver_id
		WHERE ($1 = '' OR w.txn_type = $1)
		  AND ($2 = '' OR w.direction = $2)
		  AND ($3 = '' OR w.status = $3)
		  AND ($4 = '' OR w.driver_id = $4)
		  AND ($5 = '' OR (
		      w.bank_reference_no ILIKE '%' || $5 || '%' 
		      OR w.reference_id ILIKE '%' || $5 || '%'
		      OR w.merchant_reference_id ILIKE '%' || $5 || '%'
		      OR w.account_no ILIKE '%' || $5 || '%'
		      OR d.name ILIKE '%' || $5 || '%'
		      OR d.mobile ILIKE '%' || $5 || '%'
		  ))
		  AND ($6::timestamptz IS NULL OR w.created_at >= $6)
		  AND ($7::timestamptz IS NULL OR w.created_at <= $7)`

	var total int
	err := h.Pool.QueryRow(ctx, countQ, req.TxnType, req.Direction, req.Status, req.DriverID, req.Search, req.DateFrom, req.DateTo).Scan(&total)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to count ledger transactions")
		response.Error(w, "Failed to count transactions", http.StatusInternalServerError)
		return
	}

	listQ := `
		SELECT 
			w.id::text,
			w.driver_id,
			COALESCE(d.name, 'Unknown Driver') AS driver_name,
			COALESCE(d.mobile, '') AS driver_mobile,
			COALESCE(d.vehicle_registration, '') AS vehicle_registration,
			COALESCE(d.vehicle_type, '') AS vehicle_type,
			w.direction,
			w.txn_type,
			w.amount,
			w.balance_after,
			w.status,
			COALESCE(w.reference_id, '') AS reference_id,
			COALESCE(w.account_no, '') AS account_no,
			COALESCE(w.bank_reference_no, '') AS bank_reference_no,
			COALESCE(w.zwitch_transfer_id, '') AS zwitch_transfer_id,
			COALESCE(w.merchant_reference_id, '') AS merchant_reference_id,
			COALESCE(w.error_message, '') AS error_message,
			w.created_at,
			w.updated_at
		FROM wallet_transactions w
		LEFT JOIN drivers d ON d.id::text = w.driver_id
		WHERE ($1 = '' OR w.txn_type = $1)
		  AND ($2 = '' OR w.direction = $2)
		  AND ($3 = '' OR w.status = $3)
		  AND ($4 = '' OR w.driver_id = $4)
		  AND ($5 = '' OR (
		      w.bank_reference_no ILIKE '%' || $5 || '%' 
		      OR w.reference_id ILIKE '%' || $5 || '%'
		      OR w.merchant_reference_id ILIKE '%' || $5 || '%'
		      OR w.account_no ILIKE '%' || $5 || '%'
		      OR d.name ILIKE '%' || $5 || '%'
		      OR d.mobile ILIKE '%' || $5 || '%'
		  ))
		  AND ($6::timestamptz IS NULL OR w.created_at >= $6)
		  AND ($7::timestamptz IS NULL OR w.created_at <= $7)
		ORDER BY w.created_at DESC, w.id DESC
		LIMIT $8 OFFSET $9`

	rows, err := h.Pool.Query(ctx, listQ, req.TxnType, req.Direction, req.Status, req.DriverID, req.Search, req.DateFrom, req.DateTo, limit, offset)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to query ledger transactions")
		response.Error(w, "Failed to query transactions", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := make([]EnrichedWalletTransaction, 0)
	for rows.Next() {
		var item EnrichedWalletTransaction
		err := rows.Scan(
			&item.ID,
			&item.DriverID,
			&item.DriverName,
			&item.DriverMobile,
			&item.VehicleRegistration,
			&item.VehicleType,
			&item.Direction,
			&item.TxnType,
			&item.Amount,
			&item.BalanceAfter,
			&item.Status,
			&item.ReferenceID,
			&item.AccountNo,
			&item.BankReferenceNo,
			&item.ZwitchTransferID,
			&item.MerchantReferenceID,
			&item.ErrorMessage,
			&item.CreatedAt,
			&item.UpdatedAt,
		)
		if err != nil {
			logger.Log.Error().Err(err).Msg("Failed to scan ledger transaction")
			response.Error(w, "Failed to scan transactions", http.StatusInternalServerError)
			return
		}
		item.IDAlias = item.ID
		items = append(items, item)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"total":  total,
		"limit":  limit,
		"offset": offset,
		"data":   items,
	})
}

// EnrichedPayment represents a ride payment item with passenger, driver and route details.
type EnrichedPayment struct {
	ID                 string     `json:"id"`
	IDAlias            string     `json:"_id"`
	UserID             string     `json:"user_id"`
	UserName           string     `json:"user_name"`
	UserMobile         string     `json:"user_mobile"`
	PartnerID          string     `json:"partner_id"`
	DriverName         string     `json:"driver_name"`
	DriverMobile       string     `json:"driver_mobile"`
	RideID             string     `json:"ride_id"`
	RideStatus         string     `json:"ride_status"`
	PickupAddress      string     `json:"pickup_address"`
	DropAddress        string     `json:"drop_address"`
	Description        string     `json:"description"`
	OriginalAmount     float64    `json:"original_amount"`
	ChargedAmount      float64    `json:"charged_amount"`
	DriverShare        float64    `json:"driver_share"`
	PlatformCommission float64    `json:"platform_commission"`
	PaymentMode        string     `json:"payment_mode"`
	Paid               bool       `json:"paid"`
	RazorpayOrderID    *string    `json:"razorpay_order_id,omitempty"`
	RazorpayPaymentID  *string    `json:"razorpay_payment_id,omitempty"`
	PaidAt             *time.Time `json:"paid_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	Offer              *string    `json:"offer,omitempty"`
}

// HandleListAdminPayments returns paginated, enriched ride payments.
func (h *AdminFinanceHandler) HandleListAdminPayments(w http.ResponseWriter, r *http.Request) {
	if h.Pool == nil {
		response.Error(w, "Database pool not configured", http.StatusInternalServerError)
		return
	}

	var req struct {
		PaymentMode string     `json:"payment_mode"`
		Paid        *bool      `json:"paid"`
		Search      string     `json:"search"`
		DateFrom    *time.Time `json:"date_from"`
		DateTo      *time.Time `json:"date_to"`
		Limit       *int       `json:"limit"`
		Offset      *int       `json:"offset"`
		Skip        *int       `json:"skip"`
	}

	if r.Body != nil && r.ContentLength != 0 {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	limit := 50
	if req.Limit != nil && *req.Limit > 0 {
		limit = *req.Limit
		if limit > 50 {
			limit = 50
		}
	}
	offset := 0
	if req.Offset != nil && *req.Offset >= 0 {
		offset = *req.Offset
	} else if req.Skip != nil && *req.Skip >= 0 {
		offset = *req.Skip
	}

	req.PaymentMode = strings.TrimSpace(req.PaymentMode)
	req.Search = strings.TrimSpace(req.Search)

	if strings.EqualFold(req.PaymentMode, "all") {
		req.PaymentMode = ""
	}

	ctx := r.Context()

	countQ := `
		SELECT COUNT(*)
		FROM payments p
		LEFT JOIN users u ON u.id::text = p.user_id
		LEFT JOIN drivers d ON d.id::text = p.partner_id
		LEFT JOIN rides r ON r.id::text = p.ride_id
		WHERE ($1 = '' OR p.payment_mode = $1)
		  AND ($2::boolean IS NULL OR p.paid = $2)
		  AND ($3 = '' OR (
		      p.razorpay_payment_id ILIKE '%' || $3 || '%'
		      OR p.razorpay_order_id ILIKE '%' || $3 || '%'
		      OR p.ride_id ILIKE '%' || $3 || '%'
		      OR u.name ILIKE '%' || $3 || '%'
		      OR u.mobile ILIKE '%' || $3 || '%'
		      OR d.name ILIKE '%' || $3 || '%'
		      OR d.mobile ILIKE '%' || $3 || '%'
		  ))
		  AND ($4::timestamptz IS NULL OR p.created_at >= $4)
		  AND ($5::timestamptz IS NULL OR p.created_at <= $5)`

	var total int
	err := h.Pool.QueryRow(ctx, countQ, req.PaymentMode, req.Paid, req.Search, req.DateFrom, req.DateTo).Scan(&total)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to count payments")
		response.Error(w, "Failed to count payments", http.StatusInternalServerError)
		return
	}

	listQ := `
		SELECT 
			p.id::text,
			p.user_id,
			COALESCE(u.name, 'Passenger') AS user_name,
			COALESCE(u.mobile, '') AS user_mobile,
			p.partner_id,
			COALESCE(d.name, 'Driver') AS driver_name,
			COALESCE(d.mobile, '') AS driver_mobile,
			p.ride_id,
			COALESCE(r.status, 'UNKNOWN') AS ride_status,
			COALESCE(r.pickup_address, '') AS pickup_address,
			COALESCE(r.drop_address, '') AS drop_address,
			COALESCE(p.description, '') AS description,
			p.original_amount,
			p.charged_amount,
			p.driver_share,
			ROUND(p.charged_amount - p.driver_share, 2) AS platform_commission,
			p.payment_mode,
			p.paid,
			p.razorpay_order_id,
			p.razorpay_payment_id,
			p.paid_at,
			p.created_at,
			p.offer::text
		FROM payments p
		LEFT JOIN users u ON u.id::text = p.user_id
		LEFT JOIN drivers d ON d.id::text = p.partner_id
		LEFT JOIN rides r ON r.id::text = p.ride_id
		WHERE ($1 = '' OR p.payment_mode = $1)
		  AND ($2::boolean IS NULL OR p.paid = $2)
		  AND ($3 = '' OR (
		      p.razorpay_payment_id ILIKE '%' || $3 || '%'
		      OR p.razorpay_order_id ILIKE '%' || $3 || '%'
		      OR p.ride_id ILIKE '%' || $3 || '%'
		      OR u.name ILIKE '%' || $3 || '%'
		      OR u.mobile ILIKE '%' || $3 || '%'
		      OR d.name ILIKE '%' || $3 || '%'
		      OR d.mobile ILIKE '%' || $3 || '%'
		  ))
		  AND ($4::timestamptz IS NULL OR p.created_at >= $4)
		  AND ($5::timestamptz IS NULL OR p.created_at <= $5)
		ORDER BY p.created_at DESC, p.id DESC
		LIMIT $6 OFFSET $7`

	rows, err := h.Pool.Query(ctx, listQ, req.PaymentMode, req.Paid, req.Search, req.DateFrom, req.DateTo, limit, offset)
	if err != nil {
		logger.Log.Error().Err(err).Msg("Failed to query payments")
		response.Error(w, "Failed to query payments", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := make([]EnrichedPayment, 0)
	for rows.Next() {
		var item EnrichedPayment
		var offerStr *string
		err := rows.Scan(
			&item.ID,
			&item.UserID,
			&item.UserName,
			&item.UserMobile,
			&item.PartnerID,
			&item.DriverName,
			&item.DriverMobile,
			&item.RideID,
			&item.RideStatus,
			&item.PickupAddress,
			&item.DropAddress,
			&item.Description,
			&item.OriginalAmount,
			&item.ChargedAmount,
			&item.DriverShare,
			&item.PlatformCommission,
			&item.PaymentMode,
			&item.Paid,
			&item.RazorpayOrderID,
			&item.RazorpayPaymentID,
			&item.PaidAt,
			&item.CreatedAt,
			&offerStr,
		)
		if err != nil {
			logger.Log.Error().Err(err).Msg("Failed to scan payment")
			response.Error(w, "Failed to scan payments", http.StatusInternalServerError)
			return
		}
		item.IDAlias = item.ID
		item.Offer = offerStr
		items = append(items, item)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"total":  total,
		"limit":  limit,
		"offset": offset,
		"data":   items,
	})
}
