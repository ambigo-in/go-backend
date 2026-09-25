package payment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"ambigo-backend/internal/ids"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WalletTransaction mirrors the Records.wallet collection, now stored in
// wallet_transactions table per migrations/00001_init.sql.
// Ledger discipline: EVERY balance mutation writes a row. direction is
// 'credit' | 'debit'; txn_type is withdrawal | ride_credit |
// commission_debit | referral_credit | refund | admin_adjust | fee.
// reference_id ties the row to its cause (payment_id / ride_id / merchant ref).
// balance_after snapshots drivers.wallet_balance right after the mutation so
// any balance can be rebuilt and audited from the journal.
type WalletTransaction struct {
	ID                  string     `db:"id" json:"_id"`
	DriverID            string     `db:"driver_id" json:"driver_id"`
	ZwitchBeneficiaryID string     `db:"zwitch_beneficiary_id" json:"zwitch_beneficiary_id"`
	ZwitchID            string     `db:"zwitch_id" json:"zwitch_id"`
	Amount              float64    `db:"amount" json:"amount"`
	AccountNo           string     `db:"account_no" json:"account_no"`
	MerchantReferenceID string     `db:"merchant_reference_id" json:"merchant_reference_id"`
	BankReferenceNo     string     `db:"bank_reference_no" json:"bank_reference_no"`
	ZwitchTransferID    string     `db:"zwitch_transfer_id" json:"zwitch_transfer_id"`
	Status              string     `db:"status" json:"status"`
	ErrorMessage        string     `db:"error_message" json:"error_message"`
	Direction           string     `db:"direction" json:"direction"`
	TxnType             string     `db:"txn_type" json:"txn_type"`
	ReferenceID         string     `db:"reference_id" json:"reference_id"`
	BalanceAfter        *float64   `db:"balance_after" json:"balance_after,omitempty"`
	CreatedAt           time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt           *time.Time `db:"updated_at" json:"updated_at,omitempty"`
}

// ErrNoTransaction is returned when a status update matches no row.
var ErrNoTransaction = errors.New("wallet transaction not found")

// WalletStore handles wallet_transactions and drivers.wallet_balance /
// drivers.wallet_details. In Postgres both tables live in the same DB
// (single pool), but the store is usable inside a pgx.Tx via DBTX.
type WalletStore struct {
	pool *pgxpool.Pool
	db   DBTX
}

// NewWalletStore creates a WalletStore backed by a pgxpool.Pool.
// The original Mongo version required two databases (Records + Users); the
// Postgres version uses a single pool since both tables are in the public
// schema (wallet_transactions + drivers).
func NewWalletStore(pool *pgxpool.Pool) *WalletStore {
	return &WalletStore{pool: pool, db: pool}
}

// NewWalletStoreWithDB creates a WalletStore from any DBTX (pool or Tx).
func NewWalletStoreWithDB(db DBTX) *WalletStore {
	return &WalletStore{db: db}
}

// WithTx returns a new WalletStore bound to the given transaction.
func (s *WalletStore) WithTx(tx pgx.Tx) *WalletStore {
	return &WalletStore{pool: s.pool, db: tx}
}

// Pool returns the underlying pool (may be nil if created via NewWalletStoreWithDB).
func (s *WalletStore) Pool() *pgxpool.Pool { return s.pool }

const walletSelect = `SELECT id::text, driver_id, zwitch_beneficiary_id, zwitch_id, amount, account_no, merchant_reference_id, bank_reference_no, zwitch_transfer_id, status, error_message, direction, txn_type, reference_id, balance_after, created_at, updated_at FROM wallet_transactions`

func scanWalletTransaction(row pgx.Row) (*WalletTransaction, error) {
	var w WalletTransaction
	err := row.Scan(
		&w.ID,
		&w.DriverID,
		&w.ZwitchBeneficiaryID,
		&w.ZwitchID,
		&w.Amount,
		&w.AccountNo,
		&w.MerchantReferenceID,
		&w.BankReferenceNo,
		&w.ZwitchTransferID,
		&w.Status,
		&w.ErrorMessage,
		&w.Direction,
		&w.TxnType,
		&w.ReferenceID,
		&w.BalanceAfter,
		&w.CreatedAt,
		&w.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// scanWalletRows scans a wallet_transactions result row into w.
// Shared by both list queries so the column list cannot drift.
func scanWalletRows(rows pgx.Rows, w *WalletTransaction) error {
	return rows.Scan(
		&w.ID,
		&w.DriverID,
		&w.ZwitchBeneficiaryID,
		&w.ZwitchID,
		&w.Amount,
		&w.AccountNo,
		&w.MerchantReferenceID,
		&w.BankReferenceNo,
		&w.ZwitchTransferID,
		&w.Status,
		&w.ErrorMessage,
		&w.Direction,
		&w.TxnType,
		&w.ReferenceID,
		&w.BalanceAfter,
		&w.CreatedAt,
		&w.UpdatedAt,
	)
}

// InsertTransaction inserts a new wallet transaction. ID and CreatedAt are
// generated if empty/zero via ids.New() / time.Now().
// Direction defaults to 'debit' and TxnType to 'withdrawal' for the legacy
// withdrawal call-site; every other caller must set them explicitly.
func (s *WalletStore) InsertTransaction(ctx context.Context, tx *WalletTransaction) error {
	if tx.ID == "" {
		tx.ID = ids.New()
	}
	if tx.CreatedAt.IsZero() {
		tx.CreatedAt = time.Now()
	}
	if tx.Direction == "" {
		tx.Direction = "debit"
	}
	if tx.TxnType == "" {
		tx.TxnType = "withdrawal"
	}
	_, err := s.db.Exec(ctx,
		`INSERT INTO wallet_transactions (id, driver_id, zwitch_beneficiary_id, zwitch_id, amount, account_no, merchant_reference_id, bank_reference_no, zwitch_transfer_id, status, error_message, direction, txn_type, reference_id, balance_after, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		tx.ID, tx.DriverID, tx.ZwitchBeneficiaryID, tx.ZwitchID, tx.Amount, tx.AccountNo, tx.MerchantReferenceID, tx.BankReferenceNo, tx.ZwitchTransferID, tx.Status, tx.ErrorMessage, tx.Direction, tx.TxnType, tx.ReferenceID, tx.BalanceAfter, tx.CreatedAt, tx.UpdatedAt,
	)
	return err
}

// InsertLedgerEntry records a non-withdrawal balance mutation (ride credit,
// commission debit, referral credit, refund, admin adjust, fee) in the same
// transaction as the balance update. Status is 'success' — these are internal
// postings, never pending external settlement.
// A unique merchant_reference_id is minted when empty: the column carries a
// UNIQUE constraint, so blank refs would collide globally after the first row.
func (s *WalletStore) InsertLedgerEntry(ctx context.Context, e *WalletTransaction) error {
	if e.TxnType == "" || e.TxnType == "withdrawal" {
		return errors.New("InsertLedgerEntry requires a non-withdrawal txn_type")
	}
	if e.Direction == "" {
		return errors.New("InsertLedgerEntry requires a direction")
	}
	if e.MerchantReferenceID == "" {
		e.MerchantReferenceID = "L" + strings.ReplaceAll(ids.New(), "-", "")
	}
	if e.Status == "" {
		e.Status = "success"
	}
	return s.InsertTransaction(ctx, e)
}

// FindByTransferID fetches a withdrawal row by the provider transfer id.
// Used by the transfers.updated webhook, which carries the Zwitch id.
func (s *WalletStore) FindByTransferID(ctx context.Context, transferID string) (*WalletTransaction, error) {
	if transferID == "" {
		return nil, nil
	}
	row := s.db.QueryRow(ctx, walletSelect+` WHERE zwitch_transfer_id=$1`, transferID)
	w, err := scanWalletTransaction(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return w, nil
}

// SetWalletVerifyRef stores the merchant reference sent with a verification
// request so the async verification webhook can be correlated to the driver.
func (s *WalletStore) SetWalletVerifyRef(ctx context.Context, driverID string, ref string) error {
	tag, err := s.db.Exec(ctx, `UPDATE drivers SET wallet_verify_ref=$2 WHERE id=$1`, driverID, ref)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("driver not found")
	}
	return nil
}

// FindDriverByVerifyRef resolves a verification webhook to its driver id.
func (s *WalletStore) FindDriverByVerifyRef(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	var id string
	err := s.db.QueryRow(ctx, `SELECT id::text FROM drivers WHERE wallet_verify_ref=$1`, ref).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return id, nil
}
// Used to resolve the stored outcome when a settlement loses a status race.
func (s *WalletStore) FindByMerchantRef(ctx context.Context, driverID string, merchantReferenceID string) (*WalletTransaction, error) {
	row := s.db.QueryRow(ctx, walletSelect+` WHERE driver_id=$1 AND merchant_reference_id=$2`, driverID, merchantReferenceID)
	w, err := scanWalletTransaction(row)
	if err != nil {
		return nil, err
	}
	return w, nil
}

// FindByMerchantRefAny fetches a withdrawal row by merchant ref across drivers.
// Webhook scope: the provider echoes our reference; the composite unique keeps
// it effectively per-driver, but the webhook path resolves identity from the row.
func (s *WalletStore) FindByMerchantRefAny(ctx context.Context, merchantReferenceID string) (*WalletTransaction, error) {
	if merchantReferenceID == "" {
		return nil, nil
	}
	row := s.db.QueryRow(ctx, walletSelect+` WHERE merchant_reference_id=$1 ORDER BY created_at DESC LIMIT 1`, merchantReferenceID)
	w, err := scanWalletTransaction(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return w, nil
}

// FindByReference returns every journal row sharing one reference_id
// (withdrawal + fee + any refunds), used to compute the true gross and to
// detect an already-posted refund before compensating again.
func (s *WalletStore) FindByReference(ctx context.Context, driverID string, referenceID string) ([]WalletTransaction, error) {
	rows, err := s.db.Query(ctx, walletSelect+` WHERE driver_id=$1 AND reference_id=$2 ORDER BY created_at ASC`, driverID, referenceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WalletTransaction
	for rows.Next() {
		var w WalletTransaction
		if err := scanWalletRows(rows, &w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// FindRecentPending finds a pending withdrawal of the same amount created
// within the window. Anti-double-tap: old apps send no idempotency key, so a
// fast second tap would otherwise mint a second withdrawal.
func (s *WalletStore) FindRecentPending(ctx context.Context, driverID string, amount float64, within time.Duration) (*WalletTransaction, error) {
	row := s.db.QueryRow(ctx, walletSelect+` WHERE driver_id=$1 AND status='pending' AND txn_type='withdrawal' AND amount=$2 AND created_at > now() - ($3::int * interval '1 second') ORDER BY created_at DESC LIMIT 1`, driverID, amount, int(within.Seconds()))
	w, err := scanWalletTransaction(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return w, nil
}
// The materialized column is a read cache; the journal (wallet_transactions)
// is the source of truth — see ReconcileBalance.
func (s *WalletStore) GetBalance(ctx context.Context, driverID string) (float64, error) {
	var bal float64
	err := s.db.QueryRow(ctx, `SELECT wallet_balance FROM drivers WHERE id=$1`, driverID).Scan(&bal)
	return bal, err
}

// ListTransactions returns the most recent 50 wallet transactions for a driver, ordered by
// created_at descending. Use ListTransactionsPaginated for keyset pagination.
func (s *WalletStore) ListTransactions(ctx context.Context, driverID string) ([]WalletTransaction, error) {
	return s.ListTransactionsPaginated(ctx, driverID, 50, "")
}

// ListTransactionsPaginated returns wallet transactions with keyset pagination (capped at 50).
func (s *WalletStore) ListTransactionsPaginated(ctx context.Context, driverID string, limit int, cursor string) ([]WalletTransaction, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	if cursor != "" && ids.IsValid(cursor) {
		// cursor is an id; keyset on (created_at, id)
		rows, err = s.db.Query(ctx, walletSelect+` WHERE driver_id=$1 AND (created_at, id) < ((SELECT created_at FROM wallet_transactions WHERE id=$3::uuid), $3::uuid) ORDER BY created_at DESC, id DESC LIMIT $2`, driverID, limit, cursor)
	} else {
		rows, err = s.db.Query(ctx, walletSelect+` WHERE driver_id=$1 ORDER BY created_at DESC, id DESC LIMIT $2`, driverID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []WalletTransaction
	for rows.Next() {
		var w WalletTransaction
		if err := scanWalletRows(rows, &w); err != nil {
			return nil, err
		}
		list = append(list, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []WalletTransaction{}
	}
	return list, nil
}

func (s *WalletStore) ListTransactionsWithOffset(ctx context.Context, driverID string, limit int, offset int) ([]WalletTransaction, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.Query(ctx, walletSelect+` WHERE driver_id=$1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`, driverID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []WalletTransaction
	for rows.Next() {
		var w WalletTransaction
		if err := scanWalletRows(rows, &w); err != nil {
			return nil, err
		}
		list = append(list, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []WalletTransaction{}
	}
	return list, nil
}

// CleanupOldEvents deletes processed webhook events older than the TTL.
// Idempotency only needs recent keys (provider retries stop after 24h);
// 30 days retains a full dispute window without unbounded growth.
func (s *WalletStore) CleanupOldEvents(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM processed_events WHERE received_at < now() - interval '30 days'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// StuckWithdrawal is a pending withdrawal row plus its driver, for the sweeper.
type StuckWithdrawal struct {
	DriverID            string
	MerchantReferenceID string
	ZwitchTransferID    string
	Amount              float64
	CreatedAt           time.Time
}

// ListStuckPending returns pending withdrawals older than the given age.
// Index-backed scan; the sweeper resolves each via the provider instead of
// leaving money deducted-but-unsent forever.
func (s *WalletStore) ListStuckPending(ctx context.Context, olderThan time.Duration, limit int) ([]StuckWithdrawal, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.Query(ctx,
		`SELECT driver_id, merchant_reference_id, zwitch_transfer_id, amount, created_at FROM wallet_transactions WHERE status='pending' AND txn_type='withdrawal' AND created_at < now() - ($1::int * interval '1 second') ORDER BY created_at ASC LIMIT $2`,
		int(olderThan.Seconds()), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StuckWithdrawal
	for rows.Next() {
		var sw StuckWithdrawal
		if err := rows.Scan(&sw.DriverID, &sw.MerchantReferenceID, &sw.ZwitchTransferID, &sw.Amount, &sw.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, sw)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
// A negative amount indicates a debit. Postgres equivalent of Mongo
// `$inc: {wallet_balance: amount}` is:
// `UPDATE drivers SET wallet_balance = wallet_balance + $2 WHERE id=$1`
func (s *WalletStore) UpdateWalletBalance(ctx context.Context, driverID string, amount float64) error {
	_, err := s.db.Exec(ctx,
		`UPDATE drivers SET wallet_balance = wallet_balance + $2 WHERE id=$1`,
		driverID, amount,
	)
	return err
}

// DeductBalance atomically deducts amount only if sufficient balance exists.
// Prevents concurrent withdrawals from driving balance negative.
// Postgres: `UPDATE drivers SET wallet_balance = wallet_balance - $2 WHERE id=$1 AND wallet_balance >= $2`
// Check RowsAffected==0 -> insufficient wallet balance. This is the atomic
// CAS guard that replaces Mongo's `{"wallet_balance": {"$gte": amount}}` + `$inc`.
func (s *WalletStore) DeductBalance(ctx context.Context, driverID string, amount float64) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE drivers SET wallet_balance = wallet_balance - $2 WHERE id=$1 AND wallet_balance >= $2`,
		driverID, amount,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("insufficient wallet balance")
	}
	return nil
}

// AdjustWalletBalance atomically applies a signed delta and returns the new
// balance in one statement (UPDATE ... RETURNING). Use for admin adjustments
// and any path that must never silently no-op: unknown drivers error instead
// of vanishing into a zero-row UPDATE.
func (s *WalletStore) AdjustWalletBalance(ctx context.Context, driverID string, delta float64) (float64, error) {
	var bal float64
	err := s.db.QueryRow(ctx,
		`UPDATE drivers SET wallet_balance = wallet_balance + $2 WHERE id=$1 RETURNING wallet_balance`,
		driverID, delta,
	).Scan(&bal)
	if err != nil {
		return 0, err
	}
	return bal, nil
}

// SetWalletVerified flips the first-payout gate after bank verification.
func (s *WalletStore) SetWalletVerified(ctx context.Context, driverID string, verified bool) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE drivers SET wallet_verified=$2, wallet_verified_at=CASE WHEN $2 THEN now() ELSE NULL END WHERE id=$1`,
		driverID, verified,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("driver not found")
	}
	return nil
}

// UpdateWalletDetails saves the driver's bank / Zwitch details into
// drivers.wallet_details (JSONB). The original Mongo used `$set: {wallet_details: details}`.
// Postgres: `UPDATE drivers SET wallet_details=$2 WHERE id=$1`.
// details is marshalled to JSON; WalletDetails struct works directly.
func (s *WalletStore) UpdateWalletDetails(ctx context.Context, driverID string, details interface{}) error {
	data, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx,
		`UPDATE drivers SET wallet_details=$2::jsonb, updated_at=now() WHERE id=$1::uuid`,
		driverID, data,
	)
	return err
}

// UpdateTransactionStatus updates the status and auxiliary fields of a wallet
// transaction identified by (merchant_reference_id, driver_id). The
// `AND status='pending'` guard makes settlement exactly-once across racing
// settlers (request thread vs sweeper vs multi-pod): exactly one wins, losers
// get ErrNoTransaction and must resolve the stored outcome instead of
// re-applying money movement. Returns ErrNoTransaction when nothing matched.
func (s *WalletStore) UpdateTransactionStatus(ctx context.Context, driverID string, merchantReferenceID string, status string, bankReferenceNo string, zwitchTransferID string, errorMessage string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE wallet_transactions SET status=$3, bank_reference_no=$4, zwitch_transfer_id=$5, error_message=$6, updated_at=now() WHERE merchant_reference_id=$2 AND driver_id=$1 AND status='pending'`,
		driverID, merchantReferenceID, status, bankReferenceNo, zwitchTransferID, errorMessage,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoTransaction
	}
	return nil
}
