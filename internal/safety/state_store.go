package safety

import (
	"context"
	"errors"
	"time"

	"ambigo-backend/internal/ids"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type VehicleState struct {
	RideID       string    `json:"ride_id"`
	DriverID     string    `json:"driver_id"`
	AnchorLat    float64   `json:"anchor_lat"`
	AnchorLng    float64   `json:"anchor_lng"`
	StoppedSince time.Time `json:"stopped_since"`
	Warned       bool      `json:"warned"`
	Escalated    bool      `json:"escalated"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type VehicleStateView struct {
	RideID         string    `json:"ride_id"`
	DriverID       string    `json:"driver_id"`
	StoppedSince   time.Time `json:"stopped_since"`
	StoppedSeconds int64     `json:"stopped_seconds"`
	Stage          string    `json:"stage"`
}

func (s VehicleState) View(now time.Time) VehicleStateView {
	seconds := int64(now.Sub(s.StoppedSince) / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	stage := "monitoring"
	if s.Escalated {
		stage = "emergency"
	} else if s.Warned {
		stage = "warning"
	}
	return VehicleStateView{
		RideID:         s.RideID,
		DriverID:       s.DriverID,
		StoppedSince:   s.StoppedSince,
		StoppedSeconds: seconds,
		Stage:          stage,
	}
}

type StateStore struct {
	pool *pgxpool.Pool
}

func NewStateStore(pool *pgxpool.Pool) *StateStore {
	return &StateStore{pool: pool}
}

func (s *StateStore) Upsert(ctx context.Context, state VehicleState) error {
	if !ids.IsValid(state.RideID) || !ids.IsValid(state.DriverID) {
		return nil
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO ride_safety_state (
		ride_id, driver_id, anchor_lat, anchor_lng, stopped_since, warned, escalated, updated_at
	) SELECT $1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8
	WHERE EXISTS (
		SELECT 1 FROM rides WHERE id=$1::uuid AND driver_id=$2::uuid AND status='IN_PROGRESS'
	)
	ON CONFLICT (ride_id) DO UPDATE SET
		driver_id=EXCLUDED.driver_id,
		anchor_lat=EXCLUDED.anchor_lat,
		anchor_lng=EXCLUDED.anchor_lng,
		stopped_since=EXCLUDED.stopped_since,
		warned=EXCLUDED.warned,
		escalated=EXCLUDED.escalated,
		updated_at=EXCLUDED.updated_at
	WHERE ride_safety_state.updated_at <= EXCLUDED.updated_at`,
		state.RideID, state.DriverID, state.AnchorLat, state.AnchorLng,
		state.StoppedSince, state.Warned, state.Escalated, state.UpdatedAt,
	)
	return err
}

func (s *StateStore) DeleteByDriver(ctx context.Context, driverID string) error {
	if !ids.IsValid(driverID) {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM ride_safety_state WHERE driver_id=$1::uuid`, driverID)
	return err
}

func (s *StateStore) GetByRide(ctx context.Context, rideID string) (*VehicleState, error) {
	if !ids.IsValid(rideID) {
		return nil, nil
	}
	var state VehicleState
	err := s.pool.QueryRow(ctx, `SELECT ride_id::text, driver_id::text, anchor_lat, anchor_lng,
		stopped_since, warned, escalated, updated_at
		FROM ride_safety_state WHERE ride_id=$1::uuid`, rideID).Scan(
		&state.RideID, &state.DriverID, &state.AnchorLat, &state.AnchorLng,
		&state.StoppedSince, &state.Warned, &state.Escalated, &state.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &state, err
}

func (s *StateStore) ListActive(ctx context.Context) ([]VehicleState, error) {
	rows, err := s.pool.Query(ctx, `SELECT s.ride_id::text, s.driver_id::text, s.anchor_lat, s.anchor_lng,
		s.stopped_since, s.warned, s.escalated, s.updated_at
		FROM ride_safety_state s
		JOIN rides r ON r.id=s.ride_id AND r.driver_id=s.driver_id
		WHERE r.status='IN_PROGRESS'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make([]VehicleState, 0)
	for rows.Next() {
		var state VehicleState
		if err := rows.Scan(
			&state.RideID, &state.DriverID, &state.AnchorLat, &state.AnchorLng,
			&state.StoppedSince, &state.Warned, &state.Escalated, &state.UpdatedAt,
		); err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}
