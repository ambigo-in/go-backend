package dispatch

import (
	"context"
	"log"
	"sync"
	"time"

	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/metrics"
	"ambigo-backend/internal/ride"
	"ambigo-backend/internal/websocket"
)

type Dispatcher struct {
	Matcher   *Matcher
	RideStore *ride.Store
	EventBus  *eventbus.InMemoryBus
	WSManager *websocket.Manager // kept only for DeclineHandler callback

	acceptChannels  map[string]chan string
	declineChannels map[string]chan string
	mu              sync.RWMutex
}

func NewDispatcher(matcher *Matcher, rideStore *ride.Store, eventBus *eventbus.InMemoryBus, wsManager *websocket.Manager) *Dispatcher {
	d := &Dispatcher{
		Matcher:         matcher,
		RideStore:       rideStore,
		EventBus:        eventBus,
		WSManager:       wsManager,
		acceptChannels:  make(map[string]chan string),
		declineChannels: make(map[string]chan string),
	}
	wsManager.DeclineHandler = d
	return d
}

// StartStaleRideCleanup runs every 60s and cancels SEARCHING rides older than 5 minutes.
func (d *Dispatcher) StartStaleRideCleanup() {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			count, err := d.RideStore.CancelStaleSearchingRides(ctx, 5*time.Minute)
			cancel()
			if err != nil {
				log.Printf("[Dispatcher] Stale ride cleanup error: %v", err)
			} else if count > 0 {
				log.Printf("[Dispatcher] Cancelled %d stale searching rides", count)
			}
		}
	}()
}

// RequestRide starts the matching loop for a new ride
func (d *Dispatcher) RequestRide(r *ride.Ride) error {
	ctx := context.Background()

	if err := d.RideStore.CreateRide(ctx, r); err != nil {
		return err
	}

	rideID := r.ID.Hex()

	// Create channels for this ride
	d.mu.Lock()
	d.acceptChannels[rideID] = make(chan string)
	d.declineChannels[rideID] = make(chan string)
	d.mu.Unlock()

	// Publish ride requested event (triggers metrics, analytics, etc.)
	pickupLng, pickupLat := r.Pickup.Coordinates[0], r.Pickup.Coordinates[1]
	dropoffLng, dropoffLat := r.Drop.Coordinates[0], r.Drop.Coordinates[1]
	ambTypeID := ""
	if r.AmbTypeID != nil {
		ambTypeID = *r.AmbTypeID
	}
	hospitalID := ""
	if r.HospitalID != nil {
		hospitalID = *r.HospitalID
	}
	fare := 0.0
	driverShare := 0.0
	distance := 0.0
	if r.Fare != nil {
		fare = r.Fare.Total
		driverShare = r.Fare.DriverShare
	}
	if r.Route != nil {
		distance = r.Route.DistanceKm
	}
	d.EventBus.PublishEvent(eventbus.ChannelRideRequested, eventbus.RideRequestedPayload{
		RideID:        rideID,
		UserID:        r.UserID,
		PickupLat:     pickupLat,
		PickupLng:     pickupLng,
		PickupAddress: r.PickupAddress,
		DropoffLat:    dropoffLat,
		DropoffLng:    dropoffLng,
		DropAddress:   r.DropAddress,
		AmbTypeID:     ambTypeID,
		HospitalID:    hospitalID,
		PaymentMode:   r.PaymentMode,
		IsSOS:         r.EmergencyPriority > 0,
		Fare:          fare,
		DriverShare:   driverShare,
		DistanceKm:    distance,
	})

	go d.startMatchingLoop(r)

	return nil
}

// HandleDriverAccept is called by the REST API when a driver clicks "Accept"
func (d *Dispatcher) HandleDriverAccept(ctx context.Context, rideID string, driverID string) error {
	// 1. Atomic Database Assignment
	err := d.RideStore.AtomicAssignDriver(ctx, rideID, driverID)
	if err != nil {
		return err // Ride was already taken or cancelled
	}

	// 2. Signal the waiting matching loop to stop
	d.mu.RLock()
	ch, exists := d.acceptChannels[rideID]
	d.mu.RUnlock()
	if exists {
		ch <- driverID
	}

	// 3. Publish accepted event (subscribers handle WS, metrics, etc.)
	rideData, _ := d.RideStore.GetRideByID(ctx, rideID)
	if rideData != nil {
		d.EventBus.PublishEvent(eventbus.ChannelRideAccepted, eventbus.RideAcceptedPayload{
			RideID:   rideID,
			DriverID: driverID,
			UserID:   rideData.UserID,
			Status:   string(ride.StatusAssigned),
		})
	}

	return nil
}

// HandleDriverDecline implements websocket.DeclineHandler.
func (d *Dispatcher) HandleDriverDecline(ctx context.Context, rideID, driverID string) {
	d.mu.RLock()
	ch, exists := d.declineChannels[rideID]
	d.mu.RUnlock()
	if exists {
		ch <- driverID
	}
}

// persistDispatchMetadata updates the ride's dispatch_metadata in MongoDB.
func (d *Dispatcher) persistDispatchMetadata(rideID string, meta *ride.DispatchMetadata) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d.RideStore.UpdateDispatchMetadata(ctx, rideID, meta)
}

func (d *Dispatcher) startMatchingLoop(r *ride.Ride) {
	rideIDStr := r.ID.Hex()
	log.Printf("[Dispatcher] Starting matching loop for Ride %s", rideIDStr)

	defer func() {
		d.mu.Lock()
		delete(d.acceptChannels, rideIDStr)
		delete(d.declineChannels, rideIDStr)
		d.mu.Unlock()
	}()

	pickupLng, pickupLat := r.Pickup.Coordinates[0], r.Pickup.Coordinates[1]

	ambTypeID := ""
	if r.AmbTypeID != nil {
		ambTypeID = *r.AmbTypeID
	}
	candidates, err := d.Matcher.FindBestDrivers(context.Background(), pickupLat, pickupLng, 5, ambTypeID)
	if err != nil || len(candidates) == 0 {
		log.Printf("[Dispatcher] No drivers found for Ride %s. Cancelling.", rideIDStr)
		d.RideStore.UpdateRideStatus(context.Background(), rideIDStr, ride.StatusSearching, ride.StatusCancelled)
		d.EventBus.PublishEvent(eventbus.ChannelRideCancelled, eventbus.RideCancelledPayload{
			RideID: rideIDStr,
			Reason: "no_drivers",
			UserID: r.UserID,
		})
		return
	}

	startTime := time.Now()
	r.DispatchMetadata.CandidatesSearched = len(candidates)

	for i, candidate := range candidates {
		log.Printf("[Dispatcher] Ride %s: Offering to Driver %s (ETA: %ds) [Candidate %d/%d]",
			rideIDStr, candidate.DriverID, candidate.ETASeconds, i+1, len(candidates))

		fareVal := 0.0
		driverShareVal := 0.0
		if r.Fare != nil {
			fareVal = r.Fare.Total
			driverShareVal = r.Fare.DriverShare
		}

		r.DispatchMetadata.OffersSent++

		d.EventBus.PublishEvent(eventbus.ChannelRideDriverOffered, eventbus.RideDriverOfferedPayload{
			RideID:      rideIDStr,
			DriverID:    candidate.DriverID,
			ETASeconds:  candidate.ETASeconds,
			DistanceKm:  candidate.DistanceKm,
			Fare:        fareVal,
			DriverShare: driverShareVal,
			IsSOS:       r.EmergencyPriority > 0,
		})

		d.mu.RLock()
		acceptCh := d.acceptChannels[rideIDStr]
		declineCh := d.declineChannels[rideIDStr]
		d.mu.RUnlock()

		select {
		case acceptedDriverID := <-acceptCh:
			if acceptedDriverID == candidate.DriverID {
				r.DispatchMetadata.AssignmentLatencyMs = int(time.Since(startTime).Milliseconds())
				log.Printf("[Dispatcher] Ride %s: Driver %s accepted the ride!", rideIDStr, candidate.DriverID)
				metrics.ObserveDispatchLatency(time.Since(startTime))
				d.persistDispatchMetadata(rideIDStr, &r.DispatchMetadata)
				return
			}
		case declinedDriverID := <-declineCh:
			if declinedDriverID == candidate.DriverID {
				r.DispatchMetadata.OffersDeclined++
				log.Printf("[Dispatcher] Ride %s: Driver %s declined. Moving to next.", rideIDStr, candidate.DriverID)
			}
		case <-time.After(30 * time.Second):
			r.DispatchMetadata.OffersTimedOut++
			log.Printf("[Dispatcher] Ride %s: Driver %s timed out. Moving to next.", rideIDStr, candidate.DriverID)
		}
	}

	r.DispatchMetadata.AssignmentLatencyMs = int(time.Since(startTime).Milliseconds())
	d.persistDispatchMetadata(rideIDStr, &r.DispatchMetadata)

	log.Printf("[Dispatcher] Ride %s: All candidates exhausted. Cancelling.", rideIDStr)
	d.RideStore.UpdateRideStatus(context.Background(), rideIDStr, ride.StatusSearching, ride.StatusCancelled)

	d.EventBus.PublishEvent(eventbus.ChannelRideCancelled, eventbus.RideCancelledPayload{
		RideID: rideIDStr,
		Reason: "all_drivers_exhausted",
		UserID: r.UserID,
	})
}
