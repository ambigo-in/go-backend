package offer

import (
	"context"
	"database/sql"
	"errors"

	"ambigo-backend/internal/ids"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store provides CRUD for offers backed by Postgres.
//
// Table: offers(id UUID PK, description TEXT, user_id TEXT, offer_percentage NUMERIC, offer_amount NUMERIC, max_discount NUMERIC)
type Store struct {
	pool *pgxpool.Pool
}

// NewStore creates a Store backed by the given pgxpool.Pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Create inserts a new offer. If o.ID is empty a UUID v4 is generated via ids.New().
func (s *Store) Create(ctx context.Context, o *Offer) error {
	if o.ID == "" {
		o.ID = ids.New()
	}
	// pgx handles *string/*float64 nil correctly (encodes as NULL), but we
	// explicitly handle typed-nil to avoid driver type confusion: passing a
	// typed nil *string as interface{} is not the same as untyped nil.
	var userID any = o.UserID
	if o.UserID == nil {
		userID = nil
	}
	var offerPerc any = o.OfferPercentage
	if o.OfferPercentage == nil {
		offerPerc = nil
	}
	var offerAmt any = o.OfferAmount
	if o.OfferAmount == nil {
		offerAmt = nil
	}
	var maxDisc any = o.MaxDiscount
	if o.MaxDiscount == nil {
		maxDisc = nil
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO offers(id, description, user_id, offer_percentage, offer_amount, max_discount) VALUES($1::uuid,$2,$3,$4,$5,$6)`,
		o.ID, o.Description, userID, offerPerc, offerAmt, maxDisc,
	)
	return err
}

// List returns all offers capped at 50 rows. For larger sets use ListPaginated.
func (s *Store) List(ctx context.Context) ([]Offer, error) {
	return s.ListPaginated(ctx, 50, "")
}

// ListPaginated returns offers with keyset pagination and a hard cap of 50.
func (s *Store) ListPaginated(ctx context.Context, limit int, cursor string) ([]Offer, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	if cursor != "" && ids.IsValid(cursor) {
		rows, err = s.pool.Query(ctx,
			`SELECT id::text, description, user_id, offer_percentage, offer_amount, max_discount FROM offers WHERE (created_at, id) < ((SELECT created_at FROM offers WHERE id=$2::uuid), $2::uuid) ORDER BY created_at DESC, id DESC LIMIT $1`, limit, cursor)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT id::text, description, user_id, offer_percentage, offer_amount, max_discount FROM offers ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Offer
	for rows.Next() {
		var o Offer
		var userID sql.NullString
		var offerPerc, offerAmount, maxDiscount sql.NullFloat64
		if err := rows.Scan(&o.ID, &o.Description, &userID, &offerPerc, &offerAmount, &maxDiscount); err != nil {
			return nil, err
		}
		if userID.Valid {
			v := userID.String
			o.UserID = &v
		}
		if offerPerc.Valid {
			v := offerPerc.Float64
			o.OfferPercentage = &v
		}
		if offerAmount.Valid {
			v := offerAmount.Float64
			o.OfferAmount = &v
		}
		if maxDiscount.Valid {
			v := maxDiscount.Float64
			o.MaxDiscount = &v
		}
		list = append(list, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Offer{}
	}
	return list, nil
}

func (s *Store) ListWithOffset(ctx context.Context, limit int, offset int) ([]Offer, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, description, user_id, offer_percentage, offer_amount, max_discount FROM offers ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Offer
	for rows.Next() {
		var o Offer
		var userID sql.NullString
		var offerPerc, offerAmount, maxDiscount sql.NullFloat64
		if err := rows.Scan(&o.ID, &o.Description, &userID, &offerPerc, &offerAmount, &maxDiscount); err != nil {
			return nil, err
		}
		if userID.Valid {
			v := userID.String
			o.UserID = &v
		}
		if offerPerc.Valid {
			v := offerPerc.Float64
			o.OfferPercentage = &v
		}
		if offerAmount.Valid {
			v := offerAmount.Float64
			o.OfferAmount = &v
		}
		if maxDiscount.Valid {
			v := maxDiscount.Float64
			o.MaxDiscount = &v
		}
		list = append(list, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Offer{}
	}
	return list, nil
}

// GetByID returns the offer with the given UUID string, or (nil, nil) if not found.
// The id param is a UUID string (ids.IsValid). Callers should pass string directly.
func (s *Store) GetByID(ctx context.Context, id string) (*Offer, error) {
	var o Offer
	var userID sql.NullString
	var offerPerc, offerAmount, maxDiscount sql.NullFloat64
	err := s.pool.QueryRow(ctx,
		`SELECT id::text, description, user_id, offer_percentage, offer_amount, max_discount FROM offers WHERE id=$1::uuid`,
		id,
	).Scan(&o.ID, &o.Description, &userID, &offerPerc, &offerAmount, &maxDiscount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if userID.Valid {
		v := userID.String
		o.UserID = &v
	}
	if offerPerc.Valid {
		v := offerPerc.Float64
		o.OfferPercentage = &v
	}
	if offerAmount.Valid {
		v := offerAmount.Float64
		o.OfferAmount = &v
	}
	if maxDiscount.Valid {
		v := maxDiscount.Float64
		o.MaxDiscount = &v
	}
	return &o, nil
}

// Delete removes the offer with the given UUID string. No error if the row does not exist.
func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM offers WHERE id=$1::uuid`, id)
	return err
}

// ClaimHighestByUser atomically consumes the user's highest-value offer and
// returns its amount. The DELETE...RETURNING is a single statement: concurrent
// consumers race on the row lock and exactly one wins (SKIP LOCKED lets the
// loser take the next offer or report none). Returns claimed=false when the
// user has no usable offer. This replaces read-then-delete, under which two
// concurrent rides both spent the same credit.
func (s *Store) ClaimHighestByUser(ctx context.Context, userID string) (amount float64, claimed bool, err error) {
	var amt sql.NullFloat64
	err = s.pool.QueryRow(ctx,
		`DELETE FROM offers WHERE id = (
			SELECT id FROM offers
			WHERE user_id=$1 AND offer_amount IS NOT NULL AND offer_amount > 0
			ORDER BY offer_amount DESC LIMIT 1
			FOR UPDATE SKIP LOCKED
		) RETURNING offer_amount`,
		userID,
	).Scan(&amt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if !amt.Valid || amt.Float64 <= 0 {
		return 0, false, nil
	}
	return amt.Float64, true, nil
}

// RestoreCredit re-inserts a consumed offer (compensation when the ride that
// consumed it ultimately fails). Value is preserved; identity is new —
// callers must log the swap.
func (s *Store) RestoreCredit(ctx context.Context, userID string, amount float64, description string) error {
	return s.Create(ctx, &Offer{
		Description: description,
		UserID:      &userID,
		OfferAmount: &amount,
	})
}

// FindByUserID returns offers for a given user_id (TEXT) capped at 50. Returns empty slice when none.
func (s *Store) FindByUserID(ctx context.Context, userID string) ([]Offer, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, description, user_id, offer_percentage, offer_amount, max_discount FROM offers WHERE user_id=$1 ORDER BY created_at DESC, id DESC LIMIT 50`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Offer
	for rows.Next() {
		var o Offer
		var uid sql.NullString
		var offerPerc, offerAmount, maxDiscount sql.NullFloat64
		if err := rows.Scan(&o.ID, &o.Description, &uid, &offerPerc, &offerAmount, &maxDiscount); err != nil {
			return nil, err
		}
		if uid.Valid {
			v := uid.String
			o.UserID = &v
		}
		if offerPerc.Valid {
			v := offerPerc.Float64
			o.OfferPercentage = &v
		}
		if offerAmount.Valid {
			v := offerAmount.Float64
			o.OfferAmount = &v
		}
		if maxDiscount.Valid {
			v := maxDiscount.Float64
			o.MaxDiscount = &v
		}
		list = append(list, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Offer{}
	}
	return list, nil
}
