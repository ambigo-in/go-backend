package eventbus

import (
	"context"
	"log"
	"time"

	"ambigo-backend/internal/admin"
)

// CacheInvalidator listens to admin data change events and increments counters.
type CacheInvalidator struct {
	counterStore *admin.CounterStore
}

func NewCacheInvalidator(counterStore *admin.CounterStore) *CacheInvalidator {
	return &CacheInvalidator{counterStore: counterStore}
}

func (i *CacheInvalidator) SubscribeTo(bus *InMemoryBus) {
	bus.Subscribe(ChannelAdminAmbTypeCreated, i.handleAmbTypeChange)
	bus.Subscribe(ChannelAdminAmbTypeDeleted, i.handleAmbTypeChange)
	bus.Subscribe(ChannelAdminHospitalAdded, i.handleHospitalChange)
	bus.Subscribe(ChannelAdminHospitalUpdated, i.handleHospitalChange)
	bus.Subscribe(ChannelAdminHospitalDeleted, i.handleHospitalChange)
}

func (i *CacheInvalidator) handleAmbTypeChange(payload []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := i.counterStore.IncrementCounter(ctx, "ambulance_type"); err != nil {
		log.Printf("[CacheInvalidator] Failed to increment ambulance_type counter: %v", err)
	}
}

func (i *CacheInvalidator) handleHospitalChange(payload []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := i.counterStore.IncrementCounter(ctx, "hospitals"); err != nil {
		log.Printf("[CacheInvalidator] Failed to increment hospitals counter: %v", err)
	}
}
