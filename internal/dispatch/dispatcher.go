package dispatch

import (
	"context"
	"errors"
	"sync"
	"time"

	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/metrics"
	"ambigo-backend/internal/requestid"
	"ambigo-backend/internal/ride"
	"ambigo-backend/internal/websocket"
)

type Dispatcher struct {
	Matcher   *Matcher
	RideStore *ride.Store
	EventBus  *eventbus.InMemoryBus
	WSManager *websocket.Manager

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
				logger.Log.Error().Err(err).Msg("Stale ride cleanup error")
			} else if count > 0 {
				logger.Log.Info().Int64("count", count).Msg("Cancelled stale searching rides")
			}
		}
	}()
}

// RequestRide starts the matching loop for a new ride
func (d *Dispatcher) RequestRide(ctx context.Context, r *ride.Ride) error {
	reqID := requestid.FromContext(ctx)

	if err := d.RideStore.CreateRide(ctx, r); err != nil {
		return err
	}

	rideID := r.ID

	d.mu.Lock()
	d.acceptChannels[rideID] = make(chan string, 1)
	d.declineChannels[rideID] = make(chan string, 1)
	d.mu.Unlock()

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
		RequestID:     reqID,
	})

	go d.startMatchingLoop(r, reqID)

	return nil
}

// HandleDriverAccept is called by the REST API when a driver clicks "Accept"
func (d *Dispatcher) HandleDriverAccept(ctx context.Context, rideID string, driverID string) error {
	reqID := requestid.FromContext(ctx)

	err := d.RideStore.AtomicAssignDriver(ctx, rideID, driverID)
	if err != nil {
		return err
	}

	// Immediately mark driver as BUSY in location store to prevent double-booking
	if d.WSManager != nil && d.WSManager.LocStore != nil {
		d.WSManager.LocStore.SetDriverStatus(driverID, "BUSY")
	}

	d.mu.RLock()
	ch, exists := d.acceptChannels[rideID]
	d.mu.RUnlock()
	if exists {
		select {
		case ch <- driverID:
		case <-time.After(2 * time.Second):
			logger.Log.Warn().Str("ride_id", rideID).Str("driver_id", driverID).Msg("dispatcher too slow")
			return errors.New("dispatcher busy")
		}
	}

	rideData, _ := d.RideStore.GetRideByID(ctx, rideID)
	if rideData != nil {
		d.EventBus.PublishEvent(eventbus.ChannelRideAccepted, eventbus.RideAcceptedPayload{
			RideID:    rideID,
			DriverID:  driverID,
			UserID:    rideData.UserID,
			Status:    string(ride.StatusAssigned),
			RequestID: reqID,
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
		select {
		case ch <- driverID:
		case <-time.After(2 * time.Second):
			logger.Log.Warn().Str("ride_id", rideID).Str("driver_id", driverID).Msg("dispatcher too slow")
			return
		}
	}
}

func (d *Dispatcher) persistDispatchMetadata(rideID string, meta *ride.DispatchMetadata) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d.RideStore.UpdateDispatchMetadata(ctx, rideID, meta)
}

func (d *Dispatcher) startMatchingLoop(r *ride.Ride, reqID string) {
	rideIDStr := r.ID
	logger.Log.Info().Str("ride_id", rideIDStr).Str("request_id", reqID).Msg("Starting matching loop")

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
	hospitalID := ""
	if r.HospitalID != nil {
		hospitalID = *r.HospitalID
	}

	startTime := time.Now()

	checkTicker := time.NewTicker(5 * time.Second)
	defer checkTicker.Stop()
	candidateTimer := time.NewTimer(30 * time.Second)
	defer candidateTimer.Stop()

	pickupLat2, pickupLng2 := 0.0, 0.0
	if len(r.Pickup.Coordinates) == 2 {
		pickupLng2 = r.Pickup.Coordinates[0]
		pickupLat2 = r.Pickup.Coordinates[1]
	}
	dropoffLat, dropoffLng := 0.0, 0.0
	if len(r.Drop.Coordinates) == 2 {
		dropoffLng = r.Drop.Coordinates[0]
		dropoffLat = r.Drop.Coordinates[1]
	}
	fareVal := 0.0
	driverShareVal := 0.0
	if r.Fare != nil {
		fareVal = r.Fare.Total
		driverShareVal = r.Fare.DriverShare
	}
	tripDistanceKm := 0.0
	tripDurationSeconds := 0
	if r.Route != nil {
		tripDistanceKm = r.Route.DistanceKm
		tripDurationSeconds = r.Route.DurationSeconds
	}
	isSOS := r.EmergencyPriority > 0

	// offerOne publishes a single offer and waits for accept/decline/timeout.
	// Returns (accepted, aborted): accepted → caller assigns and returns;
	// aborted → ride left SEARCHING, caller must stop the loop.
	offerOne := func(candidate Candidate, idx, total int) (bool, bool) {
		logger.Log.Info().Str("ride_id", rideIDStr).Str("driver_id", candidate.DriverID).Int("eta", candidate.ETASeconds).Int("candidate", idx).Int("total", total).Str("request_id", reqID).Msg("Offering to driver")

		r.DispatchMetadata.OffersSent++

		d.EventBus.PublishEvent(eventbus.ChannelRideDriverOffered, eventbus.RideDriverOfferedPayload{
			RideID:              rideIDStr,
			DriverID:            candidate.DriverID,
			UserID:              r.UserID,
			PickupLat:           pickupLat2,
			PickupLng:           pickupLng2,
			PickupAddress:       r.PickupAddress,
			DropoffLat:          dropoffLat,
			DropoffLng:          dropoffLng,
			DropAddress:         r.DropAddress,
			ETASeconds:          candidate.ETASeconds,
			PickupDistanceKm:    candidate.DistanceKm,
			TripDistanceKm:      tripDistanceKm,
			TripDurationSeconds: tripDurationSeconds,
			Fare:                fareVal,
			DriverShare:         driverShareVal,
			PaymentMode:         r.PaymentMode,
			IsSOS:               isSOS,
			AmbTypeID:           ambTypeID,
			OfferedAt:           time.Now().UTC().Format(time.RFC3339),
			OfferExpiresIn:      30,
			RequestID:           reqID,
		})

		d.mu.RLock()
		acceptCh := d.acceptChannels[rideIDStr]
		declineCh := d.declineChannels[rideIDStr]
		d.mu.RUnlock()

		if !candidateTimer.Stop() {
			<-candidateTimer.C
		}
		candidateTimer.Reset(30 * time.Second)

		for {
			select {
			case acceptedDriverID := <-acceptCh:
				if acceptedDriverID == candidate.DriverID {
					r.DispatchMetadata.AssignmentLatencyMs = int(time.Since(startTime).Milliseconds())
					logger.Log.Info().Str("ride_id", rideIDStr).Str("driver_id", candidate.DriverID).Str("request_id", reqID).Msg("Driver accepted the ride")
					metrics.ObserveDispatchLatency(time.Since(startTime))
					d.persistDispatchMetadata(rideIDStr, &r.DispatchMetadata)
					return true, false
				}
				return false, false
			case declinedDriverID := <-declineCh:
				if declinedDriverID == candidate.DriverID {
					r.DispatchMetadata.OffersDeclined++
					logger.Log.Info().Str("ride_id", rideIDStr).Str("driver_id", candidate.DriverID).Str("request_id", reqID).Msg("Driver declined. Moving to next.")
				}
				return false, false
			case <-candidateTimer.C:
				r.DispatchMetadata.OffersTimedOut++
				logger.Log.Info().Str("ride_id", rideIDStr).Str("driver_id", candidate.DriverID).Str("request_id", reqID).Msg("Driver timed out. Moving to next.")
				return false, false
			case <-checkTicker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				currentRide, err := d.RideStore.GetRideByID(ctx, rideIDStr)
				cancel()
				if err != nil || currentRide == nil || currentRide.Status != ride.StatusSearching {
					logger.Log.Warn().Str("ride_id", rideIDStr).Msg("Ride was cancelled externally, stopping matching loop")
					return false, true
				}
			}
		}
	}

	offered := make(map[string]bool)
	hospitalOffered := 0

	// Phase 0 — hospital-first: single nearest MD-registered driver of the
	// destination hospital within 10 km. Miss falls through to normal flow.
	if hospitalID != "" {
		if hospCandidate, ok := d.Matcher.FindBestHospitalDriver(context.Background(), hospitalID, pickupLat, pickupLng, ambTypeID); ok {
			offered[hospCandidate.DriverID] = true
			hospitalOffered = 1
			logger.Log.Info().Str("ride_id", rideIDStr).Str("driver_id", hospCandidate.DriverID).Str("hospital_id", hospitalID).Str("request_id", reqID).Msg("Hospital-first offer")
			accepted, aborted := offerOne(hospCandidate, 1, 1)
			if aborted {
				return
			}
			if accepted {
				return
			}
		}
	}

	// Phase 1 — normal flow: nearest drivers, excluding the hospital-first
	// driver if already offered (never offer the same driver twice).
	candidates, err := d.Matcher.FindBestDrivers(context.Background(), pickupLat, pickupLng, 5, ambTypeID)
	if err == nil && len(candidates) > 0 && len(offered) > 0 {
		kept := candidates[:0]
		for _, c := range candidates {
			if !offered[c.DriverID] {
				kept = append(kept, c)
			}
		}
		candidates = kept
	}
	if err != nil || len(candidates) == 0 {
		if hospitalOffered == 0 {
			availableTypes := d.Matcher.FindAvailableOtherTypes(pickupLat, pickupLng, ambTypeID)
			logger.Log.Warn().Str("ride_id", rideIDStr).Str("request_id", reqID).Msg("No drivers found. Cancelling.")
			d.RideStore.CancelRide(context.Background(), rideIDStr, ride.StatusSearching, "no_drivers", availableTypes)
			d.EventBus.PublishEvent(eventbus.ChannelRideCancelled, eventbus.RideCancelledPayload{
				RideID:         rideIDStr,
				Reason:         "no_drivers",
				UserID:         r.UserID,
				RequestID:      reqID,
				AvailableTypes: availableTypes,
			})
			return
		}
		// Hospital driver was offered but normal pool is empty/exhausted:
		// fall through to all_drivers_exhausted below.
		candidates = nil
	}

	r.DispatchMetadata.CandidatesSearched = len(candidates) + hospitalOffered

	for i, candidate := range candidates {
		accepted, aborted := offerOne(candidate, i+1, len(candidates)+hospitalOffered)
		if aborted || accepted {
			return
		}
	}

	r.DispatchMetadata.AssignmentLatencyMs = int(time.Since(startTime).Milliseconds())
	d.persistDispatchMetadata(rideIDStr, &r.DispatchMetadata)

	availableTypes := d.Matcher.FindAvailableOtherTypes(pickupLat, pickupLng, ambTypeID)
	logger.Log.Warn().Str("ride_id", rideIDStr).Str("request_id", reqID).Msg("All candidates exhausted. Cancelling.")
	d.RideStore.CancelRide(context.Background(), rideIDStr, ride.StatusSearching, "all_drivers_exhausted", availableTypes)

	d.EventBus.PublishEvent(eventbus.ChannelRideCancelled, eventbus.RideCancelledPayload{
		RideID:         rideIDStr,
		Reason:         "all_drivers_exhausted",
		UserID:         r.UserID,
		RequestID:      reqID,
		AvailableTypes: availableTypes,
	})
}
