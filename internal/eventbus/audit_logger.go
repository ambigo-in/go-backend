package eventbus

import (
	"context"
	"encoding/json"

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
	channels := []string{
		ChannelRideRequested, ChannelRideDriverOffered,
		ChannelRideAccepted, ChannelRideArrived, ChannelRideStarted,
		ChannelRideCompleted, ChannelRideCancelled,
		ChannelAuthOTPRequested, ChannelAuthUserRegistered, ChannelAuthUserLoggedIn,
		ChannelAuthDriverCreated, ChannelAuthDriverLoggedIn, ChannelAuthDriverApproved,
		ChannelPaymentCompleted, ChannelWalletWithdrawal,
		ChannelDriverLocationUpdate,
		ChannelAdminAmbTypeCreated, ChannelAdminAmbTypeDeleted,
		ChannelAdminHospitalAdded, ChannelAdminHospitalUpdated, ChannelAdminHospitalDeleted,
		ChannelAdminOfferCreated, ChannelAdminOfferDeleted,
		ChannelAdminDriverRejected,
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

	requestID := ""
	if rid, ok := raw["request_id"].(string); ok {
		requestID = rid
	}

	evt := logger.Log.Info().Str("channel", "audit")
	if channel != "" {
		evt = evt.Str("event_channel", channel)
	}
	if requestID != "" {
		evt = evt.Str("request_id", requestID)
	}
	evt.RawJSON("payload", payload).Msg("audit event")

	// V16: Persist to MongoDB
	if l.auditStore != nil {
		auditEvent := &admin.AuditEvent{
			EventType: channel,
			Channel:   channel,
			Payload:   string(payload),
			RequestID: requestID,
		}
		if err := l.auditStore.InsertEvent(context.Background(), auditEvent); err != nil {
			logger.Log.Error().Err(err).Msg("Failed to persist audit event")
		}
	}
}
