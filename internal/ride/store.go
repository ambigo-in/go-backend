package ride

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

// Store persists rides in Postgres via pgxpool.
// Table: rides per migrations/00001_init.sql + ride_condition_updates for history.
type Store struct {
	pool *pgxpool.Pool
	db   DBTX
}

// NewStore creates a Store backed by the given pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, db: pool}
}

// NewStoreWithDB creates a Store from any DBTX (pool or Tx). Useful for tests.
func NewStoreWithDB(db DBTX) *Store {
	return &Store{db: db}
}

// WithTx returns a new Store bound to the given transaction.
func (s *Store) WithTx(tx pgx.Tx) *Store {
	return &Store{pool: s.pool, db: tx}
}

// Pool returns the underlying pool (may be nil if Store was created with NewStoreWithDB).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// WithTx runs fn inside a transaction. Mirrors payment.WithTx pattern.
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

// rideSelect is the canonical column list for scanning a Ride.
// Uses ::text casts for UUID columns so they scan as string.
// JSONB columns (pickup, drop, available_types, latest_condition, condition_on_arrival) are scanned as []byte.
const rideSelect = `SELECT id::text, user_id::text, driver_id::text, amb_type_id::text, hospital_id::text, start_otp, status, pickup, pickup_address, pickup_h3_cell, drop, drop_address, route_distance_km, route_duration_seconds, route_polyline, fare_base, fare_distance, fare_emergency, fare_night, fare_waiting, fare_total, fare_driver_share, fare_referral_discount, fare_currency, emergency_type, emergency_priority, payment_mode, payment_id::text, created_at, assigned_at, arrived_at, started_at, completed_at, cancelled_at, dispatch_candidates_searched, dispatch_offers_sent, dispatch_offers_declined, dispatch_offers_timed_out, dispatch_assignment_latency_ms, cancellation_reason, available_types, latest_condition, condition_on_arrival FROM rides`

// scanRide scans a single row into a Ride. Handles JSONB unmarshaling and NULLs.
func scanRide(row pgx.Row) (*Ride, error) {
	var (
		id                    string
		userID                string
		driverIDNS            sql.NullString
		ambTypeIDNS           sql.NullString
		hospitalIDNS          sql.NullString
		startOTPNS            sql.NullString
		status                string
		pickupJSON            []byte
		pickupAddress         string
		pickupH3Cell          string
		dropJSON              []byte
		dropAddress           string
		routeDistance         sql.NullFloat64
		routeDuration         sql.NullInt32
		routePolylineNS       sql.NullString
		fareBase              sql.NullFloat64
		fareDistance          sql.NullFloat64
		fareEmergency         sql.NullFloat64
		fareNight             sql.NullFloat64
		fareWaiting           sql.NullFloat64
		fareTotal             sql.NullFloat64
		fareDriverShare       sql.NullFloat64
		fareReferralDiscount  sql.NullFloat64
		fareCurrencyNS        sql.NullString
		emergencyTypeNS       sql.NullString
		emergencyPriority     int
		paymentMode           string
		paymentIDNS           sql.NullString
		createdAt             time.Time
		assignedAt            sql.NullTime
		arrivedAt             sql.NullTime
		startedAt             sql.NullTime
		completedAt           sql.NullTime
		cancelledAt           sql.NullTime
		dispatchCandidates    int
		dispatchOffersSent    int
		dispatchOffersDeclined int
		dispatchOffersTimedOut int
		dispatchLatency       int
		cancellationReasonNS  sql.NullString
		availableTypesJSON    []byte
		latestConditionJSON   []byte
		conditionOnArrivalJSON []byte
	)

	err := row.Scan(
		&id,
		&userID,
		&driverIDNS,
		&ambTypeIDNS,
		&hospitalIDNS,
		&startOTPNS,
		&status,
		&pickupJSON,
		&pickupAddress,
		&pickupH3Cell,
		&dropJSON,
		&dropAddress,
		&routeDistance,
		&routeDuration,
		&routePolylineNS,
		&fareBase,
		&fareDistance,
		&fareEmergency,
		&fareNight,
		&fareWaiting,
		&fareTotal,
		&fareDriverShare,
		&fareReferralDiscount,
		&fareCurrencyNS,
		&emergencyTypeNS,
		&emergencyPriority,
		&paymentMode,
		&paymentIDNS,
		&createdAt,
		&assignedAt,
		&arrivedAt,
		&startedAt,
		&completedAt,
		&cancelledAt,
		&dispatchCandidates,
		&dispatchOffersSent,
		&dispatchOffersDeclined,
		&dispatchOffersTimedOut,
		&dispatchLatency,
		&cancellationReasonNS,
		&availableTypesJSON,
		&latestConditionJSON,
		&conditionOnArrivalJSON,
	)
	if err != nil {
		return nil, err
	}

	r := &Ride{
		ID:                id,
		UserID:            userID,
		Status:            RideStatus(status),
		PickupAddress:     pickupAddress,
		PickupH3Cell:      pickupH3Cell,
		DropAddress:       dropAddress,
		EmergencyPriority: emergencyPriority,
		PaymentMode:       paymentMode,
		Time: TimeLog{
			CreatedAt: createdAt,
		},
		DispatchMetadata: DispatchMetadata{
			CandidatesSearched:  dispatchCandidates,
			OffersSent:          dispatchOffersSent,
			OffersDeclined:      dispatchOffersDeclined,
			OffersTimedOut:      dispatchOffersTimedOut,
			AssignmentLatencyMs: dispatchLatency,
		},
	}

	if driverIDNS.Valid && driverIDNS.String != "" {
		s := driverIDNS.String
		r.DriverID = &s
	}
	if ambTypeIDNS.Valid && ambTypeIDNS.String != "" {
		s := ambTypeIDNS.String
		r.AmbTypeID = &s
	}
	if hospitalIDNS.Valid && hospitalIDNS.String != "" {
		s := hospitalIDNS.String
		r.HospitalID = &s
	}
	if startOTPNS.Valid {
		r.StartOTP = startOTPNS.String
	}
	if emergencyTypeNS.Valid && emergencyTypeNS.String != "" {
		s := emergencyTypeNS.String
		r.EmergencyType = &s
	}
	if paymentIDNS.Valid && paymentIDNS.String != "" {
		s := paymentIDNS.String
		r.PaymentID = &s
	}
	if cancellationReasonNS.Valid {
		r.CancellationReason = cancellationReasonNS.String
	}

	// Pickup / Drop JSONB -> GeoJSONPoint
	if len(pickupJSON) > 0 {
		var p GeoJSONPoint
		if err := json.Unmarshal(pickupJSON, &p); err == nil {
			r.Pickup = p
		}
	}
	if len(dropJSON) > 0 {
		var d GeoJSONPoint
		if err := json.Unmarshal(dropJSON, &d); err == nil {
			r.Drop = d
		}
	}

	// Route normalized columns -> *Route
	if routeDistance.Valid || routeDuration.Valid || routePolylineNS.Valid {
		route := &Route{}
		if routeDistance.Valid {
			route.DistanceKm = routeDistance.Float64
		}
		if routeDuration.Valid {
			route.DurationSeconds = int(routeDuration.Int32)
		}
		if routePolylineNS.Valid {
			route.Polyline = routePolylineNS.String
		}
		// Only keep non-zero route? Keep if any field set
		if route.DistanceKm != 0 || route.DurationSeconds != 0 || route.Polyline != "" {
			r.Route = route
		} else if routeDistance.Valid || routeDuration.Valid || routePolylineNS.Valid {
			// Preserve even if zero values but DB had explicit null vs 0 distinction
			r.Route = route
		}
	}

	// Fare normalized columns -> *Fare
	if fareBase.Valid || fareDistance.Valid || fareEmergency.Valid || fareNight.Valid || fareWaiting.Valid || fareTotal.Valid || fareDriverShare.Valid || fareReferralDiscount.Valid || fareCurrencyNS.Valid {
		fare := &Fare{}
		if fareBase.Valid {
			fare.BaseFare = fareBase.Float64
		}
		if fareDistance.Valid {
			fare.DistanceFare = fareDistance.Float64
		}
		if fareEmergency.Valid {
			fare.EmergencySurcharge = fareEmergency.Float64
		}
		if fareNight.Valid {
			fare.NightSurcharge = fareNight.Float64
		}
		if fareWaiting.Valid {
			fare.WaitingCharge = fareWaiting.Float64
		}
		if fareTotal.Valid {
			fare.Total = fareTotal.Float64
		}
		if fareDriverShare.Valid {
			fare.DriverShare = fareDriverShare.Float64
		}
		if fareReferralDiscount.Valid {
			fare.ReferralDiscount = fareReferralDiscount.Float64
		}
		if fareCurrencyNS.Valid && fareCurrencyNS.String != "" {
			fare.Currency = fareCurrencyNS.String
		} else {
			fare.Currency = "INR"
		}
		r.Fare = fare
	}

	// TimeLog nullable timestamps
	if assignedAt.Valid {
		t := assignedAt.Time
		r.Time.AssignedAt = &t
	}
	if arrivedAt.Valid {
		t := arrivedAt.Time
		r.Time.ArrivedAt = &t
	}
	if startedAt.Valid {
		t := startedAt.Time
		r.Time.StartedAt = &t
	}
	if completedAt.Valid {
		t := completedAt.Time
		r.Time.CompletedAt = &t
	}
	if cancelledAt.Valid {
		t := cancelledAt.Time
		r.Time.CancelledAt = &t
	}

	// AvailableTypes JSONB array
	if len(availableTypesJSON) > 0 && string(availableTypesJSON) != "null" {
		var at []string
		if err := json.Unmarshal(availableTypesJSON, &at); err == nil {
			r.AvailableTypes = at
		}
	}

	// LatestCondition JSONB
	if len(latestConditionJSON) > 0 && string(latestConditionJSON) != "null" {
		var lc ConditionUpdate
		if err := json.Unmarshal(latestConditionJSON, &lc); err == nil {
			r.LatestCondition = &lc
		}
	}
	if len(conditionOnArrivalJSON) > 0 && string(conditionOnArrivalJSON) != "null" {
		var ca ConditionUpdate
		if err := json.Unmarshal(conditionOnArrivalJSON, &ca); err == nil {
			r.ConditionOnArrival = &ca
		}
	}

	// ConditionUpdates is not stored in rides table (moved to ride_condition_updates); leave nil.

	return r, nil
}

// scanRides scans all rows into a slice.
func scanRides(rows pgx.Rows) ([]*Ride, error) {
	var rides []*Ride
	for rows.Next() {
		// We need to scan using the same logic as scanRide but from rows.
		// Reuse scan by creating a row wrapper: rows as pgx.Row is not directly compatible,
		// so we manually scan here with same variables.
		var (
			id                    string
			userID                string
			driverIDNS            sql.NullString
			ambTypeIDNS           sql.NullString
			hospitalIDNS          sql.NullString
			startOTPNS            sql.NullString
			status                string
			pickupJSON            []byte
			pickupAddress         string
			pickupH3Cell          string
			dropJSON              []byte
			dropAddress           string
			routeDistance         sql.NullFloat64
			routeDuration         sql.NullInt32
			routePolylineNS       sql.NullString
			fareBase              sql.NullFloat64
			fareDistance          sql.NullFloat64
			fareEmergency         sql.NullFloat64
			fareNight             sql.NullFloat64
			fareWaiting           sql.NullFloat64
			fareTotal             sql.NullFloat64
			fareDriverShare       sql.NullFloat64
			fareReferralDiscount  sql.NullFloat64
			fareCurrencyNS        sql.NullString
			emergencyTypeNS       sql.NullString
			emergencyPriority     int
			paymentMode           string
			paymentIDNS           sql.NullString
			createdAt             time.Time
			assignedAt            sql.NullTime
			arrivedAt             sql.NullTime
			startedAt             sql.NullTime
			completedAt           sql.NullTime
			cancelledAt           sql.NullTime
			dispatchCandidates    int
			dispatchOffersSent    int
			dispatchOffersDeclined int
			dispatchOffersTimedOut int
			dispatchLatency       int
			cancellationReasonNS  sql.NullString
			availableTypesJSON    []byte
			latestConditionJSON   []byte
			conditionOnArrivalJSON []byte
		)
		err := rows.Scan(
			&id,
			&userID,
			&driverIDNS,
			&ambTypeIDNS,
			&hospitalIDNS,
			&startOTPNS,
			&status,
			&pickupJSON,
			&pickupAddress,
			&pickupH3Cell,
			&dropJSON,
			&dropAddress,
			&routeDistance,
			&routeDuration,
			&routePolylineNS,
			&fareBase,
			&fareDistance,
			&fareEmergency,
			&fareNight,
			&fareWaiting,
			&fareTotal,
			&fareDriverShare,
			&fareReferralDiscount,
			&fareCurrencyNS,
			&emergencyTypeNS,
			&emergencyPriority,
			&paymentMode,
			&paymentIDNS,
			&createdAt,
			&assignedAt,
			&arrivedAt,
			&startedAt,
			&completedAt,
			&cancelledAt,
			&dispatchCandidates,
			&dispatchOffersSent,
			&dispatchOffersDeclined,
			&dispatchOffersTimedOut,
			&dispatchLatency,
			&cancellationReasonNS,
			&availableTypesJSON,
			&latestConditionJSON,
			&conditionOnArrivalJSON,
		)
		if err != nil {
			return nil, err
		}
		r := &Ride{
			ID:                id,
			UserID:            userID,
			Status:            RideStatus(status),
			PickupAddress:     pickupAddress,
			PickupH3Cell:      pickupH3Cell,
			DropAddress:       dropAddress,
			EmergencyPriority: emergencyPriority,
			PaymentMode:       paymentMode,
			Time: TimeLog{
				CreatedAt: createdAt,
			},
			DispatchMetadata: DispatchMetadata{
				CandidatesSearched:  dispatchCandidates,
				OffersSent:          dispatchOffersSent,
				OffersDeclined:      dispatchOffersDeclined,
				OffersTimedOut:      dispatchOffersTimedOut,
				AssignmentLatencyMs: dispatchLatency,
			},
		}
		if driverIDNS.Valid && driverIDNS.String != "" {
			s := driverIDNS.String
			r.DriverID = &s
		}
		if ambTypeIDNS.Valid && ambTypeIDNS.String != "" {
			s := ambTypeIDNS.String
			r.AmbTypeID = &s
		}
		if hospitalIDNS.Valid && hospitalIDNS.String != "" {
			s := hospitalIDNS.String
			r.HospitalID = &s
		}
		if startOTPNS.Valid {
			r.StartOTP = startOTPNS.String
		}
		if emergencyTypeNS.Valid && emergencyTypeNS.String != "" {
			s := emergencyTypeNS.String
			r.EmergencyType = &s
		}
		if paymentIDNS.Valid && paymentIDNS.String != "" {
			s := paymentIDNS.String
			r.PaymentID = &s
		}
		if cancellationReasonNS.Valid {
			r.CancellationReason = cancellationReasonNS.String
		}
		if len(pickupJSON) > 0 {
			var p GeoJSONPoint
			if err := json.Unmarshal(pickupJSON, &p); err == nil {
				r.Pickup = p
			}
		}
		if len(dropJSON) > 0 {
			var d GeoJSONPoint
			if err := json.Unmarshal(dropJSON, &d); err == nil {
				r.Drop = d
			}
		}
		if routeDistance.Valid || routeDuration.Valid || routePolylineNS.Valid {
			route := &Route{}
			if routeDistance.Valid {
				route.DistanceKm = routeDistance.Float64
			}
			if routeDuration.Valid {
				route.DurationSeconds = int(routeDuration.Int32)
			}
			if routePolylineNS.Valid {
				route.Polyline = routePolylineNS.String
			}
			if route.DistanceKm != 0 || route.DurationSeconds != 0 || route.Polyline != "" {
				r.Route = route
			} else if routeDistance.Valid || routeDuration.Valid || routePolylineNS.Valid {
				r.Route = route
			}
		}
		if fareBase.Valid || fareDistance.Valid || fareEmergency.Valid || fareNight.Valid || fareWaiting.Valid || fareTotal.Valid || fareDriverShare.Valid || fareReferralDiscount.Valid || fareCurrencyNS.Valid {
			fare := &Fare{}
			if fareBase.Valid {
				fare.BaseFare = fareBase.Float64
			}
			if fareDistance.Valid {
				fare.DistanceFare = fareDistance.Float64
			}
			if fareEmergency.Valid {
				fare.EmergencySurcharge = fareEmergency.Float64
			}
			if fareNight.Valid {
				fare.NightSurcharge = fareNight.Float64
			}
			if fareWaiting.Valid {
				fare.WaitingCharge = fareWaiting.Float64
			}
			if fareTotal.Valid {
				fare.Total = fareTotal.Float64
			}
			if fareDriverShare.Valid {
				fare.DriverShare = fareDriverShare.Float64
			}
			if fareReferralDiscount.Valid {
				fare.ReferralDiscount = fareReferralDiscount.Float64
			}
			if fareCurrencyNS.Valid && fareCurrencyNS.String != "" {
				fare.Currency = fareCurrencyNS.String
			} else {
				fare.Currency = "INR"
			}
			r.Fare = fare
		}
		if assignedAt.Valid {
			t := assignedAt.Time
			r.Time.AssignedAt = &t
		}
		if arrivedAt.Valid {
			t := arrivedAt.Time
			r.Time.ArrivedAt = &t
		}
		if startedAt.Valid {
			t := startedAt.Time
			r.Time.StartedAt = &t
		}
		if completedAt.Valid {
			t := completedAt.Time
			r.Time.CompletedAt = &t
		}
		if cancelledAt.Valid {
			t := cancelledAt.Time
			r.Time.CancelledAt = &t
		}
		if len(availableTypesJSON) > 0 && string(availableTypesJSON) != "null" {
			var at []string
			if err := json.Unmarshal(availableTypesJSON, &at); err == nil {
				r.AvailableTypes = at
			}
		}
		if len(latestConditionJSON) > 0 && string(latestConditionJSON) != "null" {
			var lc ConditionUpdate
			if err := json.Unmarshal(latestConditionJSON, &lc); err == nil {
				r.LatestCondition = &lc
			}
		}
		if len(conditionOnArrivalJSON) > 0 && string(conditionOnArrivalJSON) != "null" {
			var ca ConditionUpdate
			if err := json.Unmarshal(conditionOnArrivalJSON, &ca); err == nil {
				r.ConditionOnArrival = &ca
			}
		}
		rides = append(rides, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if rides == nil {
		rides = []*Ride{}
	}
	return rides, nil
}

// CreateRide inserts a new ride document. Generates UUID via ids.New() if ID empty,
// sets Status=SEARCHING and created_at=now().
func (s *Store) CreateRide(ctx context.Context, ride *Ride) error {
	if ride.ID == "" {
		ride.ID = ids.New()
	}
	ride.Status = StatusSearching
	if ride.Time.CreatedAt.IsZero() {
		ride.Time.CreatedAt = time.Now()
	}

	pickupJSON, err := json.Marshal(ride.Pickup)
	if err != nil {
		return fmt.Errorf("marshal pickup: %w", err)
	}
	dropJSON, err := json.Marshal(ride.Drop)
	if err != nil {
		return fmt.Errorf("marshal drop: %w", err)
	}

	// Nullable UUID args
	var driverIDArg, ambTypeIDArg, hospitalIDArg, paymentIDArg interface{}
	if ride.DriverID != nil && *ride.DriverID != "" {
		driverIDArg = *ride.DriverID
	}
	if ride.AmbTypeID != nil && *ride.AmbTypeID != "" {
		ambTypeIDArg = *ride.AmbTypeID
	}
	if ride.HospitalID != nil && *ride.HospitalID != "" {
		hospitalIDArg = *ride.HospitalID
	}
	if ride.PaymentID != nil && *ride.PaymentID != "" {
		paymentIDArg = *ride.PaymentID
	}

	var emergencyTypeArg interface{}
	if ride.EmergencyType != nil && *ride.EmergencyType != "" {
		emergencyTypeArg = *ride.EmergencyType
	}

	var startOTPArg interface{}
	if ride.StartOTP != "" {
		startOTPArg = ride.StartOTP
	}

	// Route normalized
	var routeDistArg, routeDurationArg, routePolylineArg interface{}
	if ride.Route != nil {
		routeDistArg = ride.Route.DistanceKm
		routeDurationArg = ride.Route.DurationSeconds
		routePolylineArg = ride.Route.Polyline
	}

	// Fare normalized
	var fareBaseArg, fareDistanceArg, fareEmergencyArg, fareNightArg, fareWaitingArg, fareTotalArg, fareDriverShareArg, fareReferralDiscountArg interface{}
	var fareCurrencyArg interface{} = "INR"
	if ride.Fare != nil {
		fareBaseArg = ride.Fare.BaseFare
		fareDistanceArg = ride.Fare.DistanceFare
		fareEmergencyArg = ride.Fare.EmergencySurcharge
		fareNightArg = ride.Fare.NightSurcharge
		fareWaitingArg = ride.Fare.WaitingCharge
		fareTotalArg = ride.Fare.Total
		fareDriverShareArg = ride.Fare.DriverShare
		if ride.Fare.ReferralDiscount != 0 {
			fareReferralDiscountArg = ride.Fare.ReferralDiscount
		}
		if ride.Fare.Currency != "" {
			fareCurrencyArg = ride.Fare.Currency
		}
	}

	// TimeLog
	var assignedAtArg, arrivedAtArg, startedAtArg, completedAtArg, cancelledAtArg interface{}
	if ride.Time.AssignedAt != nil {
		assignedAtArg = *ride.Time.AssignedAt
	}
	if ride.Time.ArrivedAt != nil {
		arrivedAtArg = *ride.Time.ArrivedAt
	}
	if ride.Time.StartedAt != nil {
		startedAtArg = *ride.Time.StartedAt
	}
	if ride.Time.CompletedAt != nil {
		completedAtArg = *ride.Time.CompletedAt
	}
	if ride.Time.CancelledAt != nil {
		cancelledAtArg = *ride.Time.CancelledAt
	}

	// Cancellation
	var cancellationReasonArg interface{}
	if ride.CancellationReason != "" {
		cancellationReasonArg = ride.CancellationReason
	}

	var availableTypesArg interface{}
	if len(ride.AvailableTypes) > 0 {
		data, err := json.Marshal(ride.AvailableTypes)
		if err != nil {
			return fmt.Errorf("marshal available_types: %w", err)
		}
		availableTypesArg = data
	}

	var latestConditionArg, conditionOnArrivalArg interface{}
	if ride.LatestCondition != nil {
		data, err := json.Marshal(ride.LatestCondition)
		if err != nil {
			return fmt.Errorf("marshal latest_condition: %w", err)
		}
		latestConditionArg = data
	}
	if ride.ConditionOnArrival != nil {
		data, err := json.Marshal(ride.ConditionOnArrival)
		if err != nil {
			return fmt.Errorf("marshal condition_on_arrival: %w", err)
		}
		conditionOnArrivalArg = data
	}

	_, err = s.db.Exec(ctx,
		`INSERT INTO rides (id, user_id, driver_id, amb_type_id, hospital_id, start_otp, status, pickup, pickup_address, pickup_h3_cell, drop, drop_address, route_distance_km, route_duration_seconds, route_polyline, fare_base, fare_distance, fare_emergency, fare_night, fare_waiting, fare_total, fare_driver_share, fare_referral_discount, fare_currency, emergency_type, emergency_priority, payment_mode, payment_id, created_at, assigned_at, arrived_at, started_at, completed_at, cancelled_at, dispatch_candidates_searched, dispatch_offers_sent, dispatch_offers_declined, dispatch_offers_timed_out, dispatch_assignment_latency_ms, cancellation_reason, available_types, latest_condition, condition_on_arrival)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8::jsonb, $9, $10, $11::jsonb, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28::uuid, $29, $30, $31, $32, $33, $34, $35, $36, $37, $38, $39, $40, $41::jsonb, $42::jsonb, $43::jsonb)`,
		ride.ID,
		ride.UserID,
		driverIDArg,
		ambTypeIDArg,
		hospitalIDArg,
		startOTPArg,
		ride.Status,
		pickupJSON,
		ride.PickupAddress,
		ride.PickupH3Cell,
		dropJSON,
		ride.DropAddress,
		routeDistArg,
		routeDurationArg,
		routePolylineArg,
		fareBaseArg,
		fareDistanceArg,
		fareEmergencyArg,
		fareNightArg,
		fareWaitingArg,
		fareTotalArg,
		fareDriverShareArg,
		fareReferralDiscountArg,
		fareCurrencyArg,
		emergencyTypeArg,
		ride.EmergencyPriority,
		ride.PaymentMode,
		paymentIDArg,
		ride.Time.CreatedAt,
		assignedAtArg,
		arrivedAtArg,
		startedAtArg,
		completedAtArg,
		cancelledAtArg,
		ride.DispatchMetadata.CandidatesSearched,
		ride.DispatchMetadata.OffersSent,
		ride.DispatchMetadata.OffersDeclined,
		ride.DispatchMetadata.OffersTimedOut,
		ride.DispatchMetadata.AssignmentLatencyMs,
		cancellationReasonArg,
		availableTypesArg,
		latestConditionArg,
		conditionOnArrivalArg,
	)
	return err
}

// AtomicAssignDriver safely assigns a driver to a ride ONLY if the ride is still SEARCHING.
// Prevents double-booking. Uses single-statement CAS: UPDATE ... WHERE id=$1::uuid AND status='SEARCHING'
func (s *Store) AtomicAssignDriver(ctx context.Context, rideID string, driverID string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE rides SET status='ASSIGNED', driver_id=$2::uuid, assigned_at=now() WHERE id=$1::uuid AND status='SEARCHING' RETURNING *`,
		rideID, driverID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("ride is no longer available")
	}
	return nil
}

// UpdateRideStatus updates the ride status, validating the transition first.
// Uses UPDATE ... WHERE id=$1::uuid AND status=$3 and checks RowsAffected.
func (s *Store) UpdateRideStatus(ctx context.Context, rideID string, currentStatus, nextStatus RideStatus) error {
	if err := ValidateTransition(currentStatus, nextStatus); err != nil {
		return err
	}

	var timeField string
	switch nextStatus {
	case StatusArrived:
		timeField = "arrived_at"
	case StatusInProgress:
		timeField = "started_at"
	case StatusCompleted:
		timeField = "completed_at"
	case StatusCancelled:
		timeField = "cancelled_at"
	}

	var tag pgconn.CommandTag
	var err error
	if timeField != "" {
		q := fmt.Sprintf(`UPDATE rides SET status=$2, %s=now() WHERE id=$1::uuid AND status=$3`, timeField)
		tag, err = s.db.Exec(ctx, q, rideID, string(nextStatus), string(currentStatus))
	} else {
		tag, err = s.db.Exec(ctx, `UPDATE rides SET status=$2 WHERE id=$1::uuid AND status=$3`, rideID, string(nextStatus), string(currentStatus))
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("failed to update status, ride might have already changed state")
	}
	return nil
}

// CancelRide sets status to CANCELLED, records cancellation time and reason.
func (s *Store) CancelRide(ctx context.Context, rideID string, currentStatus RideStatus, reason string, availableTypes ...[]string) error {
	if err := ValidateTransition(currentStatus, StatusCancelled); err != nil {
		return err
	}

	var availJSON interface{}
	if len(availableTypes) > 0 && len(availableTypes[0]) > 0 {
		data, err := json.Marshal(availableTypes[0])
		if err != nil {
			return fmt.Errorf("marshal available_types: %w", err)
		}
		availJSON = data
	}

	tag, err := s.db.Exec(ctx,
		`UPDATE rides SET status='CANCELLED', cancelled_at=now(), cancellation_reason=$2, available_types=$3::jsonb WHERE id=$1::uuid AND status=$4`,
		rideID, reason, availJSON, string(currentStatus),
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("failed to cancel ride, ride might have already changed state")
	}
	return nil
}

// GetRideByID retrieves a single ride by id.
func (s *Store) GetRideByID(ctx context.Context, rideID string) (*Ride, error) {
	row := s.db.QueryRow(ctx, rideSelect+` WHERE id=$1::uuid`, rideID)
	ride, err := scanRide(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return ride, nil
}

// GetRideHistory returns a paginated list of rides for a specific user or driver.
func (s *Store) GetRideHistory(ctx context.Context, entityID string, role string, limit, skip int64) ([]*Ride, error) {
	if role != "user" && role != "driver" {
		return nil, errors.New("invalid role")
	}
	var q string
	if role == "user" {
		q = rideSelect + ` WHERE user_id=$1::uuid ORDER BY created_at DESC LIMIT $2 OFFSET $3`
	} else {
		q = rideSelect + ` WHERE driver_id=$1::uuid ORDER BY created_at DESC LIMIT $2 OFFSET $3`
	}
	rows, err := s.db.Query(ctx, q, entityID, limit, skip)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRides(rows)
}

// UpdateDispatchMetadata persists the dispatch metadata counters for a ride.
func (s *Store) UpdateDispatchMetadata(ctx context.Context, rideID string, meta *DispatchMetadata) error {
	if meta == nil {
		return errors.New("nil dispatch metadata")
	}
	_, err := s.db.Exec(ctx,
		`UPDATE rides SET dispatch_candidates_searched=$2, dispatch_offers_sent=$3, dispatch_offers_declined=$4, dispatch_offers_timed_out=$5, dispatch_assignment_latency_ms=$6 WHERE id=$1::uuid`,
		rideID, meta.CandidatesSearched, meta.OffersSent, meta.OffersDeclined, meta.OffersTimedOut, meta.AssignmentLatencyMs,
	)
	return err
}

// UpdateRideFare persists the fare breakdown for a ride.
func (s *Store) UpdateRideFare(ctx context.Context, rideID string, fare *Fare) error {
	if fare == nil {
		return errors.New("nil fare")
	}
	_, err := s.db.Exec(ctx,
		`UPDATE rides SET fare_base=$2, fare_distance=$3, fare_emergency=$4, fare_night=$5, fare_waiting=$6, fare_total=$7, fare_driver_share=$8, fare_referral_discount=$9, fare_currency=$10 WHERE id=$1::uuid`,
		rideID, fare.BaseFare, fare.DistanceFare, fare.EmergencySurcharge, fare.NightSurcharge, fare.WaitingCharge, fare.Total, fare.DriverShare, fare.ReferralDiscount, fare.Currency,
	)
	return err
}

// EscalateEmergencyForStoppedVehicle flips a normal IN_PROGRESS ride to
// emergency priority after a confirmed 5-minute stop. The guarded WHERE
// clause makes it idempotent: SOS rides, finished rides, or repeat calls
// affect 0 rows and report escalated=false. Fare is intentionally untouched
// (locked at booking time).
func (s *Store) EscalateEmergencyForStoppedVehicle(ctx context.Context, rideID string) (bool, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE rides SET emergency_priority=10, emergency_type=COALESCE(NULLIF(emergency_type,''),'stopped_vehicle') WHERE id=$1::uuid AND status='IN_PROGRESS' AND emergency_priority=0`,
		rideID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// CancelStaleSearchingRides cancels all rides in SEARCHING state older than maxAge.
// Returns the count of cancelled rides.
func (s *Store) CancelStaleSearchingRides(ctx context.Context, maxAge time.Duration) (int64, error) {
	cutoff := time.Now().Add(-maxAge)
	tag, err := s.db.Exec(ctx,
		`UPDATE rides SET status='CANCELLED', cancelled_at=now() WHERE status='SEARCHING' AND created_at < $1`,
		cutoff,
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ListRidesByStatus returns a paginated list of rides matching one of the given statuses.
func (s *Store) ListRidesByStatus(ctx context.Context, statuses []RideStatus, limit, skip int64) ([]*Ride, error) {
	strs := make([]string, len(statuses))
	for i, st := range statuses {
		strs[i] = string(st)
	}
	rows, err := s.db.Query(ctx, rideSelect+` WHERE status = ANY($1) ORDER BY created_at DESC LIMIT $2 OFFSET $3`, strs, limit, skip)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rides, err := scanRides(rows)
	if err != nil {
		return nil, err
	}
	return rides, nil
}

// GetCurrentRide returns the currently active ride (if any) for a user or driver.
// Active = SEARCHING, ASSIGNED, ARRIVED, IN_PROGRESS, ordered by created_at DESC.
func (s *Store) GetCurrentRide(ctx context.Context, entityID string, role string) (*Ride, error) {
	if role != "user" && role != "driver" {
		return nil, errors.New("invalid role")
	}
	row := s.db.QueryRow(ctx, rideSelect+` WHERE (user_id=$1::uuid OR driver_id=$1::uuid) AND status IN ('SEARCHING','ASSIGNED','ARRIVED','IN_PROGRESS') ORDER BY created_at DESC LIMIT 1`, entityID)
	ride, err := scanRide(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return ride, nil
}

// AppendConditionUpdate appends a condition update to a ride.
// In Postgres this is INSERT INTO ride_condition_updates + UPDATE rides SET latest_condition.
// Replaces Mongo $push + $set atomic op.
func (s *Store) AppendConditionUpdate(ctx context.Context, rideID string, upd ConditionUpdate) error {
	data, err := json.Marshal(upd)
	if err != nil {
		return fmt.Errorf("marshal condition: %w", err)
	}
	// Use transaction if pool is available to keep INSERT + UPDATE atomic.
	if s.pool != nil {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx,
			`INSERT INTO ride_condition_updates (id, ride_id, level, severity, note, source, created_at) VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7)`,
			ids.New(), rideID, upd.Level, upd.Severity, upd.Note, upd.Source, upd.CreatedAt,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE rides SET latest_condition=$2::jsonb WHERE id=$1::uuid`, rideID, data); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	// Fallback without pool Tx (e.g. when using DBTX directly)
	if _, err := s.db.Exec(ctx,
		`INSERT INTO ride_condition_updates (id, ride_id, level, severity, note, source, created_at) VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7)`,
		ids.New(), rideID, upd.Level, upd.Severity, upd.Note, upd.Source, upd.CreatedAt,
	); err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE rides SET latest_condition=$2::jsonb WHERE id=$1::uuid`, rideID, data)
	return err
}

// ListRidesByHospital returns rides for a hospital, optionally filtered by statuses, paginated.
func (s *Store) ListRidesByHospital(ctx context.Context, hospitalID string, statuses []RideStatus, limit, skip int64) ([]*Ride, error) {
	var rows pgx.Rows
	var err error
	if len(statuses) > 0 {
		strs := make([]string, len(statuses))
		for i, st := range statuses {
			strs[i] = string(st)
		}
		rows, err = s.db.Query(ctx, rideSelect+` WHERE hospital_id=$1::uuid AND status = ANY($2) ORDER BY created_at DESC LIMIT $3 OFFSET $4`, hospitalID, strs, limit, skip)
	} else {
		rows, err = s.db.Query(ctx, rideSelect+` WHERE hospital_id=$1::uuid ORDER BY created_at DESC LIMIT $2 OFFSET $3`, hospitalID, limit, skip)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rides, err := scanRides(rows)
	if err != nil {
		return nil, err
	}
	return rides, nil
}

// CountRidesByHospital counts rides for a hospital, optionally filtered by statuses.
func (s *Store) CountRidesByHospital(ctx context.Context, hospitalID string, statuses []RideStatus) (int64, error) {
	var row pgx.Row
	if len(statuses) > 0 {
		strs := make([]string, len(statuses))
		for i, st := range statuses {
			strs[i] = string(st)
		}
		row = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM rides WHERE hospital_id=$1::uuid AND status = ANY($2)`, hospitalID, strs)
	} else {
		row = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM rides WHERE hospital_id=$1::uuid`, hospitalID)
	}
	var count int64
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// ListRidesByHospitalSince returns all rides for a hospital created since the given time.
func (s *Store) ListRidesByHospitalSince(ctx context.Context, hospitalID string, since time.Time) ([]*Ride, error) {
	rows, err := s.db.Query(ctx, rideSelect+` WHERE hospital_id=$1::uuid AND created_at >= $2 ORDER BY created_at DESC`, hospitalID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rides, err := scanRides(rows)
	if err != nil {
		return nil, err
	}
	return rides, nil
}

// ListConditionUpdates returns full chat history for a ride, ordered oldest -> newest.
// Used by hospital portal to render WhatsApp-like timeline instead of just latest_condition.
func (s *Store) ListConditionUpdates(ctx context.Context, rideID string) ([]ConditionUpdate, error) {
	rows, err := s.db.Query(ctx, `SELECT level, severity, note, source, created_at FROM ride_condition_updates WHERE ride_id=$1::uuid ORDER BY created_at ASC`, rideID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConditionUpdate
	for rows.Next() {
		var cu ConditionUpdate
		if err := rows.Scan(&cu.Level, &cu.Severity, &cu.Note, &cu.Source, &cu.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, cu)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []ConditionUpdate{}
	}
	return out, nil
}

// ListConditionUpdatesBatch fetches histories for multiple rides in one query.
// Returns map[rideID] -> []ConditionUpdate ordered ASC.
func (s *Store) ListConditionUpdatesBatch(ctx context.Context, rideIDs []string) (map[string][]ConditionUpdate, error) {
	if len(rideIDs) == 0 {
		return map[string][]ConditionUpdate{}, nil
	}
	rows, err := s.db.Query(ctx, `SELECT ride_id::text, level, severity, note, source, created_at FROM ride_condition_updates WHERE ride_id = ANY($1::uuid[]) ORDER BY ride_id, created_at ASC`, rideIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string][]ConditionUpdate, len(rideIDs))
	for rows.Next() {
		var rideID string
		var cu ConditionUpdate
		if err := rows.Scan(&rideID, &cu.Level, &cu.Severity, &cu.Note, &cu.Source, &cu.CreatedAt); err != nil {
			return nil, err
		}
		m[rideID] = append(m[rideID], cu)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// ensure every requested id has at least empty slice
	for _, id := range rideIDs {
		if _, ok := m[id]; !ok {
			m[id] = []ConditionUpdate{}
		}
	}
	return m, nil
}

// PopulateConditionUpdates fills Ride.ConditionUpdates for each ride in slice.
// Call after ListRidesByHospital / ListRidesByHospitalSince to avoid N+1 on hospital pages.
func (s *Store) PopulateConditionUpdates(ctx context.Context, rides []*Ride) error {
	if len(rides) == 0 {
		return nil
	}
	ids := make([]string, 0, len(rides))
	for _, r := range rides {
		ids = append(ids, r.ID)
	}
	batch, err := s.ListConditionUpdatesBatch(ctx, ids)
	if err != nil {
		return err
	}
	for _, r := range rides {
		if cu, ok := batch[r.ID]; ok {
			r.ConditionUpdates = cu
		}
	}
	return nil
}
