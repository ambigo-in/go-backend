package eventbus

import (
	"encoding/json"
	"log"
)

// AnalyticsTracker listens to ride and auth events for business analytics.
// Currently a stub — wire to your analytics backend (BigQuery, Kafka, etc.).
type AnalyticsTracker struct{}

func NewAnalyticsTracker() *AnalyticsTracker {
	return &AnalyticsTracker{}
}

func (a *AnalyticsTracker) SubscribeTo(bus *InMemoryBus) {
	bus.Subscribe(ChannelRideRequested, a.handleRideRequested)
	bus.Subscribe(ChannelRideCompleted, a.handleRideCompleted)
	bus.Subscribe(ChannelAuthUserRegistered, a.handleUserRegistered)
}

func (a *AnalyticsTracker) handleRideRequested(payload []byte) {
	var p RideRequestedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		log.Printf("[Analytics] Unmarshal error (ride:requested): %v", err)
		return
	}
	// TODO: Write to analytics pipeline
	log.Printf("[Analytics] Ride requested: %s (SOS=%v, mode=%s)", p.RideID, p.IsSOS, p.PaymentMode)
}

func (a *AnalyticsTracker) handleRideCompleted(payload []byte) {
	var p RideCompletedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		log.Printf("[Analytics] Unmarshal error (ride:completed): %v", err)
		return
	}
	log.Printf("[Analytics] Ride completed: %s amount=%.2f mode=%s", p.RideID, p.FinalAmount, p.PaymentMode)
}

func (a *AnalyticsTracker) handleUserRegistered(payload []byte) {
	var p AuthUserRegisteredPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		log.Printf("[Analytics] Unmarshal error (auth:user_registered): %v", err)
		return
	}
	log.Printf("[Analytics] User registered: %s", p.UserID)
}
