package dispatch

// Offline tests for broadcast ride offering: no DB, no network, no drivers.
// Location is a real in-memory store, ETAs use the local haversine fallback
// (empty API key), events flow through the real in-memory bus, and rides live
// in a fake RideStoreAPI. Run: go test ./internal/dispatch/ -run Broadcast -v

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/location"
	"ambigo-backend/internal/ride"
	"ambigo-backend/internal/websocket"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeRideStore is an in-memory RideStoreAPI mirroring real store semantics:
// CAS assign (only SEARCHING wins), status transitions, cancel reasons.
type fakeRideStore struct {
	mu            sync.Mutex
	rides         map[string]*ride.Ride
	seq           int
	cancelReasons map[string]string
	metaUpdates   int
}

func newFakeRideStore() *fakeRideStore {
	return &fakeRideStore{rides: map[string]*ride.Ride{}, cancelReasons: map[string]string{}}
}

func (f *fakeRideStore) CreateRide(_ context.Context, r *ride.Ride) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	if r.ID == "" {
		r.ID = fmt.Sprintf("ride-test-%d", f.seq)
	}
	r.Status = ride.StatusSearching
	f.rides[r.ID] = r
	return nil
}

func (f *fakeRideStore) GetRideByID(_ context.Context, rideID string) (*ride.Ride, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rides[rideID]; ok {
		return r, nil
	}
	return nil, nil
}

func (f *fakeRideStore) AtomicAssignDriver(_ context.Context, rideID, driverID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rides[rideID]
	if !ok || r.Status != ride.StatusSearching {
		return errors.New("ride is no longer available")
	}
	r.Status = ride.StatusAssigned
	r.DriverID = &driverID
	return nil
}

func (f *fakeRideStore) CancelRide(_ context.Context, rideID string, _ ride.RideStatus, reason string, _ ...[]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rides[rideID]; ok {
		r.Status = ride.StatusCancelled
	}
	f.cancelReasons[rideID] = reason
	return nil
}

func (f *fakeRideStore) UpdateDispatchMetadata(_ context.Context, _ string, _ *ride.DispatchMetadata) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metaUpdates++
	return nil
}

func (f *fakeRideStore) CancelStaleSearchingRides(_ context.Context, _ time.Duration) (int64, error) {
	return 0, nil
}

func (f *fakeRideStore) GetRideRegionID(_ context.Context, _ string) (*string, error) {
	return nil, nil
}

func (f *fakeRideStore) GetCurrentRide(_ context.Context, _, _ string) (*ride.Ride, error) {
	return nil, nil
}

func (f *fakeRideStore) UpdateRideStatus(_ context.Context, rideID string, _, next ride.RideStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rides[rideID]; ok {
		r.Status = next
	}
	return nil
}

func (f *fakeRideStore) ListTrail(_ context.Context, _ string) ([]ride.TrailPoint, error) {
	return nil, nil
}

func (f *fakeRideStore) GetRideHistory(_ context.Context, _, _ string, _, _ int64) ([]*ride.Ride, error) {
	return nil, nil
}

func (f *fakeRideStore) Pool() *pgxpool.Pool { return nil }

func (f *fakeRideStore) WithTx(_ pgx.Tx) *ride.Store { return nil }

func testBroadcastWorld(t *testing.T, driverIDs []string) (*Dispatcher, *fakeRideStore, *eventbus.InMemoryBus) {
	t.Helper()
	bus := eventbus.NewInMemoryBus()
	ls := location.NewMemoryStore()
	for i, id := range driverIDs {
		// Slight spread so ETAs differ; all well inside ring 1 (~0.5km).
		if err := ls.UpdateLocation(id, 17.3850+float64(i)*0.0005, 78.4867); err != nil {
			t.Fatal(err)
		}
		if err := ls.SetDriverVehicleType(id, "type-a"); err != nil {
			t.Fatal(err)
		}
	}
	m := NewMatcher(ls, NewRouteClient("", ""), map[string]string{"type-a": "Test Ambulance"})
	store := newFakeRideStore()
	d := NewDispatcher(m, store, bus, &websocket.Manager{})
	return d, store, bus
}

func newBroadcastTestRide() *ride.Ride {
	ambType := "type-a"
	return &ride.Ride{
		UserID:        "user-1",
		AmbTypeID:     &ambType,
		Pickup:        ride.GeoJSONPoint{Type: "Point", Coordinates: []float64{78.4867, 17.3850}},
		PickupAddress: "Pickup",
		Drop:          ride.GeoJSONPoint{Type: "Point", Coordinates: []float64{78.4900, 17.3900}},
		DropAddress:   "Drop",
		Route:         &ride.Route{DistanceKm: 1.4, DurationSeconds: 300},
		Fare:          &ride.Fare{Total: 900, DriverShare: 855, Currency: "INR"},
		PaymentMode:   "cash",
	}
}

func nextEvent(t *testing.T, ch chan []byte, what string) []byte {
	t.Helper()
	select {
	case raw := <-ch:
		return raw
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}

func collectOffered(t *testing.T, ch chan []byte, n int) []eventbus.RideDriverOfferedPayload {
	t.Helper()
	out := make([]eventbus.RideDriverOfferedPayload, 0, n)
	for len(out) < n {
		var p eventbus.RideDriverOfferedPayload
		if err := json.Unmarshal(nextEvent(t, ch, "offer"), &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func offeredDriverSet(offers []eventbus.RideDriverOfferedPayload) map[string]bool {
	set := map[string]bool{}
	for _, o := range offers {
		set[o.DriverID] = true
	}
	return set
}

func TestBroadcastAllFiveGetOffer(t *testing.T) {
	drivers := []string{"d1", "d2", "d3", "d4", "d5"}
	d, _, bus := testBroadcastWorld(t, drivers)
	offeredCh := bus.SubscribeWithChan(eventbus.ChannelRideDriverOffered)

	r := newBroadcastTestRide()
	if err := d.RequestRide(context.Background(), r); err != nil {
		t.Fatal(err)
	}

	offers := collectOffered(t, offeredCh, 5)
	got := offeredDriverSet(offers)
	for _, id := range drivers {
		if !got[id] {
			t.Fatalf("driver %s got no offer; got %v", id, got)
		}
	}
	if r.DispatchMetadata.OffersSent != 5 {
		t.Fatalf("expected OffersSent=5, got %d", r.DispatchMetadata.OffersSent)
	}
	// Same fare for everyone.
	for _, o := range offers {
		if o.Fare != 900 || o.DriverShare != 855 || o.OfferExpiresIn != 30 {
			t.Fatalf("bad offer payload: %+v", o)
		}
	}
}

func TestBroadcastFirstAcceptWins(t *testing.T) {
	drivers := []string{"d1", "d2", "d3", "d4", "d5"}
	d, store, bus := testBroadcastWorld(t, drivers)
	offeredCh := bus.SubscribeWithChan(eventbus.ChannelRideDriverOffered)
	withdrawnCh := bus.SubscribeWithChan(eventbus.ChannelRideWithdrawn)

	r := newBroadcastTestRide()
	if err := d.RequestRide(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	collectOffered(t, offeredCh, 5)

	// Two drivers tap Accept at the same time — exactly one must win.
	type acceptRes struct {
		id  string
		err error
	}
	resCh := make(chan acceptRes, 2)
	for _, id := range []string{"d1", "d2"} {
		go func(id string) {
			resCh <- acceptRes{id, d.HandleDriverAccept(context.Background(), r.ID, id)}
		}(id)
	}
	winner := ""
	for i := 0; i < 2; i++ {
		res := <-resCh
		if res.err == nil {
			if winner != "" {
				t.Fatalf("two winners: %s and %s", winner, res.id)
			}
			winner = res.id
		}
	}
	if winner == "" {
		t.Fatal("expected exactly one winner, got none")
	}

	// The other four must be withdrawn, never the winner.
	losers := map[string]bool{}
	for len(losers) < 4 {
		var p eventbus.RideWithdrawnPayload
		if err := json.Unmarshal(nextEvent(t, withdrawnCh, "withdraw"), &p); err != nil {
			t.Fatal(err)
		}
		if p.RideID != r.ID {
			t.Fatalf("withdraw for wrong ride: %s", p.RideID)
		}
		if p.DriverID == winner {
			t.Fatalf("winner %s was withdrawn", winner)
		}
		losers[p.DriverID] = true
	}

	stored, _ := store.GetRideByID(context.Background(), r.ID)
	if stored.Status != ride.StatusAssigned {
		t.Fatalf("expected ASSIGNED, got %s", stored.Status)
	}
	if stored.DriverID == nil || *stored.DriverID != winner {
		t.Fatalf("expected driver %s, got %+v", winner, stored.DriverID)
	}
	if r.DispatchMetadata.OffersTimedOut != 0 {
		t.Fatalf("expected no timeouts, got %d", r.DispatchMetadata.OffersTimedOut)
	}
}

func TestBroadcastAllDeclineCancels(t *testing.T) {
	drivers := []string{"d1", "d2", "d3"}
	d, store, bus := testBroadcastWorld(t, drivers)
	offeredCh := bus.SubscribeWithChan(eventbus.ChannelRideDriverOffered)
	withdrawnCh := bus.SubscribeWithChan(eventbus.ChannelRideWithdrawn)
	cancelledCh := bus.SubscribeWithChan(eventbus.ChannelRideCancelled)

	r := newBroadcastTestRide()
	if err := d.RequestRide(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	collectOffered(t, offeredCh, 3)

	for _, id := range drivers {
		d.HandleDriverDecline(context.Background(), r.ID, id)
	}

	var cancelled eventbus.RideCancelledPayload
	if err := json.Unmarshal(nextEvent(t, cancelledCh, "cancel"), &cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled.Reason != "all_drivers_exhausted" {
		t.Fatalf("expected all_drivers_exhausted, got %s", cancelled.Reason)
	}
	if r.DispatchMetadata.OffersDeclined != 3 {
		t.Fatalf("expected OffersDeclined=3, got %d", r.DispatchMetadata.OffersDeclined)
	}
	stored, _ := store.GetRideByID(context.Background(), r.ID)
	if stored.Status != ride.StatusCancelled {
		t.Fatalf("expected CANCELLED, got %s", stored.Status)
	}
	// Nobody won, so nobody is withdrawn.
	select {
	case raw := <-withdrawnCh:
		t.Fatalf("unexpected withdraw: %s", string(raw))
	case <-time.After(300 * time.Millisecond):
	}
}

func TestWithdrawOthersSkipsWinner(t *testing.T) {
	bus := eventbus.NewInMemoryBus()
	d := NewDispatcher(nil, newFakeRideStore(), bus, &websocket.Manager{})
	ch := bus.SubscribeWithChan(eventbus.ChannelRideWithdrawn)

	d.withdrawOthers("ride-x", []Candidate{{DriverID: "d1"}, {DriverID: "d2"}, {DriverID: "d3"}}, "d2", "req-1")

	got := map[string]bool{}
	for len(got) < 2 {
		var p eventbus.RideWithdrawnPayload
		if err := json.Unmarshal(nextEvent(t, ch, "withdraw"), &p); err != nil {
			t.Fatal(err)
		}
		if p.DriverID == "d2" {
			t.Fatal("winner withdrawn")
		}
		got[p.DriverID] = true
	}
	if !got["d1"] || !got["d3"] {
		t.Fatalf("expected d1+d3 withdrawn, got %v", got)
	}
}
