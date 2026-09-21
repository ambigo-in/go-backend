package payment

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ErrDuplicateEvent is returned by ClaimEvent when the event was already
// claimed by an earlier delivery (webhook retry). Callers must treat it as
// idempotent success: acknowledge without re-applying any business effect.
var ErrDuplicateEvent = errors.New("event already processed")

// ClaimEvent atomically records an inbound provider event id. The UNIQUE
// primary key on processed_events.event_id makes the check-and-insert a
// single statement — no check-then-act race between concurrent deliveries.
// Must be called inside the same transaction as the business effect so a
// crash cannot record the claim without the work (or vice versa).
func ClaimEvent(ctx context.Context, db DBTX, eventID string, eventType string, payload []byte) error {
	if eventID == "" {
		return errors.New("event id is required")
	}
	var raw []byte
	if len(payload) > 0 {
		// Normalize: store compact JSON when possible, raw text otherwise.
		var v interface{}
		if json.Unmarshal(payload, &v) == nil {
			if c, err := json.Marshal(v); err == nil {
				raw = c
			} else {
				raw = payload
			}
		} else {
			raw = payload
		}
	} else {
		raw = []byte("{}")
	}
	tag, err := db.Exec(ctx,
		`INSERT INTO processed_events (event_id, event_type, payload) VALUES ($1,$2,$3::jsonb) ON CONFLICT (event_id) DO NOTHING`,
		eventID, eventType, string(raw),
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrDuplicateEvent
	}
	return nil
}

// ClaimEventTx is ClaimEvent bound to an in-flight pgx transaction.
func ClaimEventTx(ctx context.Context, tx pgx.Tx, eventID string, eventType string, payload []byte) error {
	return ClaimEvent(ctx, tx, eventID, eventType, payload)
}
