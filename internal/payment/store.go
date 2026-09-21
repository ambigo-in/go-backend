package payment

import (
	"context"
	"errors"
	"time"

	"ambigo-backend/internal/ids"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTX is the minimal DB interface that is satisfied by *pgxpool.Pool and pgx.Tx.
// Store methods depend only on this interface so they can be used inside a
// pgx.Tx as well as with the pool directly. See WithTx helpers.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct {
	pool *pgxpool.Pool
	db   DBTX
}

// ErrAlreadyPaid is returned by MarkPaymentPaid when the payment was already
// settled by a concurrent confirm (app callback vs webhook). Callers must
// treat it as idempotent success: return "already processed" WITHOUT
// crediting the wallet again.
var ErrAlreadyPaid = errors.New("payment already processed")

// NewStore creates a Store backed by a pgxpool.Pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, db: pool}
}

// NewStoreWithDB creates a Store from any DBTX (pool or Tx). Useful for tests.
func NewStoreWithDB(db DBTX) *Store {
	return &Store{db: db}
}

// WithTx returns a new Store that will execute queries on the given Tx.
// Use together with the exported WithTx helper to wrap multiple store calls
// (e.g. payment + wallet) in a single transaction per
// docs/migration/03-tech-choices-evaluation.md section 6.
func (s *Store) WithTx(tx pgx.Tx) *Store {
	return &Store{pool: s.pool, db: tx}
}

// Pool returns the underlying pool (may be nil if Store was created with NewStoreWithDB).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// WithTx runs fn inside a transaction. The fn receives the transaction handle
// so callers can build Tx-bound stores via Store.WithTx(tx) /
// WalletStore.WithTx(tx). The transaction is rolled back if fn returns an
// error or panics, otherwise it is committed. This mirrors the pattern in
// 03-evaluation.md section 6 (pgx.Tx + Queries.WithTx).
//
// Example:
//
//	err := payment.WithTx(ctx, pool, func(tx pgx.Tx) error {
//	    sTx := store.WithTx(tx)
//	    wTx := walletStore.WithTx(tx)
//	    if err := sTx.MarkPaymentPaid(ctx, payID, rzpID, payment.ModeOnline); err != nil {
//	        return err
//	    }
//	    return wTx.UpdateWalletBalance(ctx, driverID, amount)
//	})
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CreatePayment inserts a new payment record. If payment.ID is empty a new
// UUID is generated via ids.New(). CreatedAt is set if zero.
func (s *Store) CreatePayment(ctx context.Context, p *Payment) error {
	if p.ID == "" {
		p.ID = ids.New()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(ctx,
		`INSERT INTO payments (id, user_id, partner_id, ride_id, description, original_amount, charged_amount, driver_share, payment_mode, paid, razorpay_order_id, razorpay_payment_id, paid_at, created_at, offer)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		p.ID, p.UserID, p.PartnerID, p.RideID, p.Description, p.OriginalAmount, p.ChargedAmount, p.DriverShare, p.PaymentMode, p.Paid, p.RazorpayOrderID, p.RazorpayPaymentID, p.PaidAt, p.CreatedAt, p.Offer,
	)
	return err
}

func scanPayment(row pgx.Row) (*Payment, error) {
	var p Payment
	err := row.Scan(
		&p.ID,
		&p.UserID,
		&p.PartnerID,
		&p.RideID,
		&p.Description,
		&p.OriginalAmount,
		&p.ChargedAmount,
		&p.DriverShare,
		&p.PaymentMode,
		&p.Paid,
		&p.RazorpayOrderID,
		&p.RazorpayPaymentID,
		&p.PaidAt,
		&p.CreatedAt,
		&p.Offer,
	)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

const paymentSelect = `SELECT id::text, user_id, partner_id, ride_id, description, original_amount, charged_amount, driver_share, payment_mode, paid, razorpay_order_id, razorpay_payment_id, paid_at, created_at, offer FROM payments`

// FindPendingPaymentByUserID looks for an unpaid payment belonging to a user (paid=false).
func (s *Store) FindPendingPaymentByUserID(ctx context.Context, userID string) (*Payment, error) {
	row := s.db.QueryRow(ctx, paymentSelect+` WHERE user_id=$1 AND paid=false LIMIT 1`, userID)
	p, err := scanPayment(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// FindPendingPaymentByPartnerID looks for an unpaid payment belonging to a driver (partner).
func (s *Store) FindPendingPaymentByPartnerID(ctx context.Context, partnerID string) (*Payment, error) {
	row := s.db.QueryRow(ctx, paymentSelect+` WHERE partner_id=$1 AND paid=false LIMIT 1`, partnerID)
	p, err := scanPayment(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// MarkPaymentPaid marks a payment as complete with its Razorpay transaction ID.
// It mirrors the Mongo `$set` + `$currentDate` update. The Postgres equivalent is:
// `UPDATE payments SET paid=true, razorpay_payment_id=$2, payment_mode=$3, paid_at=now() WHERE id=$1 AND paid=false RETURNING *`
// The `AND paid=false` guard makes concurrent confirms idempotent: exactly one
// wins, the loser gets ErrAlreadyPaid and must treat it as success-without-credit.
// Callers must NOT pre-check pmt.Paid outside the Tx (TOCTOU) — rely on this.
func (s *Store) MarkPaymentPaid(ctx context.Context, id string, razorpayPaymentID string, paymentMode PaymentMode) error {
	// Single-statement atomic update; row lock is implicit in UPDATE.
	tag, err := s.db.Exec(ctx,
		`UPDATE payments SET paid=true, razorpay_payment_id=$2, payment_mode=$3, paid_at=now() WHERE id=$1 AND paid=false`,
		id, razorpayPaymentID, paymentMode,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Either the payment does not exist or it was already paid.
		// Distinguish so callers can return idempotent success.
		var exists bool
		if qerr := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payments WHERE id=$1)`, id).Scan(&exists); qerr != nil {
			return qerr
		}
		if exists {
			return ErrAlreadyPaid
		}
		return errors.New("payment not found")
	}
	return nil
}

// FindPaymentByID retrieves a payment by its UUID string ID.
// Returns (nil, nil) if not found (pgx.ErrNoRows handling).
func (s *Store) FindPaymentByID(ctx context.Context, id string) (*Payment, error) {
	row := s.db.QueryRow(ctx, paymentSelect+` WHERE id=$1::uuid`, id)
	p, err := scanPayment(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// FindPaymentByRazorpayOrderID retrieves a payment by its Razorpay order ID (unique).
func (s *Store) FindPaymentByRazorpayOrderID(ctx context.Context, orderID string) (*Payment, error) {
	row := s.db.QueryRow(ctx, paymentSelect+` WHERE razorpay_order_id=$1`, orderID)
	p, err := scanPayment(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// FindPaymentByRideID retrieves a payment using its ride_id.
func (s *Store) FindPaymentByRideID(ctx context.Context, rideID string) (*Payment, error) {
	row := s.db.QueryRow(ctx, paymentSelect+` WHERE ride_id=$1`, rideID)
	p, err := scanPayment(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}
