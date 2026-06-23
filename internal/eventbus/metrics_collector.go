package eventbus

import (
	"encoding/json"
	"log"

	"ambigo-backend/internal/metrics"
)

// MetricsCollector listens to domain events and records Prometheus metrics.
type MetricsCollector struct{}

func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{}
}

func (c *MetricsCollector) SubscribeTo(bus *InMemoryBus) {
	bus.Subscribe(ChannelAuthOTPRequested, c.handleOTPRequested)
	bus.Subscribe(ChannelRideRequested, c.handleRideRequested)
	bus.Subscribe(ChannelRideAccepted, c.handleRideAccepted)
	bus.Subscribe(ChannelRideCompleted, c.handleRideCompleted)
	bus.Subscribe(ChannelRideCancelled, c.handleRideCancelled)
}

func (c *MetricsCollector) handleOTPRequested(payload []byte) {
	metrics.OtpRequestsTotal.Inc()
}

func (c *MetricsCollector) handleRideRequested(payload []byte) {
	metrics.RideRequestsTotal.Inc()
}

func (c *MetricsCollector) handleRideAccepted(payload []byte) {
	metrics.RidesAssignedTotal.Inc()
}

func (c *MetricsCollector) handleRideCompleted(payload []byte) {
	metrics.RidesCompletedTotal.Inc()
}

func (c *MetricsCollector) handleRideCancelled(payload []byte) {
	var p RideCancelledPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		log.Printf("[MetricsCollector] Unmarshal error (ride:cancelled): %v", err)
		return
	}
	metrics.RidesCancelledTotal.Inc()
}
