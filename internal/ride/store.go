package ride

import (
	"context"
	"errors"
	"time"

	"ambigo-backend/internal/retry"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type Store struct {
	collection *mongo.Collection
}

func NewStore(collection *mongo.Collection) *Store {
	return &Store{
		collection: collection,
	}
}

// CreateRide inserts a new ride document into MongoDB with STATUS_SEARCHING
func (s *Store) CreateRide(ctx context.Context, ride *Ride) error {
	ride.ID = primitive.NewObjectID()
	ride.Status = StatusSearching
	ride.Time.CreatedAt = time.Now()

	return retry.Do(ctx, retry.Default, func(ctx context.Context) error {
		_, err := s.collection.InsertOne(ctx, ride)
		return err
	})
}

// AtomicAssignDriver safely assigns a driver to a ride ONLY if the ride is still SEARCHING.
// This prevents double-booking if two drivers hit 'Accept' simultaneously.
func (s *Store) AtomicAssignDriver(ctx context.Context, rideID string, driverID string) error {
	objID, err := primitive.ObjectIDFromHex(rideID)
	if err != nil {
		return err
	}

	return retry.Do(ctx, retry.Default, func(ctx context.Context) error {
		filter := bson.M{
			"_id":    objID,
			"status": StatusSearching,
		}

		update := bson.M{
			"$set": bson.M{
				"status":           StatusAssigned,
				"driver_id":        driverID,
				"time.assigned_at": time.Now(),
			},
		}

		result := s.collection.FindOneAndUpdate(ctx, filter, update, options.FindOneAndUpdate().SetReturnDocument(options.After))
		if result.Err() != nil {
			if result.Err() == mongo.ErrNoDocuments {
				return errors.New("ride is no longer available or does not exist")
			}
			return result.Err()
		}
		return nil
	})
}

// UpdateRideStatus updates the ride status, validating the transition first.
func (s *Store) UpdateRideStatus(ctx context.Context, rideID string, currentStatus, nextStatus RideStatus) error {
	objID, err := primitive.ObjectIDFromHex(rideID)
	if err != nil {
		return err
	}

	if err := ValidateTransition(currentStatus, nextStatus); err != nil {
		return err
	}

	return retry.Do(ctx, retry.Default, func(ctx context.Context) error {
		filter := bson.M{
			"_id":    objID,
			"status": currentStatus,
		}

		updateField := ""
		switch nextStatus {
		case StatusArrived:
			updateField = "time.arrived_at"
		case StatusInProgress:
			updateField = "time.started_at"
		case StatusCompleted:
			updateField = "time.completed_at"
		case StatusCancelled:
			updateField = "time.cancelled_at"
		}

		setDoc := bson.M{"status": nextStatus}
		if updateField != "" {
			setDoc[updateField] = time.Now()
		}

		update := bson.M{
			"$set": setDoc,
		}

		result, err := s.collection.UpdateOne(ctx, filter, update)
		if err != nil {
			return err
		}
		if result.ModifiedCount == 0 {
			return errors.New("failed to update status, ride might have already changed state")
		}
		return nil
	})
}

// GetRideByID retrieves a single ride document
func (s *Store) GetRideByID(ctx context.Context, rideID string) (*Ride, error) {
	objID, err := primitive.ObjectIDFromHex(rideID)
	if err != nil {
		return nil, err
	}

	var ride Ride
	err = s.collection.FindOne(ctx, bson.M{"_id": objID}).Decode(&ride)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &ride, nil
}

// GetRideHistory returns a paginated list of rides for a specific user or driver
func (s *Store) GetRideHistory(ctx context.Context, entityID string, role string, limit, skip int64) ([]*Ride, error) {
	var filter bson.M
	if role == "user" {
		filter = bson.M{"user_id": entityID}
	} else if role == "driver" {
		filter = bson.M{"driver_id": entityID}
	} else {
		return nil, errors.New("invalid role")
	}

	// Filter out cancelled rides or searching rides if you only want history,
	// but let's return all rides sorted by creation time descending.
	opts := options.Find().SetSort(bson.M{"time.created_at": -1}).SetSkip(skip).SetLimit(limit)

	cursor, err := s.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var rides []*Ride
	if err = cursor.All(ctx, &rides); err != nil {
		return nil, err
	}

	return rides, nil
}

// UpdateDispatchMetadata persists the dispatch metadata counters for a ride.
func (s *Store) UpdateDispatchMetadata(ctx context.Context, rideID string, meta *DispatchMetadata) error {
	objID, err := primitive.ObjectIDFromHex(rideID)
	if err != nil {
		return err
	}
	_, err = s.collection.UpdateOne(ctx, bson.M{"_id": objID}, bson.M{
		"$set": bson.M{"dispatch_metadata": meta},
	})
	return err
}

// CancelStaleSearchingRides cancels all rides in SEARCHING state older than maxAge.
// Returns the count of cancelled rides and any errors encountered.
func (s *Store) CancelStaleSearchingRides(ctx context.Context, maxAge time.Duration) (int64, error) {
	cutoff := time.Now().Add(-maxAge)
	filter := bson.M{
		"status":              StatusSearching,
		"time.created_at": bson.M{"$lt": cutoff},
	}
	update := bson.M{
		"$set": bson.M{
			"status":            StatusCancelled,
			"time.cancelled_at": time.Now(),
		},
	}
	result, err := s.collection.UpdateMany(ctx, filter, update)
	if err != nil {
		return 0, err
	}
	return result.ModifiedCount, nil
}

// ListRidesByStatus returns a paginated list of rides matching one of the given statuses
func (s *Store) ListRidesByStatus(ctx context.Context, statuses []RideStatus, limit, skip int64) ([]*Ride, error) {
	filter := bson.M{"status": bson.M{"$in": statuses}}
	opts := options.Find().SetSort(bson.M{"time.created_at": -1}).SetSkip(skip).SetLimit(limit)

	cursor, err := s.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var rides []*Ride
	if err = cursor.All(ctx, &rides); err != nil {
		return nil, err
	}
	if rides == nil {
		rides = []*Ride{}
	}
	return rides, nil
}

// GetCurrentRide returns the currently active ride (if any) for a user or driver
func (s *Store) GetCurrentRide(ctx context.Context, entityID string, role string) (*Ride, error) {
	var filter bson.M
	if role == "user" {
		filter = bson.M{"user_id": entityID}
	} else if role == "driver" {
		filter = bson.M{"driver_id": entityID}
	} else {
		return nil, errors.New("invalid role")
	}

	// Active ride means status is one of: SEARCHING, ASSIGNED, ARRIVED, IN_PROGRESS
	filter["status"] = bson.M{
		"$in": []RideStatus{StatusSearching, StatusAssigned, StatusArrived, StatusInProgress},
	}

	opts := options.FindOne().SetSort(bson.M{"time.created_at": -1}) // Get the latest one if multiple exist somehow

	var ride Ride
	err := s.collection.FindOne(ctx, filter, opts).Decode(&ride)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil // No active ride
		}
		return nil, err
	}
	return &ride, nil
}
