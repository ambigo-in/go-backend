package eventbus

import (
	"context"
	"encoding/json"
	"time"

	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/logger"
)

type AuditLogger struct {
	auditStore *admin.AuditStore
}

func NewAuditLogger(auditStore *admin.AuditStore) *AuditLogger {
	return &AuditLogger{
		auditStore: auditStore,
	}
}

func (l *AuditLogger) SubscribeTo(bus *InMemoryBus) {
	// Excludes driver:location_updated by design: ~1 ping per 3s per driver
	// would flood the 30-day audit_log. Location lives in the H3 store.
	channels := []string{
		ChannelRideRequested, ChannelRideDriverOffered, ChannelRideWithdrawn,
		ChannelRideAccepted, ChannelRideArrived, ChannelRideStarted,
		ChannelRideCompleted, ChannelRideCancelled,
		ChannelRideDestinationChanged, ChannelRideConditionUpdated,
		ChannelAuthOTPRequested, ChannelAuthUserRegistered, ChannelAuthUserLoggedIn,
		ChannelAuthDriverCreated, ChannelAuthDriverLoggedIn, ChannelAuthDriverApproved,
		ChannelAuthSessionReplaced, ChannelAuthLoggedOut,
		ChannelPaymentCompleted, ChannelWalletWithdrawal,
		ChannelAdminAmbTypeCreated, ChannelAdminAmbTypeDeleted,
		ChannelAdminHospitalAdded, ChannelAdminHospitalUpdated, ChannelAdminHospitalDeleted,
		ChannelAdminOfferCreated, ChannelAdminOfferDeleted,
		ChannelAdminDriverRejected,
		ChannelAdminStaffCreated, ChannelAdminStaffUpdated,
		ChannelAdminStaffToggled, ChannelAdminStaffDeleted,
		ChannelReferralCredited,
		ChannelSafetyStoppedWarn, ChannelSafetyStoppedAlarm,
	}
	for _, ch := range channels {
		bus.Subscribe(ch, l.handleEvent)
	}
}

func (l *AuditLogger) handleEvent(payload []byte) {
	var raw map[string]interface{}
	_ = json.Unmarshal(payload, &raw)

	channel := ""
	if ch, ok := raw["_channel"].(string); ok {
		channel = ch
	}
	eventType := CanonicalEventName(channel)

	requestID := ""
	if rid, ok := raw["_request_id"].(string); ok {
		requestID = rid
	}

	// Event time comes from the publish envelope so subscriber lag never
	// skews created_at; InsertEvent falls back to now() when absent.
	var createdAt time.Time
	if ts, ok := raw["_ts"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			createdAt = t
		}
	}

	evt := logger.Log.Info().Str("channel", "audit")
	if eventType != "" {
		evt = evt.Str("event", eventType)
	}
	if channel != "" {
		evt = evt.Str("event_channel", channel)
	}
	if requestID != "" {
		evt = evt.Str("request_id", requestID)
	}
	evt.RawJSON("payload", payload).Msg("audit event")

	if l.auditStore != nil {
		auditEvent := &admin.AuditEvent{
			EventType: eventType, // canonical underscore name (ride_completed)
			Channel:   channel,   // bus channel (ride:completed)
			Payload:   string(payload),
			RequestID: requestID,
			CreatedAt: createdAt,
		}
		if err := l.auditStore.InsertEvent(context.Background(), auditEvent); err != nil {
			logger.Log.Error().Err(err).Msg("Failed to persist audit event")
		}
	}
}
