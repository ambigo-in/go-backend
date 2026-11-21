package safety

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ambigo-backend/internal/ids"

	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrAlertNotActionable = errors.New("safety alert is not actionable")

type Alert struct {
	ID             string     `json:"id"`
	RideID         string     `json:"ride_id"`
	RideRef        string     `json:"ride_ref"`
	UserID         string     `json:"user_id,omitempty"`
	DriverID       string     `json:"driver_id,omitempty"`
	DriverName     string     `json:"driver_name,omitempty"`
	DriverMobile   string     `json:"driver_mobile,omitempty"`
	AlertType      string     `json:"alert_type"`
	Status         string     `json:"status"`
	Severity       string     `json:"severity"`
	LocationLat    *float64   `json:"location_lat,omitempty"`
	LocationLng    *float64   `json:"location_lng,omitempty"`
	LocationLabel  string     `json:"location_label,omitempty"`
	RaisedAt       time.Time  `json:"raised_at"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	AcknowledgedBy string     `json:"acknowledged_by,omitempty"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	ResolvedBy     string     `json:"resolved_by,omitempty"`
	Resolution     string     `json:"resolution,omitempty"`
}

type AlertStore struct {
	pool *pgxpool.Pool
}

func NewAlertStore(pool *pgxpool.Pool) *AlertStore {
	return &AlertStore{pool: pool}
}

const alertColumns = `
	a.id::text, a.ride_id::text,
	upper(right(replace(a.ride_id::text, '-', ''), 6)),
	COALESCE(a.user_id::text, ''),
	COALESCE(COALESCE(a.driver_id, r.driver_id)::text, ''),
	COALESCE(NULLIF(a.driver_name, ''), d.name, ''),
	COALESCE(NULLIF(a.driver_mobile, ''), d.mobile, ''),
	a.alert_type, a.status, a.severity,
	a.location_lat, a.location_lng, a.location_label, a.raised_at,
	a.acknowledged_at, COALESCE(a.acknowledged_by::text, ''),
	a.resolved_at, COALESCE(a.resolved_by::text, ''), a.resolution`

func scanAlert(row interface{ Scan(...interface{}) error }) (*Alert, error) {
	var alert Alert
	err := row.Scan(
		&alert.ID, &alert.RideID, &alert.RideRef, &alert.UserID,
		&alert.DriverID, &alert.DriverName, &alert.DriverMobile,
		&alert.AlertType, &alert.Status, &alert.Severity,
		&alert.LocationLat, &alert.LocationLng, &alert.LocationLabel, &alert.RaisedAt,
		&alert.AcknowledgedAt, &alert.AcknowledgedBy,
		&alert.ResolvedAt, &alert.ResolvedBy, &alert.Resolution,
	)
	return &alert, err
}

func nullableID(id string) interface{} {
	if ids.IsValid(id) {
		return id
	}
	return nil
}

func (s *AlertStore) Create(ctx context.Context, alert *Alert) error {
	if !ids.IsValid(alert.RideID) {
		return fmt.Errorf("invalid ride id: %s", alert.RideID)
	}
	if ids.IsZero(alert.ID) {
		alert.ID = ids.New()
	}
	if alert.Severity == "" {
		alert.Severity = "critical"
	}
	if alert.RaisedAt.IsZero() {
		alert.RaisedAt = time.Now()
	}
	const query = `INSERT INTO safety_alerts (
		id, ride_id, user_id, driver_id, alert_type, severity,
		driver_name, driver_mobile, location_lat, location_lng, location_label, raised_at
	) SELECT $1,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12
	WHERE EXISTS (SELECT 1 FROM rides WHERE id=$2::uuid AND status NOT IN ('COMPLETED','CANCELLED'))`
	_, err := s.pool.Exec(ctx, query,
		alert.ID, alert.RideID, nullableID(alert.UserID), nullableID(alert.DriverID),
		alert.AlertType, alert.Severity, alert.DriverName, alert.DriverMobile,
		alert.LocationLat, alert.LocationLng, alert.LocationLabel, alert.RaisedAt,
	)
	return err
}

func (s *AlertStore) List(ctx context.Context, limit int) ([]Alert, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `SELECT `+alertColumns+`
		FROM safety_alerts a
		JOIN rides r ON r.id=a.ride_id
		LEFT JOIN drivers d ON d.id=COALESCE(a.driver_id, r.driver_id)
		ORDER BY CASE a.status WHEN 'active' THEN 0 WHEN 'acknowledged' THEN 1 ELSE 2 END, a.raised_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	alerts := make([]Alert, 0)
	for rows.Next() {
		alert, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		alerts = append(alerts, *alert)
	}
	return alerts, rows.Err()
}

func (s *AlertStore) ListByRide(ctx context.Context, rideID string) ([]Alert, error) {
	if !ids.IsValid(rideID) {
		return []Alert{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+alertColumns+`
		FROM safety_alerts a
		JOIN rides r ON r.id=a.ride_id
		LEFT JOIN drivers d ON d.id=COALESCE(a.driver_id, r.driver_id)
		WHERE a.ride_id=$1::uuid
		ORDER BY a.raised_at ASC`, rideID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	alerts := make([]Alert, 0)
	for rows.Next() {
		alert, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		alerts = append(alerts, *alert)
	}
	return alerts, rows.Err()
}

func (s *AlertStore) FindByID(ctx context.Context, id string) (*Alert, error) {
	if !ids.IsValid(id) {
		return nil, fmt.Errorf("invalid alert id: %s", id)
	}
	return scanAlert(s.pool.QueryRow(ctx, `SELECT `+alertColumns+`
		FROM safety_alerts a
		JOIN rides r ON r.id=a.ride_id
		LEFT JOIN drivers d ON d.id=COALESCE(a.driver_id, r.driver_id)
		WHERE a.id=$1::uuid`, id))
}

func (s *AlertStore) Acknowledge(ctx context.Context, id, adminID string) (*Alert, error) {
	if !ids.IsValid(id) || !ids.IsValid(adminID) {
		return nil, ErrAlertNotActionable
	}
	tag, err := s.pool.Exec(ctx, `UPDATE safety_alerts
		SET status='acknowledged', acknowledged_at=now(), acknowledged_by=$2::uuid
		WHERE id=$1::uuid AND status='active'`, id, adminID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrAlertNotActionable
	}
	return s.FindByID(ctx, id)
}

func (s *AlertStore) Resolve(ctx context.Context, id, adminID, resolution string) (*Alert, error) {
	if !ids.IsValid(id) || !ids.IsValid(adminID) {
		return nil, ErrAlertNotActionable
	}
	tag, err := s.pool.Exec(ctx, `UPDATE safety_alerts
		SET status='resolved', resolved_at=now(), resolved_by=$2::uuid, resolution=$3
		WHERE id=$1::uuid AND status IN ('active','acknowledged')`, id, adminID, resolution)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrAlertNotActionable
	}
	return s.FindByID(ctx, id)
}

func (s *AlertStore) ResolveRide(ctx context.Context, rideID, resolution string) error {
	if !ids.IsValid(rideID) {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE safety_alerts
		SET status='resolved', resolved_at=now(), resolution=$2
		WHERE ride_id=$1::uuid AND status IN ('active','acknowledged')`, rideID, resolution)
	return err
}
