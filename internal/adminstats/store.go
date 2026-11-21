package adminstats

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Day boundaries are IST: the service operates in India and Cloud Run runs in UTC.
const tz = "Asia/Kolkata"

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

type ActiveRides struct {
	Searching  int64 `json:"searching"`
	Assigned   int64 `json:"assigned"`
	Arrived    int64 `json:"arrived"`
	InProgress int64 `json:"in_progress"`
	Total      int64 `json:"total"`
	SOS        int64 `json:"sos"`
}

type DayTotals struct {
	Rides     int64   `json:"rides"`
	Completed int64   `json:"completed"`
	Cancelled int64   `json:"cancelled"`
	Revenue   float64 `json:"revenue"`
}

type Rating struct {
	Average *float64 `json:"average"`
	Count   int64    `json:"count"`
}

type Snapshot struct {
	Active    ActiveRides `json:"active"`
	Today     DayTotals   `json:"today"`
	Yesterday DayTotals   `json:"yesterday"`
	Rating30d Rating      `json:"rating_30d"`
}

// Snapshot returns rides in flight right now plus today's and yesterday's totals.
func (s *Store) Snapshot(ctx context.Context, at time.Time) (*Snapshot, error) {
	var out Snapshot
	err := s.pool.QueryRow(ctx, `
		WITH b AS (SELECT date_trunc('day', $1::timestamptz AT TIME ZONE '`+tz+`') AT TIME ZONE '`+tz+`' AS today)
		SELECT
			count(*) FILTER (WHERE status='SEARCHING'),
			count(*) FILTER (WHERE status='ASSIGNED'),
			count(*) FILTER (WHERE status='ARRIVED'),
			count(*) FILTER (WHERE status='IN_PROGRESS'),
			count(*) FILTER (WHERE status IN ('SEARCHING','ASSIGNED','ARRIVED','IN_PROGRESS') AND emergency_priority > 0),
			count(*) FILTER (WHERE created_at >= b.today),
			count(*) FILTER (WHERE completed_at >= b.today AND status='COMPLETED'),
			count(*) FILTER (WHERE cancelled_at >= b.today AND status='CANCELLED'),
			coalesce(sum(fare_total) FILTER (WHERE completed_at >= b.today AND status='COMPLETED'), 0)::float8,
			count(*) FILTER (WHERE created_at >= b.today - interval '1 day' AND created_at < b.today),
			count(*) FILTER (WHERE completed_at >= b.today - interval '1 day' AND completed_at < b.today AND status='COMPLETED'),
			count(*) FILTER (WHERE cancelled_at >= b.today - interval '1 day' AND cancelled_at < b.today AND status='CANCELLED'),
			coalesce(sum(fare_total) FILTER (WHERE completed_at >= b.today - interval '1 day' AND completed_at < b.today AND status='COMPLETED'), 0)::float8
		FROM rides, b
		WHERE status IN ('SEARCHING','ASSIGNED','ARRIVED','IN_PROGRESS')
		   OR created_at >= b.today - interval '1 day'
		   OR completed_at >= b.today - interval '1 day'
		   OR cancelled_at >= b.today - interval '1 day'`, at,
	).Scan(
		&out.Active.Searching, &out.Active.Assigned, &out.Active.Arrived, &out.Active.InProgress, &out.Active.SOS,
		&out.Today.Rides, &out.Today.Completed, &out.Today.Cancelled, &out.Today.Revenue,
		&out.Yesterday.Rides, &out.Yesterday.Completed, &out.Yesterday.Cancelled, &out.Yesterday.Revenue,
	)
	if err != nil {
		return nil, err
	}
	out.Active.Total = out.Active.Searching + out.Active.Assigned + out.Active.Arrived + out.Active.InProgress

	if err := s.pool.QueryRow(ctx,
		`SELECT count(*), avg(rating)::float8 FROM feedback WHERE created_at >= $1::timestamptz - interval '30 days'`, at,
	).Scan(&out.Rating30d.Count, &out.Rating30d.Average); err != nil {
		return nil, err
	}
	return &out, nil
}

type DailyPoint struct {
	Date      string  `json:"date"`
	Rides     int64   `json:"rides"`
	Completed int64   `json:"completed"`
	Cancelled int64   `json:"cancelled"`
	Revenue   float64 `json:"revenue"`
}

type PeriodTotals struct {
	Rides             int64    `json:"rides"`
	Completed         int64    `json:"completed"`
	Cancelled         int64    `json:"cancelled"`
	Revenue           float64  `json:"revenue"`
	SOS               int64    `json:"sos"`
	AvgAssignSeconds  *float64 `json:"avg_assign_seconds"`
	AvgTripMinutes    *float64 `json:"avg_trip_minutes"`
	CompletionRatePct *float64 `json:"completion_rate_pct"`
	AvgFareCompleted  *float64 `json:"avg_fare_completed"`
}

type Count struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

type Analytics struct {
	Days          int          `json:"days"`
	Daily         []DailyPoint `json:"daily"`
	PrevDaily     []DailyPoint `json:"prev_daily"`
	Totals        PeriodTotals `json:"totals"`
	PrevTotals    PeriodTotals `json:"prev_totals"`
	ByHour        []int64      `json:"by_hour"`
	ByCondition   []Count      `json:"by_condition"`
	ByType        []Count      `json:"by_type"`
	ByStatus      []Count      `json:"by_status"`
	CancelReasons []Count      `json:"cancel_reasons"`
}

// Analytics covers the last `days` IST days including today, and the same-length
// period immediately before it for comparison.
func (s *Store) Analytics(ctx context.Context, days int, at time.Time) (*Analytics, error) {
	out := &Analytics{Days: days, ByHour: make([]int64, 24)}
	window := `b AS (SELECT date_trunc('day', $2::timestamptz AT TIME ZONE '` + tz + `') - make_interval(days => $1 - 1) AS cur_local)`

	rows, err := s.pool.Query(ctx, `
		WITH `+window+`,
		r AS (
			SELECT (created_at AT TIME ZONE '`+tz+`')::date AS day, status, fare_total
			FROM rides, b
			WHERE created_at >= (b.cur_local - make_interval(days => $1)) AT TIME ZONE '`+tz+`'
		)
		SELECT d::date::text,
			count(r.day),
			count(r.day) FILTER (WHERE r.status='COMPLETED'),
			count(r.day) FILTER (WHERE r.status='CANCELLED'),
			coalesce(sum(r.fare_total) FILTER (WHERE r.status='COMPLETED'), 0)::float8
		FROM b, generate_series(b.cur_local - make_interval(days => $1), b.cur_local + make_interval(days => $1 - 1), interval '1 day') d
		LEFT JOIN r ON r.day = d::date
		GROUP BY d ORDER BY d`, days, at)
	if err != nil {
		return nil, err
	}
	var all []DailyPoint
	for rows.Next() {
		var p DailyPoint
		if err := rows.Scan(&p.Date, &p.Rides, &p.Completed, &p.Cancelled, &p.Revenue); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(all) == 2*days {
		out.PrevDaily, out.Daily = all[:days], all[days:]
	} else {
		out.Daily = all
	}

	rows, err = s.pool.Query(ctx, `
		WITH `+window+`
		SELECT created_at >= b.cur_local AT TIME ZONE '`+tz+`',
			count(*),
			count(*) FILTER (WHERE status='COMPLETED'),
			count(*) FILTER (WHERE status='CANCELLED'),
			coalesce(sum(fare_total) FILTER (WHERE status='COMPLETED'), 0)::float8,
			count(*) FILTER (WHERE emergency_priority > 0),
			avg(extract(epoch FROM assigned_at - created_at)) FILTER (WHERE assigned_at IS NOT NULL AND assigned_at >= created_at)::float8,
			avg(extract(epoch FROM completed_at - started_at) / 60) FILTER (WHERE status='COMPLETED' AND started_at IS NOT NULL AND completed_at >= started_at)::float8
		FROM rides, b
		WHERE created_at >= (b.cur_local - make_interval(days => $1)) AT TIME ZONE '`+tz+`'
		GROUP BY 1`, days, at)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var current bool
		var t PeriodTotals
		if err := rows.Scan(&current, &t.Rides, &t.Completed, &t.Cancelled, &t.Revenue, &t.SOS, &t.AvgAssignSeconds, &t.AvgTripMinutes); err != nil {
			rows.Close()
			return nil, err
		}
		finished := t.Completed + t.Cancelled
		if finished > 0 {
			v := float64(t.Completed) * 100 / float64(finished)
			t.CompletionRatePct = &v
		}
		if t.Completed > 0 {
			v := t.Revenue / float64(t.Completed)
			t.AvgFareCompleted = &v
		}
		if current {
			out.Totals = t
		} else {
			out.PrevTotals = t
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.pool.Query(ctx, `
		WITH `+window+`,
		r AS (SELECT rides.* FROM rides, b WHERE created_at >= b.cur_local AT TIME ZONE '`+tz+`')
		SELECT * FROM (
		SELECT 'hour', extract(hour FROM created_at AT TIME ZONE '`+tz+`')::int::text, count(*) FROM r GROUP BY 2
		UNION ALL
		SELECT 'condition', coalesce(nullif(latest_condition->>'level', ''), 'unrecorded'), count(*) FROM r GROUP BY 2
		UNION ALL
		SELECT 'type', coalesce(nullif(trim(a.name), ''), 'Unknown'), count(*) FROM r LEFT JOIN ambulance_types a ON a.id = r.amb_type_id GROUP BY 2
		UNION ALL
		SELECT 'status', status, count(*) FROM r GROUP BY 2
		UNION ALL
		SELECT 'cancel', coalesce(nullif(cancellation_reason, ''), 'unspecified'), count(*) FROM r WHERE status='CANCELLED' GROUP BY 2
		) u ORDER BY 1, 3 DESC, 2`, days, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out.ByCondition, out.ByType, out.ByStatus, out.CancelReasons = []Count{}, []Count{}, []Count{}, []Count{}
	for rows.Next() {
		var dim, label string
		var n int64
		if err := rows.Scan(&dim, &label, &n); err != nil {
			return nil, err
		}
		switch dim {
		case "hour":
			if h, err := strconv.Atoi(label); err == nil && h >= 0 && h < 24 {
				out.ByHour[h] = n
			}
		case "condition":
			out.ByCondition = append(out.ByCondition, Count{label, n})
		case "type":
			out.ByType = append(out.ByType, Count{label, n})
		case "status":
			out.ByStatus = append(out.ByStatus, Count{label, n})
		case "cancel":
			out.CancelReasons = append(out.CancelReasons, Count{label, n})
		}
	}
	return out, rows.Err()
}
