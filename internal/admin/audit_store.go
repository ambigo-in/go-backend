package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ambigo-backend/internal/ids"

	"github.com/jackc/pgx/v5/pgxpool"
)

type AuditStore struct {
	pool *pgxpool.Pool
}

func NewAuditStore(pool *pgxpool.Pool) *AuditStore {
	return &AuditStore{pool: pool}
}

type AuditEvent struct {
	ID        string    `db:"id" json:"id"`
	EventType string    `db:"event_type" json:"event_type"`
	Channel   string    `db:"channel" json:"channel"`
	Payload   string    `db:"payload" json:"payload"`
	RequestID string    `db:"request_id" json:"request_id,omitempty"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

func (s *AuditStore) InsertEvent(ctx context.Context, event *AuditEvent) error {
	if ids.IsZero(event.ID) {
		event.ID = ids.New()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	var requestID interface{}
	if event.RequestID == "" {
		requestID = nil
	} else {
		requestID = event.RequestID
	}
	const q = `INSERT INTO audit_log (id, event_type, channel, payload, request_id, created_at) VALUES ($1,$2,$3,$4::jsonb,$5,$6)`
	_, err := s.pool.Exec(ctx, q, event.ID, event.EventType, event.Channel, event.Payload, requestID, event.CreatedAt)
	return err
}

type ActivityEvent struct {
	ID        string          `json:"id"`
	EventType string          `json:"event_type"`
	Channel   string          `json:"channel"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

func (s *AuditStore) ListActivity(ctx context.Context, limit int) ([]ActivityEvent, error) {
	if limit < 1 || limit > 50 {
		limit = 20
	}
	channels := []string{
		"ride:requested", "ride:accepted", "ride:arrived", "ride:started",
		"ride:completed", "ride:cancelled", "ride:destination_changed",
		"ride:condition_updated", "safety:stopped_warning", "safety:stopped_emergency",
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, event_type, channel, payload, created_at
		FROM audit_log WHERE channel=ANY($1) ORDER BY created_at DESC LIMIT $2`, channels, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]ActivityEvent, 0)
	for rows.Next() {
		var event ActivityEvent
		if err := rows.Scan(&event.ID, &event.EventType, &event.Channel, &event.Payload, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *AuditStore) ListRideActivity(ctx context.Context, rideID string) ([]ActivityEvent, error) {
	if !ids.IsValid(rideID) {
		return []ActivityEvent{}, nil
	}
	channels := []string{
		"ride:requested", "ride:accepted", "ride:arrived", "ride:started",
		"ride:completed", "ride:cancelled", "ride:destination_changed",
		"ride:condition_updated", "safety:stopped_warning", "safety:stopped_emergency",
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, event_type, channel, payload, created_at
		FROM audit_log
		WHERE channel=ANY($1) AND payload->>'ride_id'=$2
		ORDER BY created_at ASC`, channels, rideID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]ActivityEvent, 0)
	for rows.Next() {
		var event ActivityEvent
		if err := rows.Scan(&event.ID, &event.EventType, &event.Channel, &event.Payload, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *AuditStore) CleanupOldLogs(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM audit_log WHERE created_at < now() - interval '30 days'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ---- Phase 4: Audit Log Read & Query Interface additions ----

type AuditLogFilter struct {
	Domain    string     `json:"domain"`
	Channels  []string   `json:"channels"`
	Search    string     `json:"search"`
	ActorID   string     `json:"actor_id"`
	TargetID  string     `json:"target_id"`
	RequestID string     `json:"request_id"`
	DateFrom  *time.Time `json:"date_from"`
	DateTo    *time.Time `json:"date_to"`
	Limit     int        `json:"limit"`
	Offset    int        `json:"offset"`
	SortOrder string     `json:"sort_order"`
}

type AuditLogRecord struct {
	ID        string          `json:"id"`
	EventType string          `json:"event_type"`
	Channel   string          `json:"channel"`
	Payload   json.RawMessage `json:"payload"`
	RequestID string          `json:"request_id"`
	CreatedAt time.Time       `json:"created_at"`
}

type AuditLogQueryResult struct {
	Items  []AuditLogRecord `json:"items"`
	Total  int64            `json:"total"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

type AuditDomainCounts struct {
	Admin    int64 `json:"admin"`
	Ride     int64 `json:"ride"`
	Auth     int64 `json:"auth"`
	Finance  int64 `json:"finance"`
	Safety   int64 `json:"safety"`
	Referral int64 `json:"referral"`
}

type AuditSummaryResult struct {
	TotalEvents   int64             `json:"total_events"`
	RetentionDays int               `json:"retention_days"`
	Domains       AuditDomainCounts `json:"domains"`
}

func DomainChannels(domain string) []string {
	switch strings.ToLower(domain) {
	case "admin":
		return []string{
			"admin:staff_created", "admin:staff_updated", "admin:staff_toggled", "admin:staff_deleted",
			"admin:hospital_added", "admin:hospital_updated", "admin:hospital_deleted",
			"admin:ambulance_type_created", "admin:ambulance_type_deleted",
			"admin:offer_created", "admin:offer_deleted",
			"admin:driver_rejected", "auth:driver_approved",
		}
	case "ride", "rides":
		return []string{
			"ride:requested", "ride:driver_offered", "ride:withdrawn", "ride:accepted",
			"ride:arrived", "ride:started", "ride:completed", "ride:cancelled",
			"ride:destination_changed", "ride:condition_updated",
		}
	case "auth":
		return []string{
			"auth:otp_requested", "auth:user_registered", "auth:user_logged_in",
			"auth:driver_created", "auth:driver_logged_in",
			"auth:session_replaced", "auth:logged_out",
		}
	case "finance":
		return []string{
			"payment:completed", "wallet:withdrawal",
		}
	case "safety":
		return []string{
			"safety:stopped_warning", "safety:stopped_emergency",
		}
	case "referral":
		return []string{
			"referral:credited",
		}
	default:
		return nil
	}
}

func ChannelDomain(channel string) string {
	switch {
	case channel == "auth:driver_approved" || strings.HasPrefix(channel, "admin:"):
		return "admin"
	case strings.HasPrefix(channel, "ride:"):
		return "ride"
	case strings.HasPrefix(channel, "auth:"):
		return "auth"
	case strings.HasPrefix(channel, "payment:") || strings.HasPrefix(channel, "wallet:"):
		return "finance"
	case strings.HasPrefix(channel, "safety:"):
		return "safety"
	case strings.HasPrefix(channel, "referral:"):
		return "referral"
	default:
		return "system"
	}
}

func (s *AuditStore) ListAuditLogs(ctx context.Context, filter AuditLogFilter) (*AuditLogQueryResult, error) {
	if s.pool == nil {
		return nil, errors.New("database pool is not initialized")
	}

	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	sortOrder := "DESC"
	if strings.ToLower(filter.SortOrder) == "asc" {
		sortOrder = "ASC"
	}

	var whereClauses []string
	var args []interface{}
	argIdx := 1

	if len(filter.Channels) > 0 {
		whereClauses = append(whereClauses, fmt.Sprintf("channel = ANY($%d)", argIdx))
		args = append(args, filter.Channels)
		argIdx++
	} else if filter.Domain != "" && strings.ToLower(filter.Domain) != "all" {
		domChannels := DomainChannels(filter.Domain)
		if len(domChannels) > 0 {
			whereClauses = append(whereClauses, fmt.Sprintf("channel = ANY($%d)", argIdx))
			args = append(args, domChannels)
			argIdx++
		}
	}

	if filter.DateFrom != nil && !filter.DateFrom.IsZero() {
		whereClauses = append(whereClauses, fmt.Sprintf("created_at >= $%d", argIdx))
		args = append(args, *filter.DateFrom)
		argIdx++
	}
	if filter.DateTo != nil && !filter.DateTo.IsZero() {
		whereClauses = append(whereClauses, fmt.Sprintf("created_at <= $%d", argIdx))
		args = append(args, *filter.DateTo)
		argIdx++
	}

	if filter.RequestID != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("request_id = $%d", argIdx))
		args = append(args, strings.TrimSpace(filter.RequestID))
		argIdx++
	}

	if filter.ActorID != "" {
		aid := strings.TrimSpace(filter.ActorID)
		whereClauses = append(whereClauses, fmt.Sprintf("(payload->>'actor_id' = $%d OR payload->>'admin_id' = $%d OR payload->>'user_id' = $%d OR payload->>'driver_id' = $%d)", argIdx, argIdx, argIdx, argIdx))
		args = append(args, aid)
		argIdx++
	}

	if filter.TargetID != "" {
		tid := strings.TrimSpace(filter.TargetID)
		whereClauses = append(whereClauses, fmt.Sprintf("(payload->>'target_id' = $%d OR payload->>'ride_id' = $%d OR payload->>'driver_id' = $%d OR payload->>'hospital_id' = $%d OR payload->>'amb_type_id' = $%d OR payload->>'offer_id' = $%d OR payload->>'admin_id' = $%d OR payload->>'user_id' = $%d)", argIdx, argIdx, argIdx, argIdx, argIdx, argIdx, argIdx, argIdx))
		args = append(args, tid)
		argIdx++
	}

	if filter.Search != "" {
		searchTerm := "%" + strings.TrimSpace(filter.Search) + "%"
		whereClauses = append(whereClauses, fmt.Sprintf("(event_type ILIKE $%d OR channel ILIKE $%d OR COALESCE(request_id,'') ILIKE $%d OR payload::text ILIKE $%d)", argIdx, argIdx, argIdx, argIdx))
		args = append(args, searchTerm)
		argIdx++
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = " WHERE " + strings.Join(whereClauses, " AND ")
	}

	countQuery := "SELECT COUNT(*) FROM audit_log" + whereSQL
	var total int64
	if err := s.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, err
	}

	dataQuery := fmt.Sprintf("SELECT id::text, event_type, channel, payload, COALESCE(request_id, ''), created_at FROM audit_log%s ORDER BY created_at %s LIMIT $%d OFFSET $%d", whereSQL, sortOrder, argIdx, argIdx+1)
	dataArgs := append(args, limit, offset)

	rows, err := s.pool.Query(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]AuditLogRecord, 0, limit)
	for rows.Next() {
		var item AuditLogRecord
		if err := rows.Scan(&item.ID, &item.EventType, &item.Channel, &item.Payload, &item.RequestID, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}

	return &AuditLogQueryResult{
		Items:  items,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	}, rows.Err()
}

func (s *AuditStore) GetAuditSummary(ctx context.Context, dateFrom, dateTo *time.Time) (*AuditSummaryResult, error) {
	if s.pool == nil {
		return nil, errors.New("database pool is not initialized")
	}

	from := time.Now().Add(-30 * 24 * time.Hour)
	if dateFrom != nil && !dateFrom.IsZero() {
		from = *dateFrom
	}

	query := `SELECT 
		COUNT(*),
		COUNT(*) FILTER (WHERE channel = 'auth:driver_approved' OR channel LIKE 'admin:%') AS admin_count,
		COUNT(*) FILTER (WHERE channel LIKE 'ride:%') AS ride_count,
		COUNT(*) FILTER (WHERE channel LIKE 'auth:%' AND channel != 'auth:driver_approved') AS auth_count,
		COUNT(*) FILTER (WHERE channel LIKE 'payment:%' OR channel LIKE 'wallet:%') AS finance_count,
		COUNT(*) FILTER (WHERE channel LIKE 'safety:%') AS safety_count,
		COUNT(*) FILTER (WHERE channel LIKE 'referral:%') AS referral_count
	FROM audit_log
	WHERE created_at >= $1`

	args := []interface{}{from}
	if dateTo != nil && !dateTo.IsZero() {
		query += " AND created_at <= $2"
		args = append(args, *dateTo)
	}

	var total, adminCount, rideCount, authCount, financeCount, safetyCount, referralCount int64
	err := s.pool.QueryRow(ctx, query, args...).Scan(
		&total,
		&adminCount,
		&rideCount,
		&authCount,
		&financeCount,
		&safetyCount,
		&referralCount,
	)
	if err != nil {
		return nil, err
	}

	return &AuditSummaryResult{
		TotalEvents:   total,
		RetentionDays: 30,
		Domains: AuditDomainCounts{
			Admin:    adminCount,
			Ride:     rideCount,
			Auth:     authCount,
			Finance:  financeCount,
			Safety:   safetyCount,
			Referral: referralCount,
		},
	}, nil
}

