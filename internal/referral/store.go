package referral

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"ambigo-backend/internal/ids"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTX is the minimal DB interface satisfied by *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store handles PostgreSQL operations for referral configs and records.
// Tables: referral_configs (type PK) and referral_records (UUID PK)
// per migrations/00001_init.sql.
type Store struct {
	pool *pgxpool.Pool
	db   DBTX
}

// NewStore creates a new referral Store backed by a pgxpool.Pool.
// Single pool covers both referral_configs and referral_records tables
// (originally Data.referral_config + Records.referral_records).
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, db: pool}
}

// NewStoreWithDB creates a Store from any DBTX (pool or Tx). Useful for tests.
func NewStoreWithDB(db DBTX) *Store {
	return &Store{db: db}
}

// NewStoreWithPool is an alias for NewStoreWithDB when a pool is available.
func NewStoreWithPool(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, db: pool}
}

// WithTx returns a new Store bound to the given transaction.
func (s *Store) WithTx(tx pgx.Tx) *Store {
	return &Store{pool: s.pool, db: tx}
}

// Pool returns the underlying pool for cross-store transactions
// (wallet credit + credit-flag flip must commit atomically).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// WithTx runs fn inside a transaction. Mirrors payment.WithTx / ride.WithTx pattern.
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

const configSelect = `SELECT type, referrer_amount, new_user_amount, rides_required, enabled FROM referral_configs`
const recordSelect = `SELECT id::text, type, referrer_id, referrer_role, referee_id, referee_role, code, rides_required, rides_done, referrer_credited, referee_credited, referrer_amount, referee_amount, created_at, completed_at FROM referral_records`

func scanConfig(row pgx.Row) (*Config, error) {
	var c Config
	// referral_configs has no id column — ID stays empty (type is PK).
	// Keep ID for json compatibility but don't scan from DB.
	err := row.Scan(&c.Type, &c.ReferrerAmount, &c.NewUserAmount, &c.RidesRequired, &c.Enabled)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func scanRecord(row pgx.Row) (*Record, error) {
	var r Record
	var completedAt sql.NullTime
	err := row.Scan(
		&r.ID,
		&r.Type,
		&r.ReferrerID,
		&r.ReferrerRole,
		&r.RefereeID,
		&r.RefereeRole,
		&r.Code,
		&r.RidesRequired,
		&r.RidesDone,
		&r.ReferrerCredited,
		&r.RefereeCredited,
		&r.ReferrerAmount,
		&r.RefereeAmount,
		&r.CreatedAt,
		&completedAt,
	)
	if err != nil {
		return nil, err
	}
	if completedAt.Valid {
		t := completedAt.Time
		r.CompletedAt = &t
	}
	return &r, nil
}

// ---- Config CRUD ----

// ListConfigs returns all referral type configurations.
// Postgres: SELECT * FROM referral_configs (explicit columns for scan safety)
func (s *Store) ListConfigs(ctx context.Context) ([]Config, error) {
	rows, err := s.db.Query(ctx, configSelect)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Config
	for rows.Next() {
		var c Config
		if err := rows.Scan(&c.Type, &c.ReferrerAmount, &c.NewUserAmount, &c.RidesRequired, &c.Enabled); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Config{}
	}
	return list, nil
}

// SaveConfigs upserts all configs by their "type" field.
// Replaces the full config set each time the admin saves.
// Postgres: INSERT INTO referral_configs(type,referrer_amount,new_user_amount,rides_required,enabled) VALUES... ON CONFLICT (type) DO UPDATE SET ...
func (s *Store) SaveConfigs(ctx context.Context, configs []Config) error {
	for _, cfg := range configs {
		_, err := s.db.Exec(ctx,
			`INSERT INTO referral_configs (type, referrer_amount, new_user_amount, rides_required, enabled, updated_at) VALUES ($1,$2,$3,$4,$5, now()) ON CONFLICT (type) DO UPDATE SET referrer_amount=EXCLUDED.referrer_amount, new_user_amount=EXCLUDED.new_user_amount, rides_required=EXCLUDED.rides_required, enabled=EXCLUDED.enabled, updated_at=now()`,
			cfg.Type, cfg.ReferrerAmount, cfg.NewUserAmount, cfg.RidesRequired, cfg.Enabled,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// GetConfigByType returns the config for a specific referral type (e.g., "user_to_driver").
// Postgres: SELECT * FROM referral_configs WHERE type=$1
func (s *Store) GetConfigByType(ctx context.Context, refType string) (*Config, error) {
	row := s.db.QueryRow(ctx, configSelect+` WHERE type=$1`, refType)
	c, err := scanConfig(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return c, nil
}

// ---- Record CRUD ----

// CreateRecord inserts a new referral tracking record.
// Postgres: INSERT INTO referral_records (id, type, referrer_id, referrer_role, referee_id, referee_role, code, rides_required, rides_done, referrer_credited, referee_credited, referrer_amount, referee_amount, created_at, completed_at) VALUES ($1::uuid,...)
func (s *Store) CreateRecord(ctx context.Context, rec *Record) error {
	if rec.ID == "" {
		rec.ID = ids.New()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(ctx,
		`INSERT INTO referral_records (id, type, referrer_id, referrer_role, referee_id, referee_role, code, rides_required, rides_done, referrer_credited, referee_credited, referrer_amount, referee_amount, created_at, completed_at) VALUES ($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		rec.ID, rec.Type, rec.ReferrerID, rec.ReferrerRole, rec.RefereeID, rec.RefereeRole, rec.Code, rec.RidesRequired, rec.RidesDone, rec.ReferrerCredited, rec.RefereeCredited, rec.ReferrerAmount, rec.RefereeAmount, rec.CreatedAt, rec.CompletedAt,
	)
	return err
}

// FindPendingByReferee returns referral records for a referee that still need
// attention: any row whose referrer credit has not landed. Threshold rows
// (rides_done < rides_required) are still counting; at/over-threshold rows
// with referrer_credited=false are retries of a failed credit Tx. The retry
// lane is bounded above by the service (it only increments while below
// threshold and credits via atomic claim), so overshoot rows are retried,
// never re-counted, and never double-credited.
func (s *Store) FindPendingByReferee(ctx context.Context, refereeID, refereeRole string) ([]Record, error) {
	rows, err := s.db.Query(ctx, recordSelect+` WHERE referee_id=$1 AND referee_role=$2 AND referrer_credited=false ORDER BY created_at ASC LIMIT 20`, refereeID, refereeRole)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Record
	for rows.Next() {
		var r Record
		var completedAt sql.NullTime
		if err := rows.Scan(&r.ID, &r.Type, &r.ReferrerID, &r.ReferrerRole, &r.RefereeID, &r.RefereeRole, &r.Code, &r.RidesRequired, &r.RidesDone, &r.ReferrerCredited, &r.RefereeCredited, &r.ReferrerAmount, &r.RefereeAmount, &r.CreatedAt, &completedAt); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			t := completedAt.Time
			r.CompletedAt = &t
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Record{}
	}
	return list, nil
}

// FindPendingByUser finds pending referral records where the given user is the referee.
func (s *Store) FindPendingByUser(ctx context.Context, userID string) ([]Record, error) {
	return s.FindPendingByReferee(ctx, userID, "user")
}

// FindPendingByDriver finds pending referral records where the given driver is the referee.
func (s *Store) FindPendingByDriver(ctx context.Context, driverID string) ([]Record, error) {
	return s.FindPendingByReferee(ctx, driverID, "driver")
}

// IncrementRidesDone atomically increments rides_done by 1 for a referral record.
// Returns the updated record.
// Postgres: UPDATE referral_records SET rides_done = rides_done + 1 WHERE id=$1::uuid RETURNING *
func (s *Store) IncrementRidesDone(ctx context.Context, recordID string) (*Record, error) {
	row := s.db.QueryRow(ctx,
		`UPDATE referral_records SET rides_done = rides_done + 1 WHERE id=$1::uuid RETURNING id::text, type, referrer_id, referrer_role, referee_id, referee_role, code, rides_required, rides_done, referrer_credited, referee_credited, referrer_amount, referee_amount, created_at, completed_at`,
		recordID,
	)
	return scanRecord(row)
}

// MarkReferrerCredited marks the referrer as credited for a referral record.
// Postgres: UPDATE referral_records SET referrer_credited=true, completed_at=now() WHERE id=$1::uuid
func (s *Store) MarkReferrerCredited(ctx context.Context, recordID string) error {
	_, err := s.db.Exec(ctx, `UPDATE referral_records SET referrer_credited=true, completed_at=now() WHERE id=$1::uuid`, recordID)
	return err
}

// ClaimReferrerCredit flips referrer_credited=false→true atomically and
// reports whether this caller won the claim. Concurrent threshold completions
// race here; exactly one wins, the loser skips the wallet credit (no double
// bonus). Must run inside the same Tx as the wallet credit.
func (s *Store) ClaimReferrerCredit(ctx context.Context, recordID string) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE referral_records SET referrer_credited=true, completed_at=now() WHERE id=$1::uuid AND referrer_credited=false`, recordID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ClaimRefereeCredit is the referee-side equivalent (welcome bonus).
func (s *Store) ClaimRefereeCredit(ctx context.Context, recordID string) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE referral_records SET referee_credited=true WHERE id=$1::uuid AND referee_credited=false`, recordID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// MarkRefereeCredited marks the referee as credited for a referral record.
// Postgres: UPDATE referral_records SET referee_credited=true WHERE id=$1::uuid
func (s *Store) MarkRefereeCredited(ctx context.Context, recordID string) error {
	_, err := s.db.Exec(ctx, `UPDATE referral_records SET referee_credited=true WHERE id=$1::uuid`, recordID)
	return err
}

// GetRecordByID fetches a single referral record by ID.
// Postgres: SELECT * FROM referral_records WHERE id=$1::uuid
func (s *Store) GetRecordByID(ctx context.Context, id string) (*Record, error) {
	row := s.db.QueryRow(ctx, recordSelect+` WHERE id=$1::uuid`, id)
	rec, err := scanRecord(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return rec, nil
}

// ListByReferrer returns all referral records where referrer_id matches.
// Postgres: SELECT * FROM referral_records WHERE referrer_id=$1 ORDER BY created_at DESC
func (s *Store) ListByReferrer(ctx context.Context, referrerID string) ([]Record, error) {
	rows, err := s.db.Query(ctx, recordSelect+` WHERE referrer_id=$1 ORDER BY created_at DESC`, referrerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Record
	for rows.Next() {
		var r Record
		var completedAt sql.NullTime
		if err := rows.Scan(&r.ID, &r.Type, &r.ReferrerID, &r.ReferrerRole, &r.RefereeID, &r.RefereeRole, &r.Code, &r.RidesRequired, &r.RidesDone, &r.ReferrerCredited, &r.RefereeCredited, &r.ReferrerAmount, &r.RefereeAmount, &r.CreatedAt, &completedAt); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			t := completedAt.Time
			r.CompletedAt = &t
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Record{}
	}
	return list, nil
}

// ListByReferee returns all referral records where referee_id matches.
// Postgres: SELECT * FROM referral_records WHERE referee_id=$1 ORDER BY created_at DESC
func (s *Store) ListByReferee(ctx context.Context, refereeID string) ([]Record, error) {
	rows, err := s.db.Query(ctx, recordSelect+` WHERE referee_id=$1 ORDER BY created_at DESC`, refereeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Record
	for rows.Next() {
		var r Record
		var completedAt sql.NullTime
		if err := rows.Scan(&r.ID, &r.Type, &r.ReferrerID, &r.ReferrerRole, &r.RefereeID, &r.RefereeRole, &r.Code, &r.RidesRequired, &r.RidesDone, &r.ReferrerCredited, &r.RefereeCredited, &r.ReferrerAmount, &r.RefereeAmount, &r.CreatedAt, &completedAt); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			t := completedAt.Time
			r.CompletedAt = &t
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Record{}
	}
	return list, nil
}
